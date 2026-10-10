# Local Kind and Test Mode

Use this flow when you need a full local platform: API, UI, operator, registry,
Traefik, platform services, and real MCP ingress routes.

## Prerequisites

### Test replica modes

`setup --test-mode` builds and publishes native platform images and deploys
one replica per platform Deployment and StatefulSet. Kafka uses one controller
and broker, replication factor 1, and minimum in-sync replicas 1. DaemonSets
still run once per node. This is a functional test profile without redundancy.

Use `setup --test-mode --test-multi-replica` for tests that require concurrent
replicas, such as shared UI sessions or failover. The flag uses normal manifest
counts: two UI/operator/Traefik/gateway replicas and three Kafka/ingest/processor
replicas. Naturally single-instance databases retain their normal count.
The flag requires `--test-mode`; normal setup defaults are unchanged.

Choose the mode when creating the test cluster. Switching a populated Kafka
store between one and three replicas changes both its controller quorum and
topic replication. Setup rejects that transition and preserves the store;
create a separate fresh Kind cluster for the other profile. Do not reuse the
old volumes as an automatic migration.

For QA E2E, `E2E_TEST_MULTI_REPLICA=1` passes the same flag to setup. Use it on
a fresh test cluster when the scenario requires the normal replica layout.

### Host CPU and memory

Plan for the **full platform**, including Kafka, ClickHouse,
Postgres, and the observability services. A bare Kind cluster uses much less
memory than this installation.

| Use | Docker/Kind CPU allocation | Docker/Kind memory allocation | Host guidance |
|---|---|---|---|
| Run the full stack and light functional tests | 4 CPUs | 6GiB lower planning bound; 8GiB preferred | 16GB RAM recommended on macOS/Windows to leave room for the OS, editor, and browser |
| Build from source while the stack runs | 4 or more CPUs | 8GiB preferred | 16GB RAM recommended; use sequential builds initially |

