# Cluster Readiness

`./bin/mcp-runtime setup` installs the platform (registry, operator, ingress, sentinel) into an *already-running* Kubernetes cluster. You must configure the node's container runtime and host DNS stack yourself; the steps differ per distribution.

Without that configuration, you typically see:

- `./bin/mcp-runtime setup` fails at "Publish runtime images" with `dial tcp: lookup registry.local: no such host`.
- The operator pod goes to `ImagePullBackOff` with `10.43.x.x:5000: connection refused` or `no such host`.
- MCPServer pods get stuck in `ImagePullBackOff` pulling `registry.local/<server-name>`.

The sections below list what each distribution needs before you run `setup`.
To choose where to deploy, see [Deployment Targets](deployment-targets.md).

## Dev vs production readiness

Most examples on this page use the **dev/local** path: the bundled Docker
registry exposed by `NodePort`, HTTP registry access, `registry.local`, and
node-level insecure-registry or mirror configuration. Use it for kind,
minikube, Docker Desktop, single-node k3s, CI, and disposable test clusters.

Production clusters use the same readiness model with different platform
choices:

| Area | Dev / local default | Production expectation |
|---|---|---|
| Registry | Bundled Docker registry exposed through `NodePort` and `registry.local` | Managed or hardened registry such as ECR, Artifact Registry, ACR, Harbor, or an internal registry behind stable DNS |
| Transport | HTTP or an insecure registry mirror for fast iteration | HTTPS with a trusted certificate; use `setup --with-tls` when retaining the bundled registry |
| Node trust | `/etc/hosts`, containerd mirror entries, and Docker Desktop insecure-registry settings | Platform-managed DNS, trusted CAs, image pull policy, and audited runtime config across every node pool |
| Image credentials | Local helper flows and default service account image pull secrets | Dedicated pull secrets, workload identity, or cloud-native node registry auth |
| Ingress | Bundled Traefik overlay is acceptable | Existing platform ingress or gateway may be preferred; validate ingress class, TLS, DNS, and policy ownership |
| Persistence | Default registry storage is acceptable for throwaway clusters | Storage class, backup/restore, quota, retention, and registry HA need explicit owner decisions |
| Sentinel stack | Bundled stack is useful for development and demos | Size, retention, security, and observability integration should be reviewed before production use |

Do **not** use `insecure_skip_verify`, HTTP registries, wildcard insecure CIDR
ranges, or manual `/etc/hosts` edits in production. They are local
workarounds for kubelet image pulls. In production, use a registry name that
resolves through normal DNS and that every node trusts with TLS verification
enabled.

`./bin/mcp-runtime cluster diagnostics` validates the installed registry and
the rest of a live install. Setup itself also runs a short post-setup
operational smoke gate (nodes Ready, Bound PVCs, Postgres, platform-api
health/ready, Sentinel rollout health, auth probe) and fails if those basics
are not green. For the bundled
registry it probes the in-cluster `registry/registry` Service and selects HTTP
or HTTPS from the installed registry state: if `registry/registry-internal-tls`
exists, doctor probes `https://registry.registry.svc.cluster.local:5000/v2/`;
otherwise it probes the plain HTTP service. With a provisioned external
registry, interpret bundled-registry-specific failures against your own
registry architecture; the local workarounds may not apply.

Production readiness checklist:

- Choose the registry architecture: bundled registry with TLS, or a managed /
  hardened external registry.
- Ensure every node pool that can schedule MCP Runtime workloads can pull the
  operator, gateway proxy, Sentinel, and MCP server images.
- Configure image credentials with pull secrets, workload identity, or
  cloud-native node registry auth.
- Confirm the ingress class, public DNS, and TLS issuer are owned by the
  platform team and match the cluster's ingress controller.
- Confirm a default `StorageClass` exists, or set explicit storage choices for
  the registry and Sentinel data paths.
- Decide certificate management up front: Let's Encrypt with public DNS,
  `--tls-cluster-issuer` for an enterprise CA, or a preinstalled issuer.
