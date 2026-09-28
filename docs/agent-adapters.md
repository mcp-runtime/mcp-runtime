# Agent HTTP Adapter

MCP Runtime provides one agent-side adapter: `mcp-runtime adapter proxy`. It
exposes a local Streamable HTTP MCP endpoint and forwards requests to a
platform route over HTTPS.

Every adapter request carries a session-bound SPIFFE client certificate. When
the target MCP server configures OAuth, the request also carries an OAuth
bearer token. The gateway derives the human, agent, team, and session identity
from the verified certificate and, for OAuth targets, requires the token
subject to match the session human. It never accepts governance identity
headers as a fallback.

## Required grant

Create an enabled `MCPAccessGrant` before starting the adapter. The grant must
match the target server and agent. The platform refuses session and certificate
issuance when no grant matches.

```yaml
apiVersion: mcpruntime.org/v1alpha1
kind: MCPAccessGrant
metadata:
  name: triage-grant
  namespace: mcp-servers
spec:
  serverRef:
    name: workspace-assistant
  subject:
    agentID: ticket-triage-agent
  maxTrust: high
  allowedSideEffects: [read]
  policyVersion: v1
  toolRules:
    - name: add
      decision: allow
```

## In-memory enrollment

Log in, then start the proxy with the server and agent identifiers:

```bash
mcp-runtime auth login --api-url https://platform.example.com

mcp-runtime adapter proxy \
  --runtime-url https://mcp.example.com/workspace-assistant/mcp \
  --server workspace-assistant \
  --namespace mcp-servers \
  --agent ticket-triage-agent \
  --auto-refresh
```

For an OAuth-enabled target, the local MCP client normally completes OAuth and
sends `Authorization` to the adapter. The proxy preserves it. For clients that
cannot attach the header, set `--auth-header "Bearer <access-token>"` as a
static override. Omit it when the target has no OAuth configuration.

The adapter:

1. Requests an `MCPAgentSession` that is authorized by the matching grant.
2. Generates the private key and CSR locally.
3. Sends the CSR to the platform certificate endpoint.
4. Receives the signed leaf certificate and CA bundle.
5. Connects to the HTTPS route with the client certificate and, when required by the target, the OAuth bearer.
6. Renews the certificate before expiry when `--auto-refresh` is enabled.

The private key never leaves the adapter process.

## Save a certificate

Use `adapter enroll` when another process manages the proxy lifecycle:

```bash
mcp-runtime adapter enroll \
  --server workspace-assistant \
  --namespace mcp-servers \
  --agent ticket-triage-agent
```

The command saves credentials below
`$MCP_RUNTIME_CONFIG_DIR/certs/<scope>`; the default root is
`~/.mcpruntime/certs`. The scope is deterministic for the platform, namespace,
server, agent, and SPIFFE identity. The command prints the exact directory.

- `client.key` is the locally generated private key, mode `0600`.
- `client.crt` is the session-bound leaf certificate, mode `0600`.
- `ca.crt` is the CA bundle used to verify the runtime TLS certificate.

Start the proxy with the saved files:

```bash
mcp-runtime adapter proxy \
  --runtime-url https://mcp.example.com/workspace-assistant/mcp \
  --tls-client-cert ~/.mcpruntime/certs/<scope>/client.crt \
  --tls-client-key ~/.mcpruntime/certs/<scope>/client.key \
  --tls-ca-bundle ~/.mcpruntime/certs/<scope>/ca.crt
```

For an OAuth-enabled target, the local MCP request must still carry
`Authorization`, or the proxy must be started with `--auth-header`.

## CA bundle and CSR flow

The adapter creates a private key and a PKCS#10 CSR containing the expected
session SPIFFE URI. The platform validates the CSR against the issued
`MCPAgentSession`, signs it with the configured workload issuer, and returns:

- the signed client certificate;
- the CA certificate chain required to verify platform TLS certificates;
- the SPIFFE ID and expiry time.

The CA bundle does not receive or sign the CSR itself. The platform certificate
endpoint submits the validated request to the configured issuer and returns the
issuer's chain as the bundle.

## Configuration

