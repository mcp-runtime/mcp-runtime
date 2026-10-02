# Local Kind and Test Mode

Use this flow when you need a full local platform: API, UI, operator, registry,
Traefik, Sentinel services, and real MCP ingress routes.

## Prerequisites

Install Docker, Kind, `kubectl`, Go, `curl`, `jq`, and Python 3. Then build the
CLI:

```bash
make deps
make build
```

## Create the Kind Cluster

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
kubectl --kubeconfig "$TEST_KUBECONFIG" config rename-context \
  kind-mcp-runtime test-mcp-runtime
kubectl --kubeconfig "$TEST_KUBECONFIG" config use-context test-mcp-runtime
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
kubectl --kubeconfig "$TEST_KUBECONFIG" config rename-context \
  kind-mcp-runtime test-mcp-runtime
kubectl --kubeconfig "$TEST_KUBECONFIG" config use-context test-mcp-runtime
export KUBECONFIG="$TEST_KUBECONFIG"
kubectl get nodes
```

## Install MCP Runtime

Run preflight checks, then install with the HTTP ingress overlay:

```bash
./bin/mcp-runtime bootstrap

MCP_SETUP_WAIT_TIMEOUT=900 \
  ./bin/mcp-runtime setup --test-mode \
  --ingress-manifest config/ingress/overlays/http
```

Check the platform:

```bash
./bin/mcp-runtime status
./bin/mcp-runtime cluster status
./bin/mcp-runtime registry status
./bin/mcp-runtime sentinel status   # admin kubectl
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

Platform and plain Ingress URLs are HTTP over a port-forward because
`--test-mode` with the HTTP ingress overlay is a local-only shape.
Adapter-certificate IngressRoutes still terminate TLS on `:18443`. Any shared
or public install uses `--with-tls` and the `platform`, `registry`, and `mcp`
hostnames instead; see [Deployment Targets](../deployment-targets.md).

## Seeded Logins

`setup --test-mode` seeds local-only platform logins:

| Role | Email | Password |
|---|---|---|
| User | `test@mcpruntime.org` | `test@123` |
| Admin | `admin@mcpruntime.org` | `admin@123` |

These are controlled by `PLATFORM_DEV_*` keys in `mcp-platform-api-credentials`
Secret. They are for local debugging only.

The shared contributor cluster used for tenant-isolation smoke testing also has
these local-only tenant accounts:

| Tenant | Email | Password |
|---|---|---|
| Tenant A | `tenant-a-20260510232145@mcpruntime.org` | `TenantA-20260510232145!` |
| Tenant B | `tenant-b-20260510232145@mcpruntime.org` | `TenantB-20260510232145!` |

Fresh local clusters only have the `test` and `admin` accounts unless you create
tenant teams and users yourself.

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

Check tenant isolation in the shared contributor cluster:

```bash
rm -f /tmp/mcp-tenant-a-cookie.txt
curl -sS -c /tmp/mcp-tenant-a-cookie.txt \
  -H 'content-type: application/json' \
  -d '{"email":"tenant-a-20260510232145@mcpruntime.org","password":"TenantA-20260510232145!"}' \
  http://localhost:18080/auth/login

curl -sS -b /tmp/mcp-tenant-a-cookie.txt \
  http://localhost:18080/api/v1/runtime/servers |
  jq '{count: (.servers|length), names: [.servers[] | (.namespace + "/" + .name)]}'

curl -sS -o /tmp/mcp-tenant-a-cross.txt -w '%{http_code}\n' \
  -b /tmp/mcp-tenant-a-cookie.txt \
  'http://localhost:18080/api/v1/runtime/servers?namespace=mcp-team-tenant-b'
```

Tenant A should see `mcp-team-tenant-a`, and the explicit Tenant B namespace
read should return `403`.

## Quick cluster inventory

```bash
kubectl get pods -n mcp-runtime -o wide
kubectl get pods -n mcp-platform -o wide
kubectl get mcpservers -A \
  -o custom-columns='NAMESPACE:.metadata.namespace,NAME:.metadata.name,TEAM:.spec.teamID,PATH:.spec.ingressPath,READY:.status.deploymentReady,GW:.status.gatewayReady'
kubectl get mcpaccessgrant,mcpagentsession -A -o wide
kubectl get ingress -A
```

If you remove a stale test server, remove its matching grants, sessions, and
single-purpose analytics Secret too:

```bash
kubectl delete mcpagentsession <session-name> -n <namespace> --ignore-not-found
kubectl delete mcpaccessgrant <grant-name> -n <namespace> --ignore-not-found
kubectl delete mcpserver <server-name> -n <namespace> --ignore-not-found
kubectl delete secret <server-name>-analytics-creds -n <namespace> --ignore-not-found
```

## Optional: bundled mcp-auth integration fixture

The bundled authorization server is opt-in and separate from MCP application
deployment. Setup pulls `princekrroshan01/mcp-auth-server:latest` from Docker
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
accept. Add resources with `--mcp-auth-resource-url` (repeat the flag or
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