- Review Sentinel sizing, retention, auth, Kubernetes API access, and
  observability integration before enabling it on production traffic; see
  [Platform service Kubernetes awareness and hardening](platform-services.md#kubernetes-awareness-and-hardening).
- Run `./bin/mcp-runtime setup --with-tls --strict-prod` for production-style
  validation, or document why a non-strict setup is intentional.

## Why these prerequisites exist

The registry runs as a Kubernetes `Service` of type `NodePort` (default `5000:32000/TCP`) with an `Ingress` at `registry.local`.

Three actors fetch images, and each resolves hostnames differently:

| Actor | What it pulls | DNS source |
|---|---|---|
| `./bin/mcp-runtime server push` (in-cluster mode) | Pushes from a helper pod after platform credential validation using the registry Service DNS | Cluster CoreDNS (always works) |
| `kubelet` on the node | Pulls operator / MCPServer images for pod creation | **Host DNS** (not CoreDNS) + containerd registry mirrors |
| Developer `docker push` / `docker pull` | Ad-hoc pushes or pulls from your laptop | Your local `/etc/hosts` / corporate DNS |

The CLI handles the in-cluster push path (`PushInCluster` rewrites the destination to the service DNS). You manage the developer path locally. **The distribution-specific config below configures the node/kubelet path.**

`setup --test-mode` uses the same model. It relaxes production guardrails, and
it still builds and pushes the operator, gateway proxy, and Sentinel images with
`latest` tags to the configured or bundled registry. Those pods still pull
through kubelet/containerd, so an HTTP bundled registry requires node trust for
the exact image host and port used in the rendered image references.

When setup uses the bundled registry, platform-owned image refs for the
operator, gateway proxy, and Sentinel services are rendered with the internal
registry endpoint, ClusterIP, or service-DNS host. They do not use the public
registry ingress hostname derived from `MCP_PLATFORM_DOMAIN`; that host serves
ingress routing and user-facing registry flows; set
`MCP_REGISTRY_ENDPOINT` or `MCP_REGISTRY_HOST` only when cluster nodes should
pull platform images through a specific internal registry endpoint.

### Registry setup modes

`setup --registry-mode` makes the registry path explicit:

| Mode | What setup does | Node requirement |
|------|-----------------|------------------|
| `auto` | Keeps existing behavior: use a provisioned registry config when present, otherwise install the bundled registry. | Depends on the resolved path. |
| `bundled-http` | Installs the bundled registry and keeps the registry pod on plain HTTP for internal platform pulls. Public ingress can still use TLS with `--with-tls`. | Configure every node/containerd runtime to allow the exact image host as an insecure registry. |
| `bundled-https` | Installs the bundled registry with TLS served by the registry pod itself. Public ingress TLS can still use ACME; the registry pod uses a separate internal certificate from setup-generated `mcp-runtime-ca`, or from a non-ACME `--tls-cluster-issuer` when you provide one. | Configure every node to resolve or mirror the rendered registry host and trust the internal issuing CA. |
| `external` | Skips the bundled registry and pushes setup images to a provisioned registry. | Registry DNS, TLS, auth, pull secrets, and node trust are owned by the external registry platform. |

For bundled HTTPS without an explicit `MCP_REGISTRY_ENDPOINT`, setup renders
platform image refs with `registry.registry.svc.cluster.local:5000` so the
internal registry certificate can include a stable service DNS SAN. For image
pulls, Kubernetes nodes use host DNS and their own trust store, which do not
include cluster DNS or the MCP Runtime CA. Configure containerd/k3s/your node runtime to resolve or mirror that host
and trust the CA before relying on this mode in **lab** clusters.

**Public TLS with `MCP_PLATFORM_DOMAIN` (production k3s shape):** when the
registry is exposed at `registry.<domain>` with Let's Encrypt or an enterprise
issuer, set `MCP_REGISTRY_ENDPOINT=registry.<domain>` before setup so kubelet
pulls match the certificate SANs. Do **not** use the registry Service ClusterIP
(for example `10.43.x.x:5000`) on this path; pulls fail with
`x509: cannot validate certificate ... doesn't contain any IP SANs`. See
[Reference Deployment - Environment variable reference](reference-deployment.md#environment-variable-reference)
and [Deployment Targets - bundled HTTPS](deployment-targets.md#option-a-bundled-https-registry-on-prem-reference).

When using the built-in issuer for internal registry pod TLS, setup creates
`cert-manager/mcp-runtime-ca` if it is missing; export its `tls.crt` and add that
certificate to each node runtime trust store. After setup, `cluster diagnostics` uses
the `registry-internal-tls` Secret as the signal to probe the registry Service
over HTTPS; a successful doctor registry probe confirms in-cluster push-helper
reachability, while kubelet image pulls still depend on node/containerd trust
for the exact rendered image host.

### Setup image platform

`setup` builds the operator, gateway proxy, and Sentinel images as Linux
container images. By default it inspects `kubernetes.io/arch` on cluster nodes
and builds a single-platform image for a homogeneous cluster, for example
`linux/amd64` on standard VPS nodes or `linux/arm64` on ARM nodes. Mixed-arch
clusters are rejected until multi-arch manifest publishing is supported, because
the platform deployments do not pin pods to architecture-specific node pools.

Set `MCP_IMAGE_PLATFORM=linux/amd64` or `MCP_IMAGE_PLATFORM=linux/arm64` when
you need to override detection, such as building from an Apple Silicon laptop
for an amd64 k3s VPS cluster.

## External registry path

Use an external registry when your platform already provides ECR, Artifact
Registry, ACR, Harbor, or another hardened registry with DNS, TLS, retention,
and auth controls. In that mode, `setup` skips installing the bundled registry
and configures the operator to use the provisioned registry.

Configure it either with the CLI:

```bash
./bin/mcp-runtime registry provision --url registry.example.com
```

or directly on setup:

```bash
./bin/mcp-runtime setup --registry-mode external --with-tls --strict-prod \
  --external-registry-url registry.example.com \
  --external-registry-username <user> \
  --external-registry-password <password>
```

or with environment variables before `setup`:

```bash
export PROVISIONED_REGISTRY_URL=registry.example.com
export PROVISIONED_REGISTRY_USERNAME=<user>      # optional
export PROVISIONED_REGISTRY_PASSWORD=<password>  # optional
```

When external registry credentials are configured, setup creates pull secrets
for Sentinel workloads and a separate `mcp-runtime/mcp-runtime-registry-pull-creds`
Secret for the operator Deployment. If you intentionally roll platform
workloads from an already-authenticated public registry, set
`MCP_PLATFORM_IMAGE_PULL_SECRET=<secret-name>` before setup; the Secret must
exist in `mcp-runtime` for the operator, in `mcp-platform` for control-plane
workloads, and in `mcp-observability` for the telemetry stack.

If you configured the registry with `registry provision` or environment
variables first, run setup with production validation:

```bash
./bin/mcp-runtime setup --registry-mode external --with-tls --strict-prod
```

Before rendering MCP server manifests for review or GitOps, set the image host
that cluster nodes should pull from:

```bash
export MCP_REGISTRY_INGRESS_HOST=registry.example.com
./bin/mcp-runtime server generate --metadata-dir .mcp --output manifests
```

The platform API follows the same split for short image deploys: it expands
short image names with `PLATFORM_REGISTRY_URL`, `MCP_REGISTRY_INGRESS_HOST`, or
`MCP_PLATFORM_DOMAIN` before falling back to `MCP_REGISTRY_ENDPOINT`. This keeps
tenant `MCPServer.spec.image` values on the pullable public registry host when
an internal endpoint also exists for push helpers or registry mirrors.

Use `MCP_REGISTRY_ENDPOINT` only when the operator needs a different internal
push/pull endpoint than the public image host. For example, a private registry
may expose `registry.example.com` to developers but an internal `host:port` to
cluster nodes. Keep image references, pull secrets, and node trust aligned with
the endpoint that kubelet actually uses.

External registry readiness checks:

- `docker login registry.example.com` succeeds from the build machine, if
  username/password auth is required.
- `kubectl get secret -A | grep -i pull` shows the expected image pull secret,
  or workload identity / node registry auth is configured by the platform.
- A test pod using an image from the registry reaches `Running` on every node
  pool that may run MCP Runtime workloads.
- `./bin/mcp-runtime status` reports the provisioned registry URL after setup.

## Strict production setup

`./bin/mcp-runtime setup --strict-prod` adds guardrails for production-style
installs. It is ignored in `--test-mode`; otherwise it requires:

- `--with-tls`.
- `--registry-mode external` with an external registry URL that is not dev-only,
  or `--registry-mode bundled-https` for a bundled registry served over HTTPS.
- No implicit bundled HTTP registry path.

Without `--strict-prod`, setup allows local HTTP and internal registry flows
for kind, k3s, Docker Desktop, and CI. Use `--strict-prod` for production and
production-like staging clusters.

## DNS and TLS readiness

When `MCP_PLATFORM_DOMAIN=example.com` is set, setup derives these public names:

- `registry.example.com` for registry ingress.
- `mcp.example.com` for MCP server traffic.
- `platform.example.com` for the dashboard, API, and admin-gated Grafana paths.
  Prometheus remains an internal metrics backend; tenant users reach it only
  through server-scoped API queries.

All configured public names must resolve to the cluster ingress address before
certificate issuance. For Let's Encrypt HTTP-01, port 80 must reach the ingress
controller from the public internet. For enterprise PKI, install the
`ClusterIssuer` first and pass it with:

```bash
./bin/mcp-runtime setup --with-tls --tls-cluster-issuer <issuer-name>
```

If you retain the bundled registry with TLS ingress only, make sure the registry
certificate covers the registry and MCP hostnames nodes, build machines, and
MCP clients use. The `registry/registry-cert` Certificate writes the
`registry/registry-tls` Secret and covers `registry.<domain>` plus
`mcp.<domain>` when those names are derived from `MCP_PLATFORM_DOMAIN` or
explicit ingress host environment
variables.

The registry Ingress has no `cert-manager.io/cluster-issuer` annotation.
Registry TLS is owned only by the explicit `registry-cert` Certificate, which
keeps cert-manager ingress-shim from creating a second `registry-tls`
Certificate for the same Secret. If setup reports that
another Certificate already references `registry-tls`, remove the stale owner
before applying TLS again.

The platform UI hostname is separate. `platform.<domain>` is owned by the
`mcp-platform-ui` Ingress in the `mcp-platform` namespace, and
cert-manager writes that certificate into the `mcp-platform-tls`
Secret in the same namespace. `registry-cert` does not contain
`platform.<domain>`.

Inspect both TLS paths when debugging:

```bash
kubectl get certificate registry-cert -n registry -o yaml
kubectl get secret registry-tls -n registry \
  -o jsonpath='{.data.tls\.crt}' | base64 -d | \
  openssl x509 -noout -text | grep -A1 "Subject Alternative Name"

kubectl get ingress mcp-platform-ui -n mcp-platform -o yaml
kubectl get secret mcp-platform-tls -n mcp-platform \
  -o jsonpath='{.data.tls\.crt}' | base64 -d | \
  openssl x509 -noout -text | grep -A1 "Subject Alternative Name"
```

With an external registry, you manage the registry's TLS and auth configuration
outside the bundled cert-manager flow.

The bundled registry ingress expects the repo-managed Traefik dynamic
middleware `registry-admin-auth@file`. If you bring your own ingress controller
or reuse an external Traefik install, configure an equivalent forward-auth guard
to `/api/v1/registry/authz` before exposing `registry.<domain>` publicly.

If the cluster already has a live external Traefik install, `setup` reuses it
and refuses to install a second repo-managed Traefik stack. In that shape, use:

```bash
./bin/mcp-runtime setup --ingress none ...
```

Do that only when the external ingress controller already owns the ingress
class, public entrypoints, and any required registry auth middleware. Running
two Traefik installations against the same ingress surface leads to ambiguous
ownership and hard-to-debug routing failures.

Quick public endpoint checks after DNS and TLS are live:

- `curl -k -I -H "x-api-key: $ADMIN_API_KEY" https://registry.<domain>/v2/`
  should return `200` and `docker-distribution-api-version: registry/2.0`.
  Without admin credentials, the public registry ingress should return `401`
  or `403`.
- `curl -k -I https://platform.<domain>/` should return `200`.
- `curl -k -i https://platform.<domain>/api/v1/health` should normally return
  `401` without platform credentials, which confirms the platform host routes
  API traffic.
- `curl -k -i -H "x-api-key: $ADMIN_API_KEY" https://platform.<domain>/grafana/api/v1/health`
  should reach the admin-gated observability route. Without admin credentials,
  the `sentinel-admin-auth@file` guard should return `401`. Prometheus is not
  exposed directly on the platform host; validate it through Grafana's
  datasource or a temporary `kubectl port-forward`.
- `curl -k -i https://mcp.<domain>/<server-name>/mcp` may return an
  application-level `400` or `401` when called without the expected MCP
  protocol headers or session context. That response confirms the public route
  is live before you debug the MCP client.

## Public-mode admin bootstrap

Fresh public/TLS installs must set an OIDC admin allowlist before setup:

- `MCP_PLATFORM_ADMIN_EMAIL`, for the first platform admin email
- or `ADMIN_USERS`, for a comma-separated list of admin emails or OIDC subjects

Setup writes those values into the `ADMIN_USERS` secret key. The API checks that
allowlist on Google/OIDC login and promotes matching users to platform admin.
`--acme-email` is only the certificate contact email and does not grant
platform admin.

Password bootstrap is separate. Installs that expect a password-created admin
must keep both of these secret-backed values aligned:

- `PLATFORM_ADMIN_EMAIL`
- `PLATFORM_ADMIN_PASSWORD`

If only one of the two is present in the API deployment environment, the API
can crash-loop on a clean database with an error similar to:

```text
failed to seed platform admin: PLATFORM_ADMIN_EMAIL and PLATFORM_ADMIN_PASSWORD must both be set
```

Inspect the deployment and the managed secret before you debug the database or
auth path:

```bash
kubectl get deploy mcp-platform-api mcp-runtime-api -n mcp-platform -o yaml
kubectl get deploy mcp-analytics-api -n mcp-observability -o yaml
kubectl get secret mcp-platform-api-credentials -n mcp-platform -o yaml
```

Both values must be present for the initial seed to succeed. Remove or rotate
bootstrap-only credentials after the first successful bring-up.

## Clean platform reset

Sentinel state lives mainly in PVC-backed StatefulSets, so deleting pods does
not reset the platform. For a fresh platform state, scale the StatefulSets down
and delete the PVCs you intend to wipe.

Typical destructive reset flow:

```bash
kubectl scale statefulset clickhouse kafka loki tempo \
  -n mcp-observability --replicas=0
kubectl scale statefulset mcp-postgres \
  -n mcp-platform --replicas=0

kubectl delete pvc \
  data-clickhouse-0 \
  kafka-data-kafka-0 \
  kafka-data-kafka-1 \
  kafka-data-kafka-2 \
  data-loki-0 \
  data-tempo-0 \
  -n mcp-observability
kubectl delete pvc data-mcp-postgres-0 -n mcp-platform
```

Important distinctions:

- Preserve the owner credential Secrets in `mcp-platform` and `mcp-observability` unless you intentionally want new API keys,
  JWT secrets, and platform bootstrap values.
- Preserve `mcp-platform-tls` unless you want to reissue the platform
  certificate.
- Preserve `registry/registry-storage` unless you intentionally want to wipe the
  image registry too.
- Kafka KRaft metadata, topic data, and consumer offsets all live in the three
  `kafka-data-kafka-*` PVCs. Delete all three together for a clean reset.
- A PVC stuck in `Terminating` is often still referenced by stale pods from an
  old ReplicaSet. Remove the stale pods first, then recreate the workload.

This reset is destructive. Use it as a deliberate operator workflow; do not
use it as an upgrade path.

## Node pool consistency

Registry fixes must apply to every node pool that can schedule MCP Runtime
pods. A setup can look healthy on one node and fail later when a workload lands
on a different pool with missing DNS, missing CA trust, missing registry mirror
config, or missing cloud registry permissions.

For production, either standardize registry trust and auth across all eligible
node pools, or use taints, tolerations, labels, and node selectors so MCP
Runtime workloads only land on pools that have been prepared and audited.

## k3s

k3s uses embedded containerd. The steps below cover **lab HTTP** registry
mirrors (`registry.local`, NodePort). For **public TLS + bundled HTTPS**
(`registry.<domain>` with Let's Encrypt), skip the insecure mirror path and
follow [Deployment Targets - k3s production](deployment-targets.md#option-a-bundled-https-registry-on-prem-reference)
and [Public Reference Deployment](reference-deployment.md). Set
`MCP_REGISTRY_ENDPOINT=registry.<domain>` and use `--ingress none` when k3s
Traefik already runs in `kube-system`.

For `setup --test-mode` with the bundled plain HTTP registry, the same
containerd mirror requirement applies because setup still builds and pushes
operator, gateway proxy, and Sentinel images, then deploys pods that pull those
images. On k3s hosts where `~/.kube/config` is empty or minimal, pass
`--kubeconfig /etc/rancher/k3s/k3s.yaml` to setup.

**Complete the steps below before running setup** so k3s trusts the registry
host on the first attempt. Otherwise setup fails once: it prints the registry **Internal URL**
after the Service exists; copy that `host:port` into `registries.yaml`, restart
k3s, then rerun setup. To avoid the rerun on dynamic ClusterIP environments,
configure a stable external registry or set `MCP_REGISTRY_ENDPOINT` to a
`host:port` every node already trusts.

1. **Registry mirror.** Create `/etc/rancher/k3s/registries.yaml`:

   ```yaml
   mirrors:
     registry.local:
       endpoint:
         - "http://127.0.0.1:32000"
   configs:
     "127.0.0.1:32000":
       tls:
         insecure_skip_verify: true
   ```

   If the registry's ClusterIP (e.g. `10.43.39.164:5000`) or service DNS
   (`registry.registry.svc.cluster.local:5000`) ever appears as an image ref,
   add a mirror entry for it too. Containerd does exact-host matching.

2. **Host DNS.** Add to `/etc/hosts`:

   ```text
   127.0.0.1 registry.local
   ```

3. **Reload.** `systemctl restart k3s`. k3s regenerates containerd's config from `registries.yaml` at startup.

Multi-node k3s: apply the same `/etc/rancher/k3s/registries.yaml` and `/etc/hosts` to every node. `127.0.0.1:32000` reaches the local kube-proxy, which forwards to the registry pod on whichever node it runs.

## Node disk-pressure recovery

Image-heavy setup runs on small clusters can trip kubelet `DiskPressure` during
the operator/Sentinel build-and-push phase. Typical symptoms:

- helper pods such as `registry-pusher-*` stay `Pending`
- new Sentinel pods fail scheduling with `untolerated taint(s)`
- `kubectl describe node` shows `node.kubernetes.io/disk-pressure:NoSchedule`

Check the node first:

```bash
kubectl describe node <node-name>
df -h /
```

Then clear disposable build/cache data before rerunning setup:

- stale Go build/module caches under `/tmp` and `/root/.cache/go-build`
- unused Docker images from local build stages

If free space returns but the node still reports `DiskPressure`, restart the
node's Kubernetes runtime service so kubelet/containerd accounting catches up.
The exact service name depends on the distribution, for example:

```bash
systemctl restart k3s
```

After the restart, verify `DiskPressure=False` before continuing the rollout.
If the registry pod was unavailable during the pressure window, expect a brief
wave of `ErrImagePull` or `ImagePullBackOff` until the registry becomes ready
again.

## kind

kind's nodes are containers, so the registry NodePort needs an `extraPortMappings` entry to be reachable, and containerd inside the node container needs the same mirror.
For `setup --test-mode`, MCP Runtime emits image refs such as
`registry.registry.svc.cluster.local:5000/mcp-platform-api:latest` (and sibling
split API images) so Kind nodes use one stable service-DNS host. A registry
`ClusterIP:port` can change.

1. **Cluster config.** Pass this to `kind create cluster --config`:

   ```yaml
   kind: Cluster
   apiVersion: kind.x-k8s.io/v1alpha4
   containerdConfigPatches:
     - |-
       [plugins."io.containerd.grpc.v1.cri".registry.mirrors."registry.registry.svc.cluster.local:5000"]
         endpoint = ["http://127.0.0.1:32000"]
       [plugins."io.containerd.grpc.v1.cri".registry.configs."127.0.0.1:32000".tls]
         insecure_skip_verify = true
   nodes:
     - role: control-plane
       extraPortMappings:
         - containerPort: 32000
           hostPort: 32000
           protocol: TCP
   ```

2. **Exact host matching.** If pod events show `http: server gave HTTP
   response to HTTPS client`, compare the pod image host with the mirror key.
   Containerd only applies the HTTP mirror when the strings match exactly.

Alternative: `kind load docker-image <image>` sideloads images without a registry. Use it for throwaway tests; it bypasses the CLI's registry-push flow.

## minikube

Two options.

**Option A: insecure registry flag at start time**

```bash
minikube start --insecure-registry=registry.local --insecure-registry=10.43.39.164:5000
minikube addons enable ingress
echo "$(minikube ip) registry.local" | sudo tee -a /etc/hosts
```

minikube reads the `--insecure-registry` flag only on the initial `start`. Re-create the VM to change it.

**Option B: `minikube image load`**

Load images directly into the node's image store, with no registry:

```bash
docker build -t registry.local/my-server:latest .
minikube image load registry.local/my-server:latest
```

Use this for quick iteration. `./bin/mcp-runtime server push` does not apply, because images bypass the registry.

## Docker Desktop (Kubernetes)

1. Docker Desktop → Settings → Docker Engine → add:

   ```json
   {
     "insecure-registries": ["registry.local", "10.96.0.0/12"]
   }
   ```

   Apply & Restart. Docker Desktop's embedded k8s shares the Docker daemon's registry config.

2. `/etc/hosts`:

   ```text
   127.0.0.1 registry.local
   ```

The k8s nodes (VMs managed by Docker Desktop) reach the NodePort through the shared host loopback at `127.0.0.1`.

## kubeadm / vanilla Kubernetes

For each node running kubelet:

1. Edit `/etc/containerd/config.toml`:

   ```toml
   [plugins."io.containerd.grpc.v1.cri".registry.mirrors."registry.local"]
     endpoint = ["http://<registry-reachable-ip>:32000"]
   [plugins."io.containerd.grpc.v1.cri".registry.configs."<registry-reachable-ip>:32000".tls]
     insecure_skip_verify = true
   ```

   Set `<registry-reachable-ip>` to an IP where the node can reach the registry's NodePort, typically the node's own IP or a load-balancer VIP.

2. `/etc/hosts` on each node: map `registry.local` to the same IP.

3. `systemctl restart containerd` on each node.

For production, use a stable registry endpoint with HTTPS and trusted node
configuration as described in [Dev vs production readiness](#dev-vs-production-readiness).

## Generic checks you can run

```bash
# Is the registry Service up?
kubectl get svc -n registry registry

# NodePort?
kubectl get svc -n registry registry -o jsonpath='{.spec.ports[0].nodePort}'

# From inside the cluster, this should return a JSON repository list:
kubectl run -n registry --rm -it registry-check --restart=Never \
  --image=curlimages/curl --command -- \
  curl -s http://registry.registry.svc.cluster.local:5000/v2/_catalog

# From the node (SSH to a node first; this bypasses public ingress auth):
curl -s http://127.0.0.1:32000/v2/_catalog
getent hosts registry.local

# Post-install diagnostics via the CLI (see below)
./bin/mcp-runtime cluster doctor
```

## Failure-to-cause map

| Symptom | Likely cause | What to check |
|---|---|---|
| `lookup registry.local: no such host` during publish or image pull | Host or node DNS does not know `registry.local` | `/etc/hosts`, corporate DNS, `MCP_REGISTRY_INGRESS_HOST`, and the image host emitted by generated manifests |
| `http: server gave HTTP response to HTTPS client` | The registry is HTTP, but Docker/containerd is trying HTTPS | Insecure registry / mirror settings for the exact image host in dev, or switch to HTTPS with trusted certs for production |
| `ImagePullBackOff` with `401` or `403` | Registry auth is missing or invalid | Image pull secrets, service account references, workload identity, or cloud node registry permissions |
| `ImagePullBackOff` only on some nodes | Node pool config drift | Registry mirror config, CA trust, DNS, and registry permissions on every eligible node pool |
| `curl https://registry.<domain>/v2/` returns `401` or `403` | Public registry ingress is routed but no admin credential was accepted | Retry with an admin `x-api-key`, admin Bearer token, or admin-owned registry Basic credential |
| `curl https://registry.<domain>/v2/` returns Traefik `404 page not found` | Ingress/router is not routing to the registry service | Registry `Ingress`, ingress class, Traefik logs, host rules, TLS secret names, and the `registry-admin-auth@file` middleware |
| cert-manager reports `NXDOMAIN` | Public DNS is missing or misspelled | `getent hosts registry.<domain>`, `mcp.<domain>`, `platform.<domain>`, and DNS records from outside the cluster |
| `cluster doctor` reports missing bundled registry while using an external registry | Doctor is checking the local bundled-registry path | Validate your external registry path manually and treat that specific check as advisory |

## `bootstrap`

`./bin/mcp-runtime bootstrap` validates a smaller, mostly orthogonal set of prerequisites before `setup`:

- kubectl connectivity to the cluster.
- A CoreDNS deployment in `kube-system`.
- A default `StorageClass` (one annotated `storageclass.kubernetes.io/is-default-class=true`).
- The `traefik` `IngressClass`.
- The `metallb-system` namespace, if you plan to use MetalLB for `LoadBalancer` services.

Missing pieces are reported as warnings. Install what you need with your standard platform tooling.

`bootstrap --apply --provider k3s` is the only automated apply path today: run it on the k3s server node and it applies the bundled CoreDNS and local-path manifests under `/var/lib/rancher/k3s/server/manifests`, then waits for both rollouts. Other providers (`rke2`, `kubeadm`, `generic`) print guidance instead.

## `cluster doctor` and `cluster diagnostics`

`./bin/mcp-runtime cluster doctor` is the pre-setup readiness check. Run it
before `setup`. It checks:

- Kubernetes API connectivity, Ready nodes, kubelet-reported container runtimes, and node pressure conditions.
- Node architecture, StorageClasses, Pending PVCs, and RuntimeClass references.
- Traefik ingress readiness and exposure.
- Public host resolution from `MCP_PLATFORM_DOMAIN` or the explicit `MCP_PLATFORM_INGRESS_HOST`, `MCP_REGISTRY_INGRESS_HOST`, and `MCP_MCP_INGRESS_HOST` env vars.
- Local DNS resolution for configured public hosts.
- cert-manager deployment readiness, the configured `MCP_TLS_CLUSTER_ISSUER`, and ACME HTTP-01 prerequisites.

`./bin/mcp-runtime cluster diagnostics` runs post-install diagnostics:

- Detects your distribution (k3s / kind / minikube / docker-desktop / generic).
- Checks the installed MCP Runtime namespaces, CRDs, operator, Traefik ingress, registry, Sentinel, and MCPServer reconciliation path. The MCPServer smoke uses an existing ready app image when available; otherwise it falls back to `registry.k8s.io/pause:3.9` and validates deployment/service/ingress reconciliation plus pod scheduling without a TCP readiness wait.
- Prefers k3s' bundled Traefik in `kube-system/traefik` when the active cluster is k3s, then falls back to the repo-managed `traefik/traefik` install.
- `setup` follows the same ownership model: it reuses active external Traefik and refuses to force-install the repo-managed Traefik when that would create a second active stack.
- Verifies registry reachability, registry image-pull smoke behavior, and common pod image-pull failures. The bundled registry reachability probe uses HTTPS when `registry/registry-internal-tls` is installed, and HTTP otherwise.
- Reports `http: server gave HTTP response to HTTPS client` when kubelet/containerd tried HTTPS against the HTTP dev registry, including the affected pod and image where possible.
- Streams the current check before running it, including helper pod probes and waits, so a slow run shows what it is doing.
- Prints the distribution-specific registry remediation hint only when registry or image-pull checks fail; Traefik and Sentinel failures use their own check-specific remedies.

Diagnostics discover cluster-specific values where Kubernetes exposes them.
For non-default values, set these overrides:

```bash
export MCP_CLUSTER_DOMAIN=cluster.local          # custom Service DNS suffix
export MCP_DEFAULT_INGRESS_CLASS=traefik         # active IngressClass
export MCP_DEFAULT_INGRESS_ENTRYPOINTS=web       # smoke route entrypoint(s)
export MCP_DEFAULT_SERVICE_PORT=8088              # smoke service port, if non-default
```

The doctor reads node/runtime, DNS, ingress, registry, storage, architecture,
and workload state from the active cluster. It does not assume k3s' API port, a
fixed CoreDNS label, or a particular registry NodePort. The environment values
above override only information Kubernetes cannot reliably infer from an
MCPServer smoke object.

Run `bootstrap` and then `cluster doctor` before `setup` on a fresh cluster.
Run `cluster diagnostics` after setup or after any platform change.
