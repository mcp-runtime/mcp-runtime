# Deployment Options

<span id="deployment-targets"></span>

Pick a Kubernetes target and install shape for MCP Runtime on common
self-managed and managed distributions.

- [Getting Started](self-hosting.md) has the step-by-step install flow.
- [Cluster Requirements](cluster-readiness.md) has the detailed registry,
  container runtime, DNS, ingress, TLS, and failure-mode checks.

`mcp-runtime setup` installs into an existing Kubernetes cluster. Create the
EKS, GKE, AKS, k3s, or kubeadm cluster first. Setup changes node container
runtime trust only where a documented provider path says so.

## Tested public reference

The public example platform at [platform.mcpruntime.org](https://platform.mcpruntime.org)
uses K3s for its Runtime cluster and a separate Docker/Caddy VM for its
Keycloak identity provider. Use [Cluster Provisioning](cluster-provisioning.md)
for the worked cluster example and [Public Reference Deployment](reference-deployment.md)
for configuration, secure access, certificate reuse, image rollout, identity
integration, backups, and verification. K3s is the distribution chosen by this
reference. It is the only production-validated deployment; the other entries
below describe installation shapes and prerequisites.

## Common Deployment Model

Every distribution follows the same steps:

1. Create or choose a Kubernetes cluster.
2. Configure `kubectl` for that cluster.
3. Make sure nodes can pull the registry host that MCP Runtime will use.
4. Decide ingress, DNS, TLS, storage, and image credential ownership.
5. Run `./bin/mcp-runtime bootstrap`.
6. Run `./bin/mcp-runtime cluster doctor` as the pre-setup readiness check.
7. Run `./bin/mcp-runtime setup` with the registry and TLS mode that matches
   the cluster.
8. Run `./bin/mcp-runtime cluster diagnostics` as the post-setup check suite.
9. Deploy the first MCP server and verify the dashboard/API.

Keep the environment for an install in a file and pass it with `--env-file`
so reruns use the same configuration (`config/deployments/mcpruntime-org.env.example` is the template;
variables already present in the environment are not overridden).

### Get a kubeconfig for the target distribution

Use the distribution or cloud provider's supported command to create/update a
local kubeconfig for managed clusters (EKS, GKE, AKS), Docker Desktop, kind, or
minikube. For self-managed clusters, use the administrator-provisioned
kubeconfig. On a k3s server, an authorized operator can copy
`/etc/rancher/k3s/k3s.yaml` over SSH; replace its loopback API address with the
reachable control-plane address while retaining its CA data, then restrict the
file to the current user (`chmod 600`). Do not disable TLS verification or put
kubeconfig credentials in the repo.

Select and verify the intended context before setup:

```bash
export KUBECONFIG="$HOME/.kube/<cluster>.yaml"
kubectl config get-contexts
kubectl config use-context <context-name>
kubectl config current-context
kubectl get nodes
```

Use `--context <context-name>` or `MCP_KUBE_CONTEXT` when running setup from a
multi-context kubeconfig. For the public k3s scripts, set both `KUBECONFIG`
and `MCP_SETUP_KUBECONFIG`; those scripts also accept `MCP_KUBE_CONTEXT`.

### Cluster-specific configuration to decide

Keep these values in a private env file for repeatable installs. The values
depend on the distribution and infrastructure; the public k3s profile is only
one example.

| Setting | What to configure |
|---|---|
| kubeconfig and context | Credential file, context name, API endpoint reachability, and user RBAC permissions. |
| bootstrap provider | Select a supported provider for prerequisite setup when needed; do not assume the k3s provider applies to managed clusters. |
| node architecture | `MCP_IMAGE_PLATFORM` must match the nodes that schedule platform workloads. |
| storage | Default `StorageClass`, persistence sizing, and supported access modes for the platform databases/registry. |
| ingress and DNS | Existing controller vs repo-managed Traefik; public hostnames and DNS records for `platform`, `mcp`, and `registry`. |
| TLS | Existing cert-manager `ClusterIssuer`, ACME issuer, enterprise CA, or externally managed TLS Secrets. Reuse existing certificates on routine rollouts. |
| registry | Bundled HTTP/HTTPS or external registry; node-level pull trust/auth and push credentials. For managed clouds, prefer the provider registry and workload/node identity where supported. |
| public API access | Kubernetes API endpoint used by nodes and operators; the runtime API NetworkPolicy port must match the cluster API port. |

The `hack/deploy/mcpruntime-org/` scripts encode this repository's public k3s
layout (`mcp-platform`, `mcp-observability`, bundled registry, Traefik integration, and
mcpruntime.org hostnames). For other distributions, use the generic CLI setup
and the cluster's own image publication/GitOps process unless a separate
distribution-specific rollout guide is provided.

For production-like installs, prefer:

```bash
./bin/mcp-runtime setup --with-tls --strict-prod
```

Then make registry mode explicit:

```bash
# Bundled registry with public TLS ingress (k3s on-prem, bundled-https).
# Pod pulls must use the TLS-covered registry hostname, not the registry Service ClusterIP.
./bin/mcp-runtime setup \
  --registry-mode bundled-https \
  --with-tls \
  --strict-prod
```

When `MCP_PLATFORM_DOMAIN` is set, export `MCP_REGISTRY_ENDPOINT=registry.<domain>`
before setup so in-cluster image pulls use the same hostname as the Let's Encrypt
certificate. Using the registry Service ClusterIP with `bundled-https` causes
`ImagePullBackOff` (`x509: cannot validate certificate ... doesn't contain any IP SANs`).

For an existing external registry:

```bash
# Existing managed or enterprise registry.
./bin/mcp-runtime setup \
  --registry-mode external \
  --external-registry-url registry.example.com \
  --with-tls \
  --strict-prod
```

## Choose a Target

| Target | Best use | Registry recommendation | Notes |
|---|---|---|---|
| kind | Contributor development, CI-like smoke tests, disposable clusters | Bundled HTTP registry with the documented kind mirror | Use [Contributor Local Kind](contributor/local-kind.md) and `setup --test-mode`. |
| Docker Desktop Kubernetes | Laptop demos and local evaluation | Bundled HTTP registry or Docker Desktop image loading | Good for local UI/API exploration, not production. |
| minikube | Laptop or VM evaluation | Insecure registry flag at cluster start, or `minikube image load` | Recreate minikube when changing insecure registry settings. |
| k3s | Single-node lab, edge, small self-managed clusters | Bundled HTTP for labs; bundled HTTPS or external for production | **Tested public reference:** [platform.mcpruntime.org](https://platform.mcpruntime.org). See [Public Reference Deployment](reference-deployment.md) (production operations), [Cluster Provisioning](cluster-provisioning.md) (topology), and [Cluster Readiness - k3s](cluster-readiness.md#k3s). |
| kubeadm / vanilla Kubernetes | Self-managed production or staging | External registry, or bundled HTTPS with node CA trust | Configure containerd, DNS, ingress, storage, and TLS on every node. |
| RKE2 | Self-managed production or staging | External registry, or bundled HTTPS with node CA trust | Treat it like a hardened self-managed cluster; use provider tooling for runtime config. |
| EKS | AWS managed Kubernetes | ECR | Use AWS-managed node registry auth, a real ingress/load balancer, Route 53 or equivalent DNS, and cert-manager or enterprise TLS. |
| GKE | Google managed Kubernetes | Artifact Registry | Use node/workload identity registry access, Cloud DNS or equivalent DNS, and a Kubernetes ingress controller compatible with this platform. |
| AKS | Azure managed Kubernetes | ACR | Use AKS/ACR integration or pull secrets, Azure DNS or equivalent DNS, and a supported ingress/TLS path. |

OpenShift and other Kubernetes distributions have no documented install path
yet. They work only if the cluster satisfies the same Kubernetes contracts: CRDs, Deployments, Services, Ingress, storage, image pulls, TLS
secrets, and pod security requirements. Review the generated manifests and
platform security policy before using those clusters.

## Self-Managed Clusters

On self-managed clusters you control node runtime configuration, and you own
every node pull path.

### HTTPS registry NodePort migration

HTTPS registry overlays expose a ClusterIP Service, with no unauthenticated
NodePort. Public publication continues through the authenticated registry
ingress; bundled HTTPS node pulls use the configured internal Service endpoint.
The plain HTTP lab overlays retain NodePort 32000.

Before upgrading an HTTPS installation that configured a containerd mirror at
`127.0.0.1:32000` or a node address on port 32000, switch every node to the
supported bundled HTTPS pull endpoint and configure its CA trust. Verify a new
image pull before applying the registry overlay. An upgrade removes that
NodePort; existing running containers remain running, but stale mirrors would
prevent subsequent pulls. Back up the containerd configuration and preserve
registry storage and certificates. Do not expose another backend port as a
workaround. This closes the node exposure; repository-scoped authentication on
the internal endpoint is still tracked in #531.

### k3s lab example

Use this for a single-node k3s lab or internal evaluation with the bundled
plain HTTP registry. Do not copy the insecure registry settings into
production.

1. Install k3s and point your shell at its kubeconfig:

   ```bash
   export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
   kubectl get nodes
   ```

2. Preconfigure k3s containerd for the bundled registry NodePort:

   ```bash
   sudo tee /etc/rancher/k3s/registries.yaml >/dev/null <<'EOF'
   mirrors:
     registry.local:
       endpoint:
         - "http://127.0.0.1:32000"
   configs:
     "127.0.0.1:32000":
       tls:
         insecure_skip_verify: true
   EOF

   echo "127.0.0.1 registry.local" | sudo tee -a /etc/hosts
   sudo systemctl restart k3s
   ```

   Multi-node k3s needs equivalent registry mirror and host resolution on every
   node that can schedule MCP Runtime pods.

3. Build the CLI and run the k3s bootstrap apply path:

   ```bash
   make deps
   make build

   ./bin/mcp-runtime bootstrap --apply --provider k3s
   ```

   `bootstrap --apply --provider k3s` is the only automated prerequisite apply
   path today. It installs the bundled CoreDNS and local-path manifests when
   they are missing.

4. Install the platform:

   ```bash
   MCP_SETUP_WAIT_TIMEOUT=900 \
     MCP_REGISTRY_ENDPOINT=registry.local:32000 \
     ./bin/mcp-runtime setup
   ```

5. Validate the rollout:

   ```bash
   ./bin/mcp-runtime status
   ./bin/mcp-runtime cluster diagnostics
   kubectl get pods -n mcp-platform
   kubectl get pods -n mcp-observability
   ```

If setup prints a different registry internal URL, copy that exact `host:port`
into `/etc/rancher/k3s/registries.yaml`, restart k3s, and rerun setup. k3s
containerd registry matching is exact.

### k3s production-style shape

For a public or persistent k3s cluster there are two common registry shapes.

#### Option A: bundled HTTPS registry (on-prem reference)

Use this when MCP Runtime owns the in-cluster registry and exposes it at
`registry.<domain>` with Let's Encrypt (or an enterprise issuer). k3s often
already runs Traefik in `kube-system`. Pass `--ingress none`, set
`PLATFORM_TRAEFIK_NAMESPACE=kube-system`, and
`PLATFORM_TEAM_TRAEFIK_WATCH=disabled` so setup does not install a second
ingress stack and team create does not patch k3s Traefik.

Set `MCP_REGISTRY_ENDPOINT=registry.<domain>` (the TLS-covered hostname) before
setup so pod pulls match the certificate. Using the registry Service ClusterIP
causes `ImagePullBackOff` with `x509: ... doesn't contain any IP SANs`.

Copy `config/deployments/mcpruntime-org.env.example` to
`mcpruntime-org.env`, then follow **[Public Reference Deployment](reference-deployment.md)**
for first install (`--acme-email`), reruns (`hack/deploy/mcpruntime-org/setup.sh`
intentionally omits `--acme-email` and uses `--tls-cluster-issuer` instead),
safe clean+restore, rollout-only updates, the full environment variable reference,
and multitenancy validation.

#### Option B: external registry

Use when images live in a registry you already operate (Harbor, ECR mirror, etc.):

```bash
export MCP_PLATFORM_DOMAIN=example.com
export MCP_PLATFORM_ADMIN_EMAIL=admin@example.com
export GOOGLE_CLIENT_ID=<google-oauth-client-id>

./bin/mcp-runtime bootstrap --provider k3s
./bin/mcp-runtime setup \
  --registry-mode external \
  --external-registry-url registry.example.com \
  --with-tls \
  --acme-email ops@example.com \
  --strict-prod
```

Use `--tls-cluster-issuer <issuer-name>` instead of `--acme-email` when your
cluster already has an enterprise `ClusterIssuer`.

For a complete four-node reference topology, worker join commands, ServiceLB
pinning, public DNS, Cloudflare or enterprise proxy front doors, TLS, registry,
validation, and a five-node extension, use [Cluster Provisioning](cluster-provisioning.md).

### kubeadm, RKE2, and other self-managed clusters

For self-managed production clusters:

- Use a stable registry endpoint that every node can resolve and trust.
- Configure containerd or your node runtime on every node pool.
- Use an ingress controller that creates Kubernetes `Ingress` routes, or run
  `setup --ingress none` only when equivalent ingress and registry auth are
  managed outside this repo.
- Install or verify a default `StorageClass`.
- Decide whether cert-manager, an enterprise issuer, or pre-created TLS secrets
  own platform certificates.

Then use the same production-style setup command:

```bash
./bin/mcp-runtime bootstrap
./bin/mcp-runtime cluster doctor
./bin/mcp-runtime setup --with-tls --strict-prod
./bin/mcp-runtime cluster diagnostics
```

## Managed Kubernetes

The provider manages node lifecycle. You still choose the registry, DNS, TLS,
and ingress. In most managed environments, use an external registry.

### EKS

Recommended shape:

- Registry: ECR.
- Node pull auth: EKS node role, IRSA, or explicit image pull secrets.
- DNS: Route 53 or your enterprise DNS.
- Ingress: existing platform ingress controller, AWS Load Balancer Controller,
  ingress-nginx, or Traefik, as long as it supports the required Kubernetes
  `Ingress` routes and registry auth guard.
- TLS: cert-manager with Let's Encrypt or an enterprise issuer.

Setup shape:

```bash
export MCP_PLATFORM_DOMAIN=example.com
export MCP_PLATFORM_ADMIN_EMAIL=admin@example.com
export GOOGLE_CLIENT_ID=<google-oauth-client-id>

./bin/mcp-runtime bootstrap
./bin/mcp-runtime setup \
  --registry-mode external \
  --external-registry-url <account>.dkr.ecr.<region>.amazonaws.com/mcp-runtime \
  --with-tls \
  --strict-prod
```

### GKE

Recommended shape:

- Registry: Artifact Registry.
- Node pull auth: Google-managed node identity or workload identity where
  appropriate.
- DNS: Cloud DNS or enterprise DNS.
- Ingress: GKE ingress, ingress-nginx, or Traefik, with equivalent registry
  auth if you do not use the repo-managed Traefik dynamic config.
- TLS: cert-manager with Let's Encrypt or an enterprise issuer.

Setup shape:

```bash
export MCP_PLATFORM_DOMAIN=example.com
export MCP_PLATFORM_ADMIN_EMAIL=admin@example.com
export GOOGLE_CLIENT_ID=<google-oauth-client-id>

./bin/mcp-runtime bootstrap
./bin/mcp-runtime setup \
  --registry-mode external \
  --external-registry-url <region>-docker.pkg.dev/<project>/<repo> \
  --with-tls \
  --strict-prod
```

### AKS

Recommended shape:

- Registry: ACR.
- Node pull auth: AKS to ACR attachment, managed identity, or pull secrets.
- DNS: Azure DNS or enterprise DNS.
- Ingress: Application Gateway Ingress Controller, ingress-nginx, or Traefik,
  with equivalent registry auth if you replace repo-managed Traefik.
- TLS: cert-manager with Let's Encrypt or an enterprise issuer.

Setup shape:

```bash
export MCP_PLATFORM_DOMAIN=example.com
export MCP_PLATFORM_ADMIN_EMAIL=admin@example.com
export GOOGLE_CLIENT_ID=<google-oauth-client-id>

./bin/mcp-runtime bootstrap
./bin/mcp-runtime setup \
  --registry-mode external \
  --external-registry-url <registry>.azurecr.io/mcp-runtime \
  --with-tls \
  --strict-prod
```

## Ingress and registry ownership

MCP Runtime installs repo-managed Traefik or reuses an existing ingress
controller. Run one ingress stack per public surface.

If you bring your own ingress controller:

- Make sure it watches the namespaces MCP Runtime uses.
- Make sure it can serve `platform.<domain>`, `mcp.<domain>`, and
  `registry.<domain>` when `MCP_PLATFORM_DOMAIN` is set.
- On k3s, Traefik in `kube-system` already watches cluster-wide; use
  `setup --ingress none` and set `PLATFORM_TRAEFIK_NAMESPACE=kube-system` /
  `PLATFORM_TEAM_TRAEFIK_WATCH=disabled` so team create does not install a
  second Traefik stack.
- Provide an equivalent registry auth guard before exposing
  `registry.<domain>` publicly. The repo-managed Traefik stack uses
  `registry-admin-auth@file` backed by `/api/v1/registry/authz`.
- Use `./bin/mcp-runtime setup --ingress none ...` only when that external
  ingress path is already prepared.

## After Setup

Run the same checks on every distribution:

```bash
./bin/mcp-runtime status
./bin/mcp-runtime cluster diagnostics

kubectl get pods -n mcp-runtime
kubectl get pods -n mcp-platform
kubectl get pods -n mcp-observability
kubectl get ingress -A
```

For host-based public installs, also verify:

```bash
getent hosts registry.<domain>
getent hosts mcp.<domain>
getent hosts platform.<domain>

curl -k -I https://platform.<domain>/
curl -k -I -H "x-api-key: $ADMIN_API_KEY" https://registry.<domain>/v2/
```

Then continue with [Getting Started - Deploy your first
server](self-hosting.md#7-deploy-your-first-server).
