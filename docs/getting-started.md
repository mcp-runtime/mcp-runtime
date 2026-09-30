# Getting Started

Install MCP Runtime on your own Kubernetes cluster. To try the platform without
a cluster, use the [Quickstart](quickstart.md).

## Prerequisites

- Go `1.26+` (matches the repository `go.mod` files)
- `make`
- Docker or a Docker-compatible client, with the daemon running and reachable
- `kubectl` on `PATH`, configured for the target cluster
- `curl`, `jq`, and `python3` for documented dev and traffic-generation flows
- `kind` for the contributor test-mode cluster in step 3
- A Kubernetes cluster (k3s, kind, minikube, Docker Desktop Kubernetes, EKS, GKE, AKS, or equivalent). If you are choosing a target, start with [Deployment Targets](deployment-targets.md), then use [Cluster Readiness](cluster-readiness.md) for distribution-specific prep.

Host bootstrap:

```bash
make deps-install              # best-effort install for supported macOS/Linux hosts
STRICT_DEPS_CHECK=1 make deps-check
```

`make deps-install` is intentionally best-effort: it can install some packages with Homebrew or apt, but it cannot enable Docker Desktop, create cloud credentials, or configure your kubeconfig. Re-run `STRICT_DEPS_CHECK=1 make deps-check` until the required host tools pass.

## 1. Install the CLI

**Option A: install a release binary** (no Go required). Select your operating
system and copy the install command:

=== "macOS"

    ```bash
    curl -fsSL https://raw.githubusercontent.com/mcp-runtime/mcp-runtime/main/install.sh | MCP_RUNTIME_OS=darwin sh
    ```

=== "Linux"

    ```bash
    curl -fsSL https://raw.githubusercontent.com/mcp-runtime/mcp-runtime/main/install.sh | MCP_RUNTIME_OS=linux sh
    ```

=== "Windows (amd64)"

    ```powershell
    irm https://raw.githubusercontent.com/mcp-runtime/mcp-runtime/main/install.ps1 | iex
    ```