These are planning recommendations from one ARM64 development installation,
not certified minimums or load-test capacity. On native Linux, reserve memory
for the host as well as the containers; on macOS/Windows, configure the Docker
VM allocation explicitly. See [Docker Desktop resource settings](https://docs.docker.com/desktop/settings-and-maintenance/settings/#advanced).

An 8GB Mac with a 4-CPU, 6GiB Docker VM ran the full stack, but first-time
parallel source builds caused substantial host memory pressure and slow
startup. It is a constrained testing option, rather than the recommended
development host. `setup` builds sequentially by default: leave
`--parallel-builds` off on small hosts. For QA E2E, start with
`E2E_IMAGE_BUILD_PARALLELISM=1` and `E2E_IMAGE_MIRROR_PARALLELISM=1`.

Measured on 2026-10-08 with Kind 0.33.0 / Kubernetes 1.37.0, one ARM64
node, native service images from `15dc3f18`, and no MCP test traffic. Neither
baseline included Metrics Server:

| Quantity | Default single replica | `--test-multi-replica` |
|---|---|---|
| Active pod CPU requests, including Kubernetes | 2.25 cores | 3.30 cores |
| Active pod memory requests | 2.53GiB | 4.35GiB |
| Kind node memory working set | 3.07GiB | 3.95GiB |
| Kind node CPU usage, one sample | 0.54 core | 0.59 core |
| Active pods | 31 | 41 |

The multi-replica profile reserves an additional **1.05 CPU cores and 1.81GiB
RAM** before adding MCP servers. Actual usage depends on traffic; the idle CPU
samples do not establish load capacity. Metrics Server adds requests of 100m
CPU and 200Mi RAM; its observed usage was 9m CPU and 17Mi RAM.

The working set includes Kubernetes overhead. BuildKit runs outside the Kind
node and needs additional VM/host headroom. On this 8GiB Mac, one later idle
sample had 6.79GiB host swap and 3.56GiB compressed memory while the node used
3.27GiB. This is why 16GB host RAM is recommended for source development.
Resource limits can exceed node capacity because services do not all reach
their limits together; re-measure under your workload.

The tested Colima disk allocation was **60GiB**, with about **32.65GiB used**
on its shared filesystem after builds and both test profiles. This includes
image/build caches and a retained stopped cluster, so it is a tested disk
budget, not a clean-install minimum. The single-replica platform declared
85GiB of PVC capacities across seven volumes; thin local-path storage did not
consume that capacity immediately. Do not add PVC capacities to filesystem
usage or add overlapping Docker image/cache categories. Data retention and
additional server images increase actual consumption.

With the isolated test kubeconfig below, inspect your installation using:

```bash
docker stats --no-stream mcp-runtime-control-plane
kubectl describe node mcp-runtime-control-plane
kubectl get --raw /api/v1/nodes/mcp-runtime-control-plane/proxy/stats/summary |
  jq '.node | {cpu, memory}'
```

The node summary reads kubelet statistics and does not require metrics-server.
The commands require permission to inspect the contributor cluster.

A later resource-instrumented local E2E attempt on 2026-10-10 collected
134 valid Kubernetes samples out of 213 before failing during setup because
an image-push helper could not be scheduled while the node was unhealthy.
Its sampled node peaks were 3.54 CPU cores and 3.39GiB working memory;
pod requests peaked at 2.45 cores and 2.85GiB, and shared node filesystem
usage reached 42.02GiB. These are partial startup observations from a failed
run, not a completed functional or traffic benchmark. They do not establish
minimum hardware requirements; retain headroom and repeat the suite on a
healthy host before sizing for a workload.

### macOS with Colima

The measurement host was macOS 15.5 on ARM64 (Apple Silicon), with 8GB physical
RAM and 8 logical CPUs. Colima 0.10.3 used the Virtualization.framework (`vz`)
backend and `virtiofs`, with 4 virtual CPUs, 6GiB memory, and a 60GiB disk.
The Kind node reported about 5.77GiB allocatable memory after VM overhead.
The Docker engine inside the VM was Linux ARM64, version 29.5.2. Kind 0.33.0
created one Kubernetes 1.37.0 control-plane node. Service manifests and their
ELF executables were checked as ARM64; these numbers do not describe an
AMD64-under-emulation deployment or a production load test.

Install the local tools, then start the VM before creating Kind:

```bash
brew install colima docker docker-buildx kind
# Configure the Homebrew buildx CLI plugin as shown by:
brew info docker-buildx
colima start --cpu 4 --memory 6 --disk 60 --vm-type vz \
  --mount-type virtiofs --downloader curl
docker context use colima
docker version
docker buildx version
kind version
```

Use the isolated kubeconfig and registry-mirror Kind configuration in the next
section, then run `setup --test-mode` for the smaller profile. `kubectl`, Go,
and the other tools below are still required. `--downloader curl` was used
after Colima's default VM-image downloader timed out on this host. For a
16GB host, allocate 8GiB to Colima for more build headroom rather than copying
the constrained 6GiB measurement configuration as a universal recommendation.

From a source checkout at the repository root, install Docker, Kind, `kubectl`,
Go `1.26+`, Make, `curl`, `jq`, and Python 3. Then build the
CLI:

```bash
make deps
make build
```

## Create or reuse the Kind cluster

First run `kind get clusters`. If `mcp-runtime` already exists, use the reuse
instructions below; do not run `kind create cluster` a second time.

The documented test-mode install emits pod images that use
`registry.registry.svc.cluster.local:5000/...`. The Kind node needs a matching
containerd mirror before setup runs.

Keep the contributor cluster in its own kubeconfig so a fresh setup never
depends on the ambient context or a production kubeconfig. This leaves
`~/.kube/config` and any production credentials untouched.

```bash
mkdir -p -m 700 "$HOME/.kube"
TEST_KUBECONFIG="$HOME/.kube/test-mcp-runtime-config"
KIND_CONFIG="$(mktemp)"
trap 'rm -f "$KIND_CONFIG"' EXIT
cat > "$KIND_CONFIG" <<'EOF'
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
containerdConfigPatches:
  - |-
    [plugins."io.containerd.grpc.v1.cri".registry.mirrors."registry.registry.svc.cluster.local:5000"]
      endpoint = ["http://127.0.0.1:32000"]
EOF

kind create cluster --name mcp-runtime --config "$KIND_CONFIG" \
  --kubeconfig "$TEST_KUBECONFIG" --wait 120s
chmod 600 "$TEST_KUBECONFIG"
kubectl --kubeconfig "$TEST_KUBECONFIG" config use-context kind-mcp-runtime
export KUBECONFIG="$TEST_KUBECONFIG"
kubectl config current-context
kubectl get nodes
```

For an existing `mcp-runtime` Kind cluster, export its kubeconfig into the
isolated test file, rename the generated context, and verify the nodes before
using it:

```bash
TEST_KUBECONFIG="$HOME/.kube/test-mcp-runtime-config"
kind export kubeconfig --name mcp-runtime --kubeconfig "$TEST_KUBECONFIG"
chmod 600 "$TEST_KUBECONFIG"
kubectl --kubeconfig "$TEST_KUBECONFIG" config use-context kind-mcp-runtime
export KUBECONFIG="$TEST_KUBECONFIG"
kubectl get nodes
```

## Capture resources during E2E

QA E2E captures resource samples by default (`E2E_CAPTURE_RESOURCES=1`). It
starts before cluster/setup work and continues through the selected tests.
Use an explicit Kind kubeconfig and preserve the reports:

```bash
KUBECONFIG="$TEST_KUBECONFIG" CLUSTER_NAME=mcp-runtime \
  E2E_CACHE_MODE=1 E2E_KEEP_CLUSTER=1 \
  E2E_IMAGE_BUILD_PARALLELISM=1 E2E_IMAGE_MIRROR_PARALLELISM=1 \
  E2E_ARTIFACT_DIR="$PWD/.local/e2e-resource-report" \
  E2E_SCENARIOS=smoke-auth,cli-platform,governance,trust,oauth,observability \
  bash test/e2e/qa-e2e.sh
```

For a fresh multi-replica cluster, add `E2E_TEST_MULTI_REPLICA=1` and use a
different cluster name/kubeconfig. Keep the context named `kind-<cluster-name>`;
the sampler refuses other contexts and never uses an ambient target.

The artifact directory contains `resource-usage/samples.jsonl`, `samples.csv`,
`summary.json`, and `summary.md`, plus stage timings. Measurements include
kubelet node/pod CPU and memory, pod requests, Metrics Server when installed,
Docker node statistics, host CPU/swap/disk, Colima VM memory, and actual local
volume/containerd disk usage. Disk collection runs about once per minute;
other samples target 15 seconds, with collection duration recorded. Missing
samples and partial disk coverage are reported, not counted as zero.

`grafana.json` and `grafana.md` capture Prometheus time series through Grafana
for the same test window: scrape health, application CPU/RSS/goroutines,
gateway requests/latency, and container/network/volume metrics where collected.
The bundled scrape configuration currently exposes application metrics, but
container, kube-state, and volume queries can have no series. Capture records
those gaps explicitly; Metrics Server does not populate Prometheus history.
Grafana credentials are read privately from the local Secret and are never
written to the reports.

A sampled peak can miss a short burst. Functional E2E exercises deployments
and MCP traffic, but does not establish capacity at a fixed requests-per-second
rate. Stage labels identify the last stage started; parallel stages overlap.
Report the profile, revision, node/host architecture, VM allocation, cache
state, scenario set, test result, and collection gaps alongside any sizing
recommendation. Include Kubernetes and build overhead plus headroom; do not
publish an idle sample as a production minimum.

## Install MCP Runtime

Run preflight checks, then install with the HTTP ingress overlay:

```bash
./bin/mcp-runtime bootstrap

./bin/mcp-runtime cluster doctor

MCP_SETUP_WAIT_TIMEOUT=900 \
  ./bin/mcp-runtime setup --test-mode \
  --kubeconfig "$TEST_KUBECONFIG" \
  --ingress-manifest config/ingress/overlays/http
```

First-time image downloads can take longer than the defaults on a constrained
host or slow connection. Check pod events and logs before treating a timeout
as a failed service. If the pods are progressing, rerun the same supported
setup with `MCP_SETUP_WAIT_TIMEOUT=1800` and
`MCP_DEPLOYMENT_TIMEOUT=30m`; completed builds and persistent data are reused.
The setup timeout uses seconds; the deployment timeout uses a duration such
as `30m`. Longer waits do not repair crash loops, failed image pulls, or
insufficient CPU/memory.

For a fresh cluster that needs the normal multi-replica profile, use:

```bash
MCP_SETUP_WAIT_TIMEOUT=1800 MCP_DEPLOYMENT_TIMEOUT=30m \
  ./bin/mcp-runtime setup --test-mode --test-multi-replica \
  --kubeconfig "$TEST_KUBECONFIG" \
  --ingress-manifest config/ingress/overlays/http
```

The setup flag `--kubeconfig` makes the target explicit; do not rely on the
ambient current context when installing either profile.

Check the platform:

```bash
./bin/mcp-runtime status
./bin/mcp-runtime cluster status
./bin/mcp-runtime registry status
./bin/mcp-runtime ops status   # admin kubectl
./bin/mcp-runtime cluster diagnostics
```

The same shell must keep `KUBECONFIG` set to
`$HOME/.kube/test-mcp-runtime-config` while running contributor commands.
Pass that path explicitly when invoking E2E scripts:

```bash
KUBECONFIG="$HOME/.kube/test-mcp-runtime-config" \
  E2E_CACHE_MODE=1 E2E_KEEP_CLUSTER=1 CLUSTER_NAME=mcp-runtime \
  E2E_SCENARIOS=smoke-auth,governance bash test/e2e/qa-e2e.sh
```

Expose the dashboard and MCP routes:

```bash
kubectl port-forward -n traefik svc/traefik 18080:8000 18443:8443
```

Local URLs:

| Surface | URL |
|---|---|
| Platform UI | `http://localhost:18080/` |
| Platform API | `http://localhost:18080/api/v1` |
| MCP route shape (plain Ingress) | `http://localhost:18080/<server-name>/mcp` |
| Adapter-certificate MCP routes | `https://localhost:18443/<server-name>/mcp` (`-k` for Traefik's local default cert; websecure-only when `MCP_ADAPTER_CERTIFICATES=true`) |

Keep the Traefik port-forward running while using the browser or `curl`.

If pods are Ready but the UI returns 404, check Traefik logs. Repeated
timeouts to `https://10.96.0.1:443` can mean its NetworkPolicy lacks the
Kubernetes API endpoint port (normally 6443). The bundled policy allows both
the Service and endpoint ports. For an existing repo-managed installation,
reconcile the corrected policy through the CLI from this source checkout:

```bash
./bin/mcp-runtime cluster config --kubeconfig "$TEST_KUBECONFIG" \
  --force-ingress-install \
  --ingress-manifest config/ingress/base/networkpolicy.yaml
```

This applies the policy without reapplying workload replica counts. External
ingress controllers retain their own network-policy configuration.

Platform and plain Ingress URLs are HTTP over a port-forward because
`--test-mode` with the HTTP ingress overlay is a local-only shape.
Adapter-certificate IngressRoutes still terminate TLS on `:18443`. Any shared
or public install uses `--with-tls` and the `platform`, `registry`, and `mcp`
hostnames instead; see [Deployment Options](../deployment-targets.md).

## Seeded Logins

`setup --test-mode` seeds local-only platform logins:

| Role | Email | Password |
|---|---|---|
| User | `test@mcpruntime.org` | `test@123` |
| Admin | `admin@mcpruntime.org` | `admin@123` |

These are controlled by `PLATFORM_DEV_*` keys in `mcp-platform-api-credentials`
Secret. They are for local debugging only.

Fresh clusters have only the seeded `test` and `admin` accounts. Create
additional teams and users through `mcp-runtime team create` and
`mcp-runtime team user create`; see [Multi-team isolation](../teams-and-access.md).

## Catalog visibility checks

Anonymous users must not see the MCP catalog:

```bash
curl -i http://localhost:18080/api/v1/runtime/servers
```

Expected status: `401 Unauthorized`.

Check the default test user:

```bash
rm -f /tmp/mcp-test-user-cookie.txt
curl -sS -c /tmp/mcp-test-user-cookie.txt \
  -H 'content-type: application/json' \
  -d '{"email":"test@mcpruntime.org","password":"test@123"}' \
  http://localhost:18080/auth/login

curl -sS -b /tmp/mcp-test-user-cookie.txt \
  http://localhost:18080/api/v1/runtime/servers |
  jq '{count: (.servers|length), names: [.servers[] | (.namespace + "/" + .name)]}'
```

In default tenant mode, signed-in users see MCPs from team namespaces they
belong to only. A setup installed with `--platform-mode org` instead shows the
shared org catalog from `mcp-servers-org`, and `--platform-mode public` shows
the public preview catalog from `mcp-servers-public`.

For a reproducible tenant-isolation exercise, follow
[Module 3: Multi-team setup](../learn/03-multi-team-access.md), which creates its
own teams and users. Account names from somebody else's reused cluster are
not prerequisites for this guide.

## Quick cluster inventory

```bash
kubectl get pods -n mcp-runtime -o wide
kubectl get pods -n mcp-platform -o wide
kubectl get mcpservers -A \
  -o custom-columns='NAMESPACE:.metadata.namespace,NAME:.metadata.name,TEAM:.spec.teamID,PATH:.spec.ingressPath,READY:.status.deploymentReady,GW:.status.gatewayReady'
kubectl get mcpaccessgrant,mcpagentsession -A -o wide
kubectl get ingress -A
```

Remove stale test resources through the CLI after logging in with an account
that can administer the server:

```bash
./bin/mcp-runtime access session delete <session-name> --namespace <namespace>
./bin/mcp-runtime access grant delete <grant-name> --namespace <namespace>
./bin/mcp-runtime server delete <server-name> --namespace <namespace>
```

Use `--help` to review confirmation flags before scripting cleanup. The
operator cleans up owned workload resources when the server is deleted; do
not delete shared analytics or registry credentials as part of server cleanup.

## Optional: bundled mcp-auth integration fixture

The bundled authorization server is opt-in and separate from MCP application
deployment. Setup pulls `princekrroshan01/mcp-auth-server:0.4.4` from Docker
Hub by default. Production deployments additionally require HTTPS
issuer/resource URLs, a provider connector, signing-key Secret, and TLS
Secret; test mode may use the local issuer:

```bash
./bin/mcp-runtime setup --test-mode \
  --with-mcp-auth-server \
  --ingress-manifest config/ingress/overlays/http
```

Validate the Go SDK example's server-side OAuth metadata with the Runtime CLI:

```bash
./bin/mcp-runtime server validate --metadata-dir examples/oauth-example-go-2025-11-25/.mcp
```

With `gateway.enabled: true`, Runtime validates OAuth tokens and applies its
grants, sessions, policy, and audit behavior. The Go source can also validate
OAuth directly with the `mcp-auth` SDK when deployed with the gateway disabled;
that standalone mode bypasses those gateway features. E2E uses distinct
`-gateway` and `-standalone` server names so the deployment modes are visible
in routes and cluster resources. To run the Go process without server auth,
omit the OAuth settings.

In `--test-mode` the authorization server is configured with the Go example
resource, so one deployment issues a token for the protected fixture.
Add resources with `--mcp-auth-resource-url` (repeat the flag or
comma-separate) when you deploy your own server:

```bash
./bin/mcp-runtime setup --test-mode \
  --with-mcp-auth-server \
  --mcp-auth-resource-url http://localhost:18080/my-server/mcp \
  --ingress-manifest config/ingress/overlays/http
```

Each value must equal the `spec.auth.audience` of the MCP server it fronts.
Outside `--test-mode`, resource URLs can be supplied as an optional bootstrap
list; the operator reconciles the list from current OAuth MCPServer audiences.
Any supplied value must be HTTPS, and the deployment additionally needs
`--mcp-auth-signing-key-secret` (a Secret holding the RSA signing key as
`private-key.pem`). Test mode lets the server generate an ephemeral key, which
it only permits for a loopback issuer; in production an ephemeral key would
invalidate every issued token on restart.

The standalone SDK fixtures verify tokens against the in-cluster JWKS endpoint
(`MCP_AUTH_JWKS_URL`) rather than discovering it, because the public issuer is
not reachable from inside a pod and the authorization server sits behind an
ingress that strips its path prefix. Issuer and audience are still validated in
full.

UI-only updates can use `update --build --only ui` with a local release manifest
and clean source checkout. Pass the Kind kubeconfig and context explicitly. The
bundled registry is reachable through the in-cluster publisher; Docker on the
host cannot resolve its Kubernetes Service address. Use `--image-platform
linux/arm64` on ARM64 Kind nodes.

Prometheus currently lacks some container, node filesystem and volume history
series in the bundled stack; [#680](https://github.com/mcp-runtime/mcp-runtime/issues/680)
tracks collection coverage. The E2E report distinguishes missing Grafana series
from zero usage and retains kubelet/host samples as separate observations.