| Environment variable | Purpose |
|---|---|
| `MCP_RUNTIME_URL` | Absolute HTTPS Streamable HTTP MCP route. |
| `MCP_RUNTIME_ADAPTER_SERVER` / `MCP_RUNTIME_ADAPTER_AGENT` | Server and agent for in-memory certificate enrollment. |
| `MCP_RUNTIME_ADAPTER_NAMESPACE` | Target namespace. |
| `MCP_RUNTIME_ADAPTER_AUTO_REFRESH` | Renew an in-memory certificate before expiry. |
| `MCP_PLATFORM_API_URL` | Platform API base URL. |
| `MCP_TRUST_DOMAIN` | Optional SPIFFE trust domain check. |
| `MCP_RUNTIME_TLS_CLIENT_CERT` / `_KEY` | Externally managed PEM client certificate and key. |
| `MCP_RUNTIME_TLS_CA_BUNDLE` | PEM CA bundle used to verify the runtime. |
| `MCP_RUNTIME_AUTH_HEADER` | Static OAuth `Authorization` override when the local MCP client does not send one; it does not set adapter identity. |
| `MCP_RUNTIME_HOST_HEADER` | Override the HTTP Host header for ingress routing. |
| `MCP_RUNTIME_LISTEN_ADDR` | Local proxy listener; defaults to `127.0.0.1:8099`. |
| `MCP_RUNTIME_REQUEST_TIMEOUT` | Timeout for adapter to runtime requests. |
| `MCP_RUNTIME_MAX_INBOUND_BYTES` | JSON-RPC request body limit; defaults to 16 MiB. |
| `MCP_RUNTIME_LOG_LEVEL` | `info` logs runtime denials to stderr. |

Non-HTTPS runtime URLs, missing certificates, invalid key pairs, and
certificate enrollment failures stop the adapter before it begins listening.
When the target enables OAuth, the gateway also rejects requests that lack a
bearer token or whose OAuth subject does not match the certificate's enrolled
session. When OAuth is omitted, the verified certificate alone authenticates
the adapter.

## Enterprise mTLS and SPIFFE

To authenticate adapters with session-bound client certificates, install
cert-manager and an internal `ClusterIssuer` backed by your company CA, Vault,
ADCS, or another workload PKI. Do not use Let's Encrypt for client certificates.

Local `setup --test-mode` installs cert-manager and provisions the bundled
`mcp-runtime-ca` ClusterIssuer automatically so this flow can be validated on
Kind without public DNS or a production CA.

```bash
mcp-runtime setup \
  --with-tls \
  --tls-cluster-issuer letsencrypt-prod \
  --mtls-cluster-issuer company-workload-ca
```

Adapter certificate authentication on **gateway** server routes is opt-in: set
`MCP_ADAPTER_CERTIFICATES=true` for setup (it passes it to the operator) in
addition to naming a workload issuer with `--mtls-cluster-issuer`. Enabling it
moves every gateway-enabled server that uses the Traefik ingress class from a
plain Ingress to a Traefik IngressRoute, and puts the gateway behind an mTLS hop
that only Traefik can reach; servers are then no longer reachable directly on
their Service. It requires `--with-tls` because Traefik terminates client TLS;
the IngressRoute uses the operator's configured ingress entrypoints (`websecure`
when none are set). `--tls-cluster-issuer` controls public ingress and registry
certificates and is separate from the workload issuer. Set `MCP_TRUST_DOMAIN` to
the platform's SPIFFE trust domain. Production must configure both
`MCP_TRUST_DOMAIN` and `MCP_SETUP_MTLS_CLUSTER_ISSUER`; test mode defaults the
issuer to `mcp-runtime-ca`.

OAuth is independent. Omit `spec.auth` for a cert-only governed route; add
`spec.auth` only when direct clients (or adapters calling an OAuth-enabled
target) need a bearer. Do not apply empty `auth: {}` unless you intend to turn
OAuth on via the derived issuer.

```yaml
# Cert-only (adapters present the session certificate; no bearer required)
spec:
  ingressHost: mcp.mcpruntime.org
  publicPathPrefix: workspace-assistant
  ingressClass: traefik
  gateway:
    enabled: true
  # omit auth

# Optional OAuth on the same certificate-capable route
spec:
  ingressHost: mcp.mcpruntime.org
  publicPathPrefix: workspace-assistant
  ingressClass: traefik
  gateway:
    enabled: true
  auth:
    mode: oauth
    issuerURL: https://auth.mcpruntime.org
    audience: https://mcp.mcpruntime.org/workspace-assistant/mcp
```