The macOS/Linux installer detects the CPU and installs to `~/.local/bin`
without `sudo`. Windows installs to a user-local directory and adds it to your
user `PATH`; open a new terminal after installation. Pin a
specific release by setting `MCP_RUNTIME_VERSION` to its tag before running the
installer. For example, run `export MCP_RUNTIME_VERSION=vX.Y.Z` on macOS/Linux
or `$env:MCP_RUNTIME_VERSION='vX.Y.Z'` in PowerShell. Browse all binaries on the
[latest GitHub release](https://github.com/mcp-runtime/mcp-runtime/releases/latest).
If macOS or Linux cannot find `mcp-runtime`, add `~/.local/bin` to your shell's
`PATH`, for example with `export PATH="$HOME/.local/bin:$PATH"`.

**Option B: build from source** (requires Go 1.26+):

```bash
make deps
make build
```

This produces `./bin/mcp-runtime` with version metadata from
`git describe --tags --match 'v*'`, the current commit, and UTC build time.
Release binaries use the release tag exactly. Override with
`VERSION=<tag> make build` when needed.

## 2. Confirm cluster readiness

```bash
./bin/mcp-runtime bootstrap
```

Before setup, confirm the target Kubernetes cluster is ready for registry
pushes, image pulls, ingress, storage, and TLS. See
[Deployment Targets](deployment-targets.md) to choose the right install shape
for self-managed or managed Kubernetes, then
[cluster-readiness.md](cluster-readiness.md) for distribution-specific
preparation.

`setup` installs MCP Runtime resources into an already-running cluster. It does
not configure node DNS, containerd or Docker registry trust, public DNS, TLS
issuers, image pull credentials, or storage classes. Fix those prerequisites
with your platform tooling before continuing.

`bootstrap` validates kubectl connectivity, CoreDNS, the default
`StorageClass`, Traefik `IngressClass`, and MetalLB namespace. It only warns;
fix gaps with your platform tooling, or `bootstrap --apply --provider k3s` to
install bundled CoreDNS / local-path on k3s. After setup, run `cluster diagnostics`
to validate the installed MCP Runtime resources, registry pulls, ingress,
Sentinel, and operator readiness.

## 3. Contributor test-mode cluster (local Kind)

For local development, CI, or contributing to the repo, use the Kind-based
test-mode path. The contributor docs own this path completely:

- [Contributor Guide](contributor/README.md)
- [Local Kind and Test Mode](contributor/local-kind.md)

Quick path (requires `kind` on `PATH`; the linked guide creates an isolated
test kubeconfig so this does not use the ambient or production context):

```bash
make deps && make build
# Follow Local Kind and Test Mode to create/export the test kubeconfig first.
export KUBECONFIG="$HOME/.kube/test-mcp-runtime-config"
./bin/mcp-runtime bootstrap
./bin/mcp-runtime cluster doctor
./bin/mcp-runtime setup --test-mode --ingress-manifest config/ingress/overlays/http
kubectl port-forward -n traefik svc/traefik 18080:8000 18443:8443
./bin/mcp-runtime cluster diagnostics
```

See [Local Kind and Test Mode](contributor/local-kind.md) for fresh-device and
existing-cluster instructions, including the isolated kubeconfig setup.

`bootstrap` and `cluster doctor` run before setup: `bootstrap` reports (and on
k3s can install) missing cluster prerequisites, `cluster doctor` checks nodes,
storage, ingress, DNS, and TLS readiness. `cluster diagnostics` runs after setup
to validate what was installed.

Local surfaces: platform `http://localhost:18080/`, plain Ingress MCP
`http://localhost:18080/<server-name>/mcp`, adapter-certificate MCP
`https://localhost:18443/<server-name>/mcp` (use `-k` for the local default
cert when `MCP_ADAPTER_CERTIFICATES=true`).

## 4. Production-style install

Use this path for any cluster you intend to keep.
That includes staging, internal shared clusters, externally reachable installs,
or anything that needs stable registry, DNS, TLS, storage, and ingress
ownership.

Before `setup`, make these decisions explicitly:

- Registry: bundled registry with TLS, or a provisioned external registry
- DNS: stable hostnames for `registry`, `mcp`, and `platform`
- TLS: Let's Encrypt, enterprise `ClusterIssuer`, or preinstalled cert flow
- Ingress: repo-managed Traefik or an existing platform ingress controller
- Storage and retention: registry and Sentinel persistence choices
- Image pull auth: pull secrets, workload identity, or node-native registry auth

Read these first:

- [Deployment Targets](deployment-targets.md)
- [Cluster readiness](cluster-readiness.md)
- [Sentinel Kubernetes awareness and hardening](sentinel.md#kubernetes-awareness-and-hardening)
- [Multi-team isolation](multi-team.md) if multiple teams will publish or govern servers on one cluster

For production-oriented setup, choose the registry path explicitly. With a
provisioned registry:

```bash
./bin/mcp-runtime bootstrap
./bin/mcp-runtime setup --registry-mode external --external-registry-url registry.example.com --with-tls --strict-prod
```

With the bundled registry serving internal HTTPS, setup generates an internal
CA secret for the registry pod certificate unless you provide an existing
ClusterIssuer. Configure every node to trust that CA for image pulls. Public
ingress TLS can still use ACME:

```bash
./bin/mcp-runtime bootstrap
./bin/mcp-runtime setup --registry-mode bundled-https --with-tls --acme-email ops@example.com --strict-prod
```

If you want hostnames derived from one domain, set:

```bash
export MCP_PLATFORM_DOMAIN=example.com
export MCP_PLATFORM_ADMIN_EMAIL=admin@example.com
./bin/mcp-runtime setup --registry-mode external --external-registry-url registry.example.com --with-tls --strict-prod
```

That derives:

- `registry.example.com`
- `mcp.example.com`
- `platform.example.com`

If you already have an external registry, provision it before setup so the
cluster pulls from the same hardened image host you intend to keep:

```bash
./bin/mcp-runtime registry provision --url registry.example.com
./bin/mcp-runtime setup --registry-mode external --with-tls --strict-prod
```

For public/TLS setup, setup validates the host env even without
`--strict-prod`. Use `MCP_PLATFORM_DOMAIN` or set
`MCP_PLATFORM_INGRESS_HOST`, `MCP_REGISTRY_INGRESS_HOST`, and
`MCP_MCP_INGRESS_HOST` explicitly. For bundled HTTPS with a public domain,
set `MCP_REGISTRY_ENDPOINT=registry.<domain>` so kubelet pulls match the TLS
certificate (not the registry ClusterIP). Set `MCP_PLATFORM_ADMIN_EMAIL` or
`ADMIN_USERS` so the first OIDC login for that email is promoted to platform
admin; `--acme-email` is only the certificate contact email.

When building setup images from a machine with a different CPU architecture
than the cluster, set `MCP_IMAGE_PLATFORM` to the target node platform, for
example `MCP_IMAGE_PLATFORM=linux/amd64` for standard VPS/k3s nodes.

Tenant users open server-scoped Prometheus and Grafana views from Activity
server rows. The platform API verifies access to the exact `MCPServer` and
expands only allowlisted Prometheus queries; raw Prometheus remains internal.
The bundled Prometheus discovers operator-managed MCPServer Services and
scrapes metrics from their gateway sidecars.

You can also skip the saved provision step and pass
`--external-registry-url registry.example.com` directly to `setup`.

If you use an internal CA instead of ACME, install the issuer first and point
setup at it:

```bash
./bin/mcp-runtime setup --with-tls --tls-cluster-issuer <issuer-name> --strict-prod
```

What `--strict-prod` is for:

- requires TLS
- rejects dev-only registry assumptions such as `registry.local`
- forces you onto a stable production-style registry endpoint

Do not use the contributor `--test-mode` flow as a production install guide.
`--test-mode` is for local development and CI-like validation; it provisions a
local cert-manager workload CA for mTLS tests and still builds
and pushes local images and assumes the contributor registry and ingress shape.

## 5. Install the platform stack

```bash
./bin/mcp-runtime setup
```

`setup` installs the platform pieces companies need for MCP operations: CRDs,
`mcp-runtime` and catalog namespaces, the internal Docker registry, ingress
wiring, the operator, and the bundled Sentinel stack for gateway policy,
analytics, audit, and observability.

`--platform-mode` selects the namespace model:

| Mode | Default namespace behavior | Behavior |
|---|---|---|
| `tenant` | Principal team namespace | Default private mode. Signed-in users publish through team namespaces for teams they belong to. |
| `org` | `mcp-servers-org` | Signed-in users publish and browse the org-wide catalog and can still work in team namespaces. |
| `public` | `mcp-servers-public` | Anonymous users can browse the public preview catalog; signed-in users publish public preview MCP servers and can still work in team namespaces. |

For browser Google sign-in, provide the OAuth client ID before setup. Non-test
public TLS installs (`--platform-mode public --with-tls`) fail fast unless
`GOOGLE_CLIENT_ID` / `MCP_GOOGLE_CLIENT_ID` is set, or `OIDC_ISSUER`,
`OIDC_AUDIENCE`, and `OIDC_JWKS_URL` are all set for another provider. For
Google, setup uses the client ID as the OIDC audience and fills the standard
Google issuer and JWKS URL when those values are not set explicitly:

```bash
export GOOGLE_CLIENT_ID=<client>.apps.googleusercontent.com
export MCP_PLATFORM_ADMIN_EMAIL=admin@example.com
./bin/mcp-runtime setup --with-tls --platform-mode public
```

For a non-Google OIDC provider, set `OIDC_ISSUER`, `OIDC_AUDIENCE`, and
`OIDC_JWKS_URL` before setup. Reruns preserve existing values in
`mcp-sentinel/mcp-sentinel-config`.

For multi-team or tenant-separated deployments, keep setup as the platform
install and provision one namespace per team with `mcp-runtime team create
<slug>` (platform API). Use the platform API to default team IDs, or set
`spec.teamID` and `subject.teamID` directly in YAML; an explicit foreign
`subject.teamID` delegates access to another team while the gateway still
matches every non-empty subject field. See
[Multi-team isolation](multi-team.md).

Common variants:

```bash
./bin/mcp-runtime setup --with-tls            # cert-manager TLS for the registry
./bin/mcp-runtime setup --platform-mode public # public preview catalog namespace
./bin/mcp-runtime setup --without-sentinel    # skip the request-path stack
./bin/mcp-runtime setup --test-mode           # local Kind/dev build+push path
./bin/mcp-runtime setup --storage-mode hostpath # single-node cluster with no dynamic provisioner
./bin/mcp-runtime setup --parallel-builds     # build and publish setup images in parallel
```

Defaults if you pass nothing: `--ingress traefik`, `--ingress-manifest
config/ingress/overlays/http`, `--registry-mode auto`, `--registry-type docker`,
`--registry-storage 20Gi`, `--platform-mode tenant`, and `--storage-mode
dynamic`. `--parallel-builds` changes image build and publish only; cluster,
registry, TLS, and rollout sequencing stay the same.

To enable optional adapter client certificates on gateway server routes, add
`--mtls-cluster-issuer <cluster-issuer>` alongside `--with-tls`. Name an
enterprise cert-manager issuer, or the bundled `mcp-runtime-ca` to have setup
provision one; `--test-mode` defaults to `mcp-runtime-ca`. Set
`MCP_ADAPTER_CERTIFICATES=true` to turn the feature on; production must also
set `MCP_TRUST_DOMAIN` (for example `mcpruntime.org`). OAuth remains optional:
omit `spec.auth` for cert-only routes, or add `spec.auth` when direct clients
need a bearer. See
[Agent Adapters](agent-adapters.md#enterprise-mtls-and-spiffe).

The bundled mcp-auth authorization server is optional and off by default. Check
the provider with `./bin/mcp-runtime auth provider-check --issuer-url <issuer>`,
then enable it with `--with-mcp-auth-server`; outside test mode it also needs
`--mcp-auth-signing-key-secret`. The issuer defaults to
`https://auth.<MCP_PLATFORM_DOMAIN>/mcp-auth`, and the operator reconciles
resource audiences from OAuth MCPServers. With managed TLS, setup provisions the auth
issuer certificate automatically; `--mcp-auth-tls-secret` is only an optional
override for externally managed certificates. See
[MCP authorization](mcp-authorization.md).

Every setup flag and its default is listed in the
[CLI reference](cli.md#setup).

### Local development notes

For Kind or other local setups where traffic reaches Traefik through `kubectl port-forward` or a NodePort but the ingress controller does not publish `Ingress.status.loadBalancer.ingress[]`, run setup with permissive ingress readiness:

```bash
export MCP_INGRESS_READINESS_MODE=permissive
./bin/mcp-runtime setup --test-mode --ingress-manifest config/ingress/overlays/http
kubectl port-forward -n traefik svc/traefik 18080:8000 18443:8443
```

Then use `http://127.0.0.1:18080/<publicPathPrefix>/mcp` for plain Ingress MCP
traffic. When `MCP_ADAPTER_CERTIFICATES=true`, gateway routes use Traefik
IngressRoute on `websecure` only — probe them at
`https://127.0.0.1:18443/<publicPathPrefix>/mcp` (local Kind typically needs
`-k` / insecure TLS skip for Traefik's default certificate).

!!! warning "`MCPServer` stuck in `PartiallyReady` while traffic works"
    Strict readiness (the default) waits for the Ingress to publish
    `status.loadBalancer.ingress[]`. Many dev and NodePort-style controllers route
    traffic without ever publishing it. `permissive` treats an Ingress with rules as
    ready. Keep `strict` for production clusters that rely on published
    load-balancer status.

## 6. Confirm health

```bash
./bin/mcp-runtime status
./bin/mcp-runtime cluster status
./bin/mcp-runtime registry status
./bin/mcp-runtime sentinel status
```

## 7. Deploy your first server

The server deploy flow (init → validate → build → push → deploy → grant → adapter)
is covered step-by-step in the learning modules:

- [Module 2: Your first governed server](learn/module-2-first-server.md): end-to-end hands-on
- [Module 3: Multi-team setup](learn/module-3-multi-team.md): two teams, cross-team grants

Quick reference:

```bash
mcp-runtime server init my-server --from-server http://localhost:8088
mcp-runtime server validate --metadata-dir .mcp
mcp-runtime server build image my-server --tag v1
mcp-runtime server push --image ... --scope tenant
mcp-runtime server deploy my-server --scope tenant --metadata-dir .mcp
```

## 8. Observe live traffic and policy

Use the platform dashboard and API first:

```bash
./bin/mcp-runtime auth login --api-url <platform-url>
./bin/mcp-runtime status
# Dashboard: http://localhost:18080/ (Kind) or https://platform.<domain>/
```

Admin/operator kubectl diagnostics (`sentinel *` requires admin cluster access):

```bash
./bin/mcp-runtime sentinel port-forward ui          # Governance + dashboard
./bin/mcp-runtime sentinel port-forward grafana     # Metrics + traces + logs
./bin/mcp-runtime sentinel logs gateway --follow    # Tail the proxy
```

## End-to-end flow

```mermaid
flowchart LR
    A[Build CLI<br/>make build] --> B[bootstrap + setup]
    B --> C[auth login]
    C --> D[server init<br/>build, push, deploy]
    D --> E[grant init + apply]
    E --> F[adapter or admin session]
    F --> G[Traffic through gateway]
    G --> H[Observe in UI + Grafana]
```

## Next steps

- [Publish an MCP Server](publish-mcp-server.md): write manifests or `.mcp` metadata, build, push, deploy, and verify.
- [Multi-team isolation](multi-team.md): team IDs, namespaces, RBAC, and ingress guidance.
- [Architecture](architecture.md): how the pieces fit together.
- [CLI](cli.md): full command reference.
- [API](api.md): every CRD field and HTTP endpoint.
- [Sentinel](sentinel.md): request-path governance, audit, observability.

**Next:** [Concepts](concepts.md): understand Grants, Sessions, Trust levels, and Side effects before deploying servers.
