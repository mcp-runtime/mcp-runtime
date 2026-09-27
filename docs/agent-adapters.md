# Agent Adapters

MCP Runtime includes two agent-side adapters that attach governed identity to
MCP traffic. The agent framework needs no knowledge of grants, sessions, or
policy:

- `mcp-runtime adapter proxy` exposes a local Streamable HTTP MCP endpoint and
  forwards requests to an MCP Runtime route.
- `mcp-runtime adapter stdio` exposes a stdio MCP server process and forwards
  each JSON-RPC message to the same MCP Runtime HTTP route.

Both adapters only **present** issued identity values; all traffic still goes
through the gateway, which enforces policy. Platform admins author
`MCPAccessGrant` resources first (scaffold them with
`mcp-runtime access grant init`), and the platform API issues `MCPAgentSession`
values through `POST /api/v1/runtime/adapter/sessions` when the adapter starts
with `--server` and `--agent`.

The adapters support stdio and Streamable HTTP, the two standard MCP
transports. There is no separate legacy HTTP+SSE adapter.

## How the adapter gets its identity

There are three supported ways to give an adapter its `humanID`, `agentID`,
`teamID`, and `sessionID`:

1. **Platform-issued session (recommended).** The adapter calls
   `POST /api/v1/runtime/adapter/sessions`. The platform derives the principal
   from your `mcp-runtime auth login` token, picks a matching enabled
   `MCPAccessGrant`, writes (or reuses) an `MCPAgentSession`, and returns the
   identity values. Optional `--auto-refresh` renews the session before
   expiry without restarting the adapter.
2. **Explicit flags / environment.** `--human-id`, `--agent-id`,
   `--session-id`, `--team-id` (or the matching `MCP_RUNTIME_*` env vars).
   Useful for testing and for inheriting an externally-managed session.
3. **Anonymous mode** (stdio only): `--anonymous` skips identity entirely so
   the adapter can target public/read-only runtime routes. Only the methods
   listed in `--anonymous-methods` are forwarded.

Mixed configurations are supported: identity flags always override values
returned by the platform-issued session, so a caller can pin a specific field
(e.g. a long-lived `--session-id` for a test) while letting the platform fill
in the rest. The override survives every auto-refresh tick.

## Platform-issued sessions: quickstart