**How termination works.** Traefik verifies a presented adapter client
certificate against the platform workload CA, asserts the verified SPIFFE
identity to the gateway, and re-encrypts over a second mTLS hop. Client-supplied
`X-MCP-*` identity headers are not trusted. Ordinary OAuth clients without a
client certificate continue with Bearer only on OAuth-enabled servers. The
operator generates the Traefik `TLSOption` (`VerifyClientCertIfGiven`), the
`spiffe-identity` middleware, a `ServersTransport` (re-encrypted hop with a
pinned ingress certificate), and a path-based `IngressRoute`. A
`NetworkPolicy` restricts the gateway port to the ingress so the SPIFFE
assertion cannot be forged by another pod, and the gateway requires a
verified mTLS hop before trusting that assertion.

The caller-facing server certificate for the route is published as Traefik's
**default certificate** via a single `TLSStore` named `default`, not a
per-IngressRoute `secretName` (Traefik resolves `secretName` only in the
IngressRoute's own tenant namespace, where the shared platform host certificate
does not exist). Configure the operator with `MCP_DEFAULT_INGRESS_TLS_SECRET`
(the host certificate Secret) and `MCP_DEFAULT_INGRESS_TLS_SECRET_NAMESPACE` (a
Traefik-watched namespace holding that Secret); the operator then reconciles the
one `default` TLSStore there. When unset, Traefik falls back to its built-in
default certificate.

**Kind contributor cluster.** Adapter-certificate IngressRoutes are
websecure-only. Port-forward both Traefik ports and probe HTTPS with an
insecure TLS skip for the local default cert:

```bash
kubectl port-forward -n traefik svc/traefik 18080:8000 18443:8443
# Platform / plain Ingress samples: http://127.0.0.1:18080/...
# Adapter-certificate MCP routes: https://127.0.0.1:18443/<publicPathPrefix>/mcp
```

Enroll an external adapter after signing in to the platform:

```bash
mcp-runtime adapter enroll \
  --platform-url https://platform.mcpruntime.org \
  --server workspace-assistant \
  --namespace mcp-servers \
  --agent cursor \
  --trust-domain mcpruntime.org
```

The command generates `client.key` locally and submits only a CSR. The platform
checks that the SPIFFE URI identifies a session owned by the signed-in
principal, then returns short-lived `client.crt` and `ca.crt` under
`MCP_RUNTIME_CONFIG_DIR/certs` (normally `~/.mcpruntime/certs`).
`--platform-url` takes scheme and host with no `/api` path, and defaults to the
URL saved by `auth login` or `$MCP_PLATFORM_API_URL`. Because enrollment is
session-bound, the grant prerequisite above applies here too.

```bash
mcp-runtime adapter proxy \
  --runtime-url https://mcp.mcpruntime.org/workspace-assistant/mcp \
  --tls-client-cert ~/.mcpruntime/certs/<scope>/client.crt \
  --tls-client-key ~/.mcpruntime/certs/<scope>/client.key \
  --tls-ca-bundle ~/.mcpruntime/certs/<scope>/ca.crt
```

### One-command mode

Pass `--server` and `--agent` to `adapter proxy` so it enrolls a session-bound
certificate in memory at startup (nothing is written to disk) and feeds it
straight to the runtime transport:

```bash
mcp-runtime adapter proxy \
  --runtime-url https://mcp.mcpruntime.org/workspace-assistant/mcp \
  --platform-url https://platform.mcpruntime.org \
  --server workspace-assistant \
  --namespace mcp-servers \
  --agent cursor \
  --trust-domain mcpruntime.org \
  --auto-refresh
```

In-memory enrollment requires an `https` `--runtime-url` and the same
`--server`/`--agent` inputs as `enroll` (the certificate's SPIFFE URI encodes
the issued session). With `--auto-refresh`, the adapter re-enrolls a fresh
certificate a few minutes before the session expires and drains idle
connections so subsequent requests renegotiate with it. Long-running adapters
keep working without restarts. To reuse `enroll` output instead of in-memory
enrollment, pass the `--tls-client-cert`/`-key`/`-ca-bundle` files.

Adapters authenticate with the session-bound certificate; grant and session
policy authorize that identity. On OAuth-enabled targets they also forward the
bearer and the gateway binds the certificate session human to the OAuth
subject. Existing MCPServer resources using the removed `auth.mode: mtls` must
omit `spec.auth` for cert-only routes or set `auth.mode: oauth` with
`issuerURL` and `audience` when OAuth is required. Set platform-wide
`MCP_TRUST_DOMAIN` and `MCP_MTLS_CLUSTER_ISSUER` to enable adapter enrollment;
test-mode defaults the issuer to `mcp-runtime-ca`.

The gateway derives adapter governance identity from the verified SPIFFE URI
and the operator-rendered session binding.