Apply the grant first. The platform issues (and later refreshes) an adapter
session only when an enabled `MCPAccessGrant` matches the server, the signed-in
principal, and the agent; without one the session call returns 403 and the
adapter refuses to start. See [Required grant](#required-grant) below.

```bash
mcp-runtime auth login --api-url https://platform.example.com

mcp-runtime adapter stdio \
  --runtime-url https://mcp.example.com/oauth-example-go-2025-11-25-gateway/mcp \
  --server oauth-example-go-2025-11-25-gateway \
  --agent ticket-triage-agent \
  --auto-refresh
```

What this does on each invocation:

1. The CLI calls `POST /api/v1/runtime/adapter/sessions` with `{serverName,
   namespace?, agentID}`. `namespace` defaults to the principal's primary
   namespace.
2. The platform derives `humanID` from `Principal.Subject` (fallback to
   `Email`) and `teamID` from the principal's membership in the namespace's
   team.
3. The platform lists enabled `MCPAccessGrant` resources in that namespace,
   filters those whose `serverRef.name` matches and whose subject equals the
   caller or is empty (wildcard), and picks the grant with the highest
   `MaxTrust`. Ties are broken by oldest `creationTimestamp`.
4. The platform looks up an existing `MCPAgentSession` with the deterministic
   name `adapter-<sha256-prefix(humanID,agentID,teamID,serverName)>` in the
   namespace. If one exists and is not revoked, has more than 30 s until
   expiry, and its `policyVersion` matches the selected grant's, it is
   reused. Otherwise a fresh `MCPAgentSession` is applied with a 1 h TTL
   (capped at 24 h).
5. The response carries `name`, `humanID`, `agentID`, `teamID`,
   `consentedTrust`, `policyVersion`, and absolute `expiresAt`. The adapter
   uses `name` as `X-MCP-Agent-Session` on every outbound request.
6. With `--auto-refresh`, a background goroutine renews the session ~5 min
   before `expiresAt` and atomically rotates the identity. In-flight requests
   continue with the previous identity; subsequent requests pick up the new
   one without a restart. Transient platform errors are logged to stderr; the
   previous identity stays in place until a refresh succeeds.

### Required grant

A grant must exist before the platform will issue a session. Example:

```yaml
apiVersion: mcpruntime.org/v1alpha1
kind: MCPAccessGrant
metadata:
  name: triage-grant
  namespace: mcp-servers
spec:
  serverRef:
    name: oauth-example-go-2025-11-25-gateway
  subject:
    # Any of these may be empty to act as a wildcard for that field.
    humanID: support-lead
    agentID: ticket-triage-agent
    teamID: team-acme
  maxTrust: high
  allowedSideEffects:
    - read
  policyVersion: v1
  toolRules:
    - name: add
      decision: allow
      requiredTrust: low
    - name: upper
      decision: allow
      requiredTrust: low
```

If the principal does not match any enabled grant for the server, the
adapter-session endpoint returns 403 and the adapter refuses to start.

## Explicit-identity mode

When you already have an `MCPAgentSession` and don't want the platform to pick
the grant for you (for example in a fixed CI environment), set everything
explicitly:

```bash
export MCP_RUNTIME_URL=http://localhost:18080/oauth-example-go-2025-11-25-gateway/mcp
export MCP_RUNTIME_HUMAN_ID=support-lead
export MCP_RUNTIME_AGENT_ID=ticket-triage-agent
export MCP_RUNTIME_SESSION_ID=sess-ticket-triage-agent

mcp-runtime adapter proxy
```

| Environment variable | Required | Purpose |
|---|---:|---|
| `MCP_RUNTIME_URL` | yes | Absolute Streamable HTTP MCP route. |
| `MCP_RUNTIME_HUMAN_ID` | yes¹ | Human identity (`X-MCP-Human-ID`). |
| `MCP_RUNTIME_AGENT_ID` | yes¹ | Agent identity (`X-MCP-Agent-ID`). |
| `MCP_RUNTIME_TEAM_ID` | no | Team identity (`X-MCP-Team-ID`) for team-scoped grants. |
| `MCP_RUNTIME_SESSION_ID` | yes¹ | `MCPAgentSession` name (`X-MCP-Agent-Session`). |
| `MCP_RUNTIME_HOST_HEADER` | no | Override the `Host` header for host-based ingress. |
| `MCP_RUNTIME_LISTEN_ADDR` | proxy | Local listener; defaults to `127.0.0.1:8099`. |
| `MCP_RUNTIME_PROTOCOL_VERSION` | stdio | `MCP-Protocol-Version` header the stdio adapter sends for legacy (`initialize`-based) requests. Defaults to `2025-06-18`; the negotiated `result.protocolVersion` from the runtime's `initialize` response overrides it for the rest of the process. Requests that declare a version in `params._meta` use that version. The HTTP proxy forwards the client's own header. |
| `--no-xforwarded` flag | proxy | Pass this flag to suppress `X-Forwarded-*` headers forwarded to the runtime. Defaults to enabled (headers are sent). There is no corresponding env var. |
| `MCP_RUNTIME_REQUEST_TIMEOUT` | no | Go duration for adapter→runtime calls. Defaults to unbounded. |
| `MCP_RUNTIME_MAX_INBOUND_BYTES` | proxy | Caps inbound JSON-RPC bodies; over-cap responds 413. Defaults to 16 MiB. |
| `MCP_RUNTIME_AUTH_HEADER` | no | Static `Authorization` header injected on every runtime request (e.g. `Bearer …`). |
| `MCP_RUNTIME_TLS_CLIENT_CERT` / `_KEY` | no | PEM client cert / key for mTLS to the runtime. |
| `MCP_RUNTIME_TLS_CA_BUNDLE` | no | PEM CA bundle replacing the system trust store. |
| `MCP_RUNTIME_ANONYMOUS` | stdio | `true` enables anonymous mode. |
| `MCP_RUNTIME_ANONYMOUS_METHODS` | stdio | CSV allowlist of methods in anonymous mode. |
| `MCP_RUNTIME_TOOLS_CACHE_TTL` | stdio | Caches `tools/list` responses for this duration (e.g. `30s`). Anonymous mode bypasses the cache. |
| `MCP_RUNTIME_LOG_LEVEL` | no | `info` logs runtime 4xx denials to stderr. |

¹ Required unless `--server` (platform-issued session) or `--anonymous` is in
use. With `--server`, missing fields are populated from the issued response.

The adapters inject these headers on every forwarded request:

```text
X-MCP-Human-ID:      <humanID>
X-MCP-Agent-ID:      <agentID>
X-MCP-Team-ID:       <teamID>            (omitted when empty)
X-MCP-Agent-Session: <sessionID>
Authorization:       <MCP_RUNTIME_AUTH_HEADER>   (when set)
```

Incoming spoofed values for the four governance headers are stripped before
the upstream call. MCP protocol headers (`Mcp-Protocol-Version`,
`Mcp-Session-Id`, `content-type`, `accept`) are preserved.

## Anonymous mode (stdio)

Public read-only routes, such as a catalog discovery endpoint, need no
identity. Run the stdio shim anonymously:

```bash
mcp-runtime adapter stdio \
  --runtime-url https://mcp.example.com/public-catalog/mcp \
  --anonymous \
  --anonymous-methods initialize,notifications/initialized,server/discover,ping,tools/list,resources/list,prompts/list
```

Anonymous methods default to the protocol handshake (`initialize` for legacy
clients, `server/discover` for MCP `2026-07-28` clients), `ping`, and the three
read-only discovery calls. Any method outside the allowlist is rejected with a JSON-RPC
`-32601` error before the request leaves the adapter, so an agent SDK cannot
accidentally call `tools/call` against a public route.

The `tools/list` cache is **bypassed** in anonymous mode: different anonymous
callers can see different responses depending on what the runtime exposes
publicly, and there is no safe shared cache key.

## Direct HTTP clients

When the agent framework supports Streamable HTTP MCP and custom headers,
you can call the runtime directly without the adapter. Mint a session with
the platform API once, then attach the returned identity on every request.
Use a platform login token or user API key for this call; service-only setup
keys do not carry a human subject and cannot mint adapter sessions.

```python
import asyncio
import os
import httpx

from agents import Agent, Runner
from agents.mcp import MCPServerStreamableHttp

async def main() -> None:
    platform_token = os.environ["MCP_PLATFORM_API_TOKEN"]
    async with httpx.AsyncClient() as http:
        resp = await http.post(
            os.environ["MCP_PLATFORM_API_URL"].rstrip("/")
            + "/api/v1/runtime/adapter/sessions",
            json={
                "serverName": "oauth-example-go-2025-11-25-gateway",
                "agentID": "ticket-triage-agent",
            },
            headers={
                "Authorization": f"Bearer {platform_token}",
            },
        )
        resp.raise_for_status()
        session = resp.json()

    async with MCPServerStreamableHttp(
        name="oauth-example-go-2025-11-25-gateway",
        params={
            "url": os.environ["MCP_RUNTIME_URL"],
            "headers": {
                "X-MCP-Human-ID": session["humanID"],
                "X-MCP-Agent-ID": session["agentID"],
                "X-MCP-Team-ID": session.get("teamID", ""),
                "X-MCP-Agent-Session": session["name"],
            },
        },
    ) as server:
        agent = Agent(
            name="Governed Agent",
            instructions="Use MCP tools when they help.",
            mcp_servers=[server],
        )
        print((await Runner.run(agent, "Add 2 and 3.")).final_output)

asyncio.run(main())
```

This is the only path where the consumer calls the platform API itself. For
framework code that cannot attach headers, use the proxy or stdio adapter
below.

## HTTP proxy adapter

Use the proxy when a framework can speak Streamable HTTP MCP but cannot
attach the governance headers itself.

```bash
mcp-runtime adapter proxy \
  --runtime-url https://mcp.example.com/oauth-example-go-2025-11-25-gateway/mcp \
  --server oauth-example-go-2025-11-25-gateway \
  --agent ticket-triage-agent \
  --auto-refresh
```

Then point the framework's MCP URL at the local proxy:

```text
http://127.0.0.1:8099/mcp
```

The proxy forwards to the exact `--runtime-url` route. The local request path
is accepted for client compatibility; it is not appended upstream. Query
strings from the configured URL and client request are merged. By default the
proxy adds `X-Forwarded-*` headers; set `--no-xforwarded` to suppress them on
loopback paths where they only add audit noise.

The proxy also exposes:

- `GET /healthz`, `GET /livez`, `GET /readyz`: 204 No Content when running.
- `GET /metrics`: delegates to `ProxyConfig.MetricsHandler` when wired up
  (typically a Prometheus exporter backed by `RuntimeTransport.Meter`).
  Returns 404 when no metrics handler is configured.

Inbound JSON-RPC bodies over `--max-inbound-bytes` (default 16 MiB) get HTTP
413 with a JSON-RPC parse-error body so the agent SDK can recover.

This shape works for LangChain, LlamaIndex, CrewAI, custom Python/Go/Node
services, or any other MCP-aware runtime that can talk to a Streamable HTTP
URL.

## Stdio shim

Use the shim when an IDE or client only launches stdio MCP commands (Cursor,
Claude Desktop, similar):

```json
{
  "mcpServers": {
    "oauth-example-go-2025-11-25-gateway": {
      "command": "/absolute/path/to/bin/mcp-runtime",
      "args": [
        "adapter", "stdio",
        "--runtime-url", "https://mcp.example.com/oauth-example-go-2025-11-25-gateway/mcp",
        "--server", "oauth-example-go-2025-11-25-gateway",
        "--agent", "ticket-triage-agent",
        "--auto-refresh"
      ],
      "env": {
        "MCP_PLATFORM_API_URL": "https://platform.example.com",
        "MCP_PLATFORM_API_TOKEN": "..."
      }
    }
  }
}
```

The shim reads newline-delimited JSON-RPC messages from stdin, posts them to
the runtime, and writes responses back to stdout.

Shim behavior:

- `initialize` is forwarded synchronously so the runtime `Mcp-Session-Id` is
  captured before later requests. The negotiated `protocolVersion` from the
  `result` body is also captured and used on subsequent calls.
- Streamable HTTP `text/event-stream` responses are streamed through frame
  by frame so server-to-client requests and progress messages flow without
  waiting for the runtime to close the response. The shim watches each SSE
  frame for `notifications/tools/list_changed` and invalidates its
  `tools/list` cache when it sees one.
- The shim leaves `MCP_RUNTIME_REQUEST_TIMEOUT` unset by default so
  long-running tool calls are not cut off. Set it when fail-fast behaviour
  is preferable.
- Platform 4xx denials (e.g. `trust_too_low`) are returned to stdio clients
  as JSON-RPC errors. Runtime denials matching `session_expired` /
  `session_not_found` are repackaged with `error.data.runtime_status =
  "session_expired"` so the SDK can choose to re-initialize.
- Idempotent reads (`tools/list`, `resources/list`, `prompts/list`, `ping`,
  `server/discover`) retry on `502`/`504`/connection-reset with exponential backoff (100 ms →
  200 ms → 1 s cap). `tools/call` never retries automatically.

### MCP 2026-07-28 clients

MCP revision [`2026-07-28`](https://modelcontextprotocol.io/specification/2026-07-28/changelog)
removed the `initialize` handshake. A modern client puts its protocol version
in each request's `params._meta` (`io.modelcontextprotocol/protocolVersion`).
The shim serves legacy and modern clients side by side. For a request that
declares `2026-07-28` or later, it:

- sends `MCP-Protocol-Version` from that request's `_meta`, so the header always
  matches the body;
- adds `Mcp-Method`, and `Mcp-Name` for `tools/call`, `prompts/get`, and
  `resources/read`, Base64-encoding values that cannot safely appear as plain
  HTTP header values, including non-ASCII and control characters, leading or
  trailing whitespace, and values matching the Base64 sentinel pattern;
- mirrors tool arguments annotated with `x-mcp-header` into `Mcp-Param-*`
  headers, using the schemas from earlier `tools/list` results. Tools whose
  annotations are invalid are dropped from `tools/list` with a warning on
  stderr;
- sends no `Mcp-Session-Id`;
- on a stdio `notifications/cancelled` for an in-flight request, closes that
  HTTP request and does not forward the notification;
- keeps `subscriptions/listen` streams open regardless of
  `MCP_RUNTIME_REQUEST_TIMEOUT`.

Requests without a `_meta` version keep the legacy behavior described above.

## Expected outcomes

- A low-trust allowed tool call succeeds when the grant, session, and tool
  rule permit it.
- If the active session consents to less trust than a tool requires, the
  runtime returns `trust_too_low` and the adapter surfaces it as a JSON-RPC
  error to the client.
- Disabling or revoking the platform-side grant/session blocks calls
  without changing adapter configuration. With `--auto-refresh`, the next
  refresh tick may detect the new state (no matching grant → 403, surfaced
  in the adapter's stderr; the previous identity remains in use until
  expiry).
- Restarting the adapter against a revoked or expired session yields a
  fresh `MCPAgentSession` automatically, since the reuse predicate excludes
  revoked/near-expiry sessions.

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

Adapter certificate authentication on OAuth-configured server routes is
opt-in: set `MCP_ADAPTER_CERTIFICATES=true` for setup (it passes it to the
operator) in addition to naming a workload issuer with `--mtls-cluster-issuer`.
Enabling it moves every OAuth server that uses the Traefik ingress class and
the gateway from a plain Ingress to a Traefik IngressRoute, and puts the
gateway behind an mTLS hop that only Traefik can reach; servers are then no
longer reachable directly on their Service. It requires `--with-tls` because
Traefik terminates client TLS; the IngressRoute uses the operator's configured
ingress entrypoints (`websecure` when none are set).
`--tls-cluster-issuer` controls public ingress and registry certificates and is
separate from the workload issuer. Set `MCP_TRUST_DOMAIN` to the platform's
SPIFFE trust domain. Production must configure both `MCP_TRUST_DOMAIN` and
`MCP_SETUP_MTLS_CLUSTER_ISSUER`; test mode defaults the issuer to
`mcp-runtime-ca`.

Keep the MCPServer in OAuth mode. Ordinary OAuth clients use the normal URL
without a client certificate; adapters can enroll a session-bound certificate
for certificate-based identity on that same URL.

```yaml
spec:
  ingressHost: mcp.example.com
  publicPathPrefix: workspace-assistant
  ingressClass: traefik
  gateway:
    enabled: true
  auth:
    mode: oauth
    issuerURL: https://auth.example.com
    audience: https://mcp.example.com/workspace-assistant/mcp
```

**How termination works.** Traefik optionally verifies a presented client
certificate against the platform workload CA, injects the verified SPIFFE
identity as a trusted header (`X-MCP-Verified-SPIFFE-ID`), and re-encrypts to
the gateway over a second mTLS hop. Without a client certificate, OAuth clients
continue through the normal route. The operator generates the Traefik
`TLSOption` (`VerifyClientCertIfGiven`), the `spiffe-identity` middleware
(preserves governance headers for OAuth requests without a certificate; strips
them and injects verified session identity for adapter certificates), a
`ServersTransport` (the re-encrypted hop with a pinned ingress certificate), and
a path-based `IngressRoute`. A `NetworkPolicy` restricts the gateway port to the
ingress so the trusted header cannot be forged by another pod, and the gateway
requires the connection to be a verified mTLS hop before trusting the header.

The caller-facing server certificate for the OAuth route is
published as Traefik's **default certificate** via a single `TLSStore` named
`default`, not a per-IngressRoute `secretName` (Traefik resolves `secretName`
only in the IngressRoute's own tenant namespace, where the shared platform host
certificate does not exist). Configure the operator with
`MCP_DEFAULT_INGRESS_TLS_SECRET` (the host certificate Secret) and
`MCP_DEFAULT_INGRESS_TLS_SECRET_NAMESPACE` (a Traefik-watched namespace holding
that Secret); the operator then reconciles the one `default` TLSStore there.
When unset, Traefik falls back to its built-in default certificate.

Enroll an external adapter after signing in to the platform:

```bash
mcp-runtime adapter enroll \
  --platform-url https://platform.example.com \
  --server workspace-assistant \
  --namespace mcp-servers \
  --agent cursor \
  --trust-domain mcpruntime.org \
  --output-dir ~/.config/mcp-runtime/workspace-assistant
```

The command generates `client.key` locally and submits only a CSR. The platform
checks that the SPIFFE URI identifies a session owned by the signed-in
principal, then returns short-lived `client.crt` and `ca.crt` files.
`--platform-url` takes scheme and host with no `/api` path, and defaults to the
URL saved by `auth login` or `$MCP_PLATFORM_API_URL`. Because enrollment is
session-bound, the grant prerequisite above applies here too.

```bash
mcp-runtime adapter proxy \
  --runtime-url https://mcp.example.com/workspace-assistant/mcp \
  --tls-client-cert ~/.config/mcp-runtime/workspace-assistant/client.crt \
  --tls-client-key ~/.config/mcp-runtime/workspace-assistant/client.key \
  --tls-ca-bundle ~/.config/mcp-runtime/workspace-assistant/ca.crt
```

### One-command mode: `--auth mtls`

`--auth mtls` collapses the enroll-then-run steps into a single command. The
adapter enrolls a session-bound certificate in memory at startup (nothing is
written to disk) and feeds it straight to the runtime transport:

```bash
mcp-runtime adapter proxy \
  --auth mtls \
  --runtime-url https://mcp.example.com/workspace-assistant/mcp \
  --platform-url https://platform.example.com \
  --server workspace-assistant \
  --namespace mcp-servers \
  --agent cursor \
  --trust-domain mcpruntime.org \
  --auto-refresh
```

`--auth mtls` requires an `https` `--runtime-url` and the same
`--server`/`--agent` inputs as `enroll` (the certificate's SPIFFE URI encodes
the issued session). With `--auto-refresh`, the adapter re-enrolls a fresh
certificate a few minutes before the session expires and drains idle
connections so subsequent requests renegotiate with it. Long-running adapters
keep working without restarts. Governance identity headers are suppressed in
this mode. To reuse `enroll` output instead of in-memory enrollment, pass
`--auth mtls` together with the `--tls-client-cert`/`-key`/`-ca-bundle` files.

Clients without an adapter certificate use OAuth. An adapter can authenticate
with its session-bound certificate; grant and session policy then authorize its
identity. Existing MCPServer resources using the removed `auth.mode: mtls` must
be changed to `auth.mode: oauth` and configured with `issuerURL` and `audience`.
The operator rejects old stored values and removes their obsolete per-server
certificate and ingress resources. Set platform-wide `MCP_TRUST_DOMAIN` and
`MCP_MTLS_CLUSTER_ISSUER` to enable adapter enrollment; test-mode defaults the
issuer to `mcp-runtime-ca`.

For adapter-certificate requests, the gateway ignores caller-supplied `X-MCP-*`
identity headers and derives identity from the verified SPIFFE URI and the
operator-rendered session binding. OAuth clients without a certificate retain
the normal OAuth and session-header flow.
