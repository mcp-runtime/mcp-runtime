# Runtime

The runtime is the Kubernetes control plane for MCP servers. It handles cluster bootstrap, the registry, ingress setup, operator reconciliation, deployment resources, rollout, and the access model for each server. Requests then pass to the [Sentinel](sentinel.md) request path.

The runtime runs on top of your ingress and networking layer and handles MCP-specific delivery, access, and rollout. It does not route general cluster traffic.

## What the runtime owns

| Area | Responsibility |
|---|---|
| **Bootstrap** | `cluster` and `setup` initialize CRDs and namespaces, configure ingress, provision clusters, optionally wire cert-manager TLS. |
| **Registry workflow** | Registry commands and setup wiring provide a place to publish and pull MCP server images. |
| **Server delivery** | The operator reconciles `MCPServer` into Deployments, Services, and Ingress. Each server gets a stable route. |
| **Access and consent** | Grants and sessions are separate resources. They hold policy, side-effect allowances, trust ceilings, consent, expiry, and revocation, apart from the deployment YAML. |
| **Brokered rollout** | Servers run direct or behind the proxy sidecar. Rollout settings live on the same server resource. |

## Core resources

Three CRDs form the runtime surface: `MCPServer`, `MCPAccessGrant`, and `MCPAgentSession`.
Many grants and sessions can reference one `MCPServer`. With allow-list policy,
the gateway evaluates the matching grant and, when `session.required: true`,
the session on each tool call. Observe mode records calls without enforcing
those checks.

See the [API reference](api.md) for full field definitions and examples.

## Reconciliation outputs

For every `MCPServer`, the operator reconciles:

- **Deployment**: image, replicas, resource requests/limits, env, image-pull secrets.
- **Service**: ClusterIP exposing `spec.servicePort` → `spec.port`.
- **Ingress**: routes `spec.publicPathPrefix` as `/<prefix>/mcp`, or explicit `spec.ingressHost` + `spec.ingressPath`, to the Service with per-class annotations (Traefik / NGINX / Istio).
- **Policy ConfigMap**: rendered from the matching `MCPAccessGrant` + `MCPAgentSession` resources, consumed by the proxy sidecar when `gateway.enabled`.

`MCPServer.status` exposes:

- `phase`: `Pending` → `PartiallyReady` → `Ready`.
- `message`: human-readable progress.
- `conditions`: standard Kubernetes condition slice.
- Per-resource readiness booleans: `deploymentReady`, `serviceReady`, `ingressReady`, `gatewayReady`, `policyReady`, and `canaryReady` when a canary rollout is configured.
- `ingressReady` defaults to strict mode: the Ingress must publish `status.loadBalancer.ingress[]`. For dev or NodePort-style ingress controllers that route traffic without publishing load-balancer status, set operator env `MCP_INGRESS_READINESS_MODE=permissive`. Permissive mode treats an Ingress with rules as ready.

### Useful defaults

- Servers default to `/{server-name}/mcp`; set `spec.publicPathPrefix` to choose the public path prefix explicitly.
- Hostless path-based routing is supported through `spec.publicPathPrefix`; otherwise provide `spec.ingressHost` or configure the operator default host.
- Container port defaults to `8088`, service port to `80`.
- Gateway listens on `8091`.
- `setup` provisions `mcp-runtime` plus the active shared catalog namespace for
  shared modes: `mcp-servers-org` for `org` or `mcp-servers-public` for
  `public`.
- `tenant` mode uses team namespaces for the authenticated principal's team
  memberships. Place runtime CRDs in per-team namespaces and rely on Kubernetes
  RBAC and ingress watch configuration for isolation.
- Default ingress class is `traefik`; override via `spec.ingressClass`.

## Topology

```mermaid
flowchart TB
    Dev[Developer / CI] --> CLI[mcp-runtime CLI]
    CLI --> CRD[v1alpha1 surface<br/>MCPServer / MCPAccessGrant / MCPAgentSession]
    CRD --> Op[Operator + Registry + Ingress]
    Op --> K8s[Deployments / Services / Ingress]

    K8s -->|gateway disabled| Direct["/{server-name}/mcp → MCP server"]
    K8s -->|gateway enabled| Gateway[mcp-gateway sidecar]
    Gateway --> MCP[MCP server]
    Gateway --> Stack[Services Stack<br/>ingest / processor / API / UI / observability]
```

## Install and delivery flow

```mermaid
flowchart LR
    A[01. Initialize cluster<br/>cluster init / setup] --> B[02. Configure ingress + registry]
    B --> C[03. Describe servers<br/>MCPServer YAML]
    C --> D[04. Scaffold + publish<br/>server init, build, push, deploy]
    D --> E[05. Grant access<br/>grant init/apply; adapter sessions]
    E --> F[06. Observe<br/>status, UI, API]
```

| Step | Commands |
|---|---|
| Initialize cluster | `cluster init`, `setup`, `bootstrap` |
| Configure ingress + registry | `cluster config --ingress traefik`, `registry provision` |
| Describe servers | `server init`, hand-written `MCPServer` YAML, or metadata in `.mcp/` |
| Publish + deploy | `auth login`, `server build image`, `server push`, `server deploy`, `server generate` for GitOps YAML |
| Grant access | `auth login`, `access grant init`, `access grant apply`; sessions via `adapter proxy --server … --agent …` or admin `access session init/apply` |
| Observe | `status`, platform UI/API; admin: `sentinel status`, `sentinel port-forward ui` |

## Traffic and enforcement model

| Mode | Behavior |
|---|---|
| **Direct** | No `gateway.enabled`. Service points at the MCP server directly. Server is exposed at `/{server-name}/mcp`. |
| **Gateway** | `spec.gateway.enabled: true`. Traffic flows through the proxy sidecar, which handles identity, policy, audit, and telemetry. |
| **Trust evaluation** | At tool-call time, effective trust is `min(grant.maxTrust, session.consentedTrust)` and must meet the required trust, which is the higher of the tool's `requiredTrust` and the matching tool rule's `requiredTrust`. |
| **Side-effect evaluation** | Each listed tool must declare `sideEffect: read`, `write`, or `destructive`. A grant authorizes only tools whose side effect is in `allowedSideEffects`. Omitted or empty `allowedSideEffects` allows no side-effect classes. A tool the server never declared is denied, because it has no side effect to authorize. |
| **Observe mode** | `policy.mode: observe` allows the call before identity, session, grant, side-effect, and trust checks run. Requests are still proxied and audited, but nothing is enforced. |

### Gateway policy snapshots

The operator renders `MCPServer` + `MCPAccessGrant` + `MCPAgentSession` state
into a JSON policy ConfigMap that the gateway sidecar mounts and reloads. The
rendered document carries this metadata, separate from the authorization
`policyVersion`:

| Field | Meaning |
|---|---|
| `schema_version` | Compatibility of the rendered JSON contract. The gateway rejects any version it does not support. Documents are `v1` unless a grant sets `expiresAt`; those are `v2`, so a gateway built before grant expiry rejects them and keeps its last valid policy instead of ignoring the expiry. |
| `revision` | Deterministic `sha256:` digest of the canonical policy content. Identical content always yields the same revision. `generated_at` is excluded from the digest, so timestamps never change it. |
| `generated_at` | Informational only; set at write time and never affects `revision`. |

Both sides share `pkg/policy.Validate`: the operator validates a rendered
document **before** replacing the ConfigMap, and the gateway validates a decoded
document **before** activating it. Validation fails closed. It rejects unknown
trust, side-effect, decision, or policy-mode values, duplicate names,
and OAuth without an issuer.

Activation is **last-known-good**: a malformed, unsupported, or invalid update
never replaces the active snapshot. The gateway keeps serving the previous valid
policy and records the failure. Snapshot swaps are atomic, so each request sees
either the complete old policy or the complete new one.

To check the applied policy, use the gateway endpoints:

- `GET /health`: liveness (always OK while serving).
- `GET /ready`: readiness; fails until the first valid policy snapshot loads.
- `GET /config/status`: sanitized `schema_version`, `revision`, `loaded_at`,
  and `last_reload_error` (no policy body).
- `GET /metrics`: `mcp_gateway_policy_reload_total{result}`,
  `mcp_gateway_policy_active_revision_info{revision,schema_version}`, and
  `mcp_gateway_policy_last_success_timestamp_seconds`.

### Agent adapters

The agent adapter is a helper process for frameworks and IDEs. `mcp-runtime
adapter proxy` accepts local Streamable HTTP MCP traffic and authenticates to
the governed runtime route with a session-bound client certificate. The gateway
derives the enrolled session identity from the verified certificate. When the
target configures `spec.auth`, the adapter also forwards the OAuth bearer and
the gateway requires the token subject to match the session human.

With `--server <MCPServer name> --agent <id>`, the adapter creates or reuses an
`MCPAgentSession` through `POST /api/v1/runtime/adapter/sessions`, then submits
a CSR for a certificate whose SPIFFE URI identifies that session. The
platform issues the session only when a matching enabled `MCPAccessGrant`
allows it. `--auto-refresh` enrolls a replacement certificate before expiry
and rotates the TLS transport without restarting the process. `adapter enroll`
saves the PEM files under `MCP_RUNTIME_CONFIG_DIR/certs` (normally
`~/.mcpruntime/certs`).

Operational notes:

- The proxy exposes `/healthz`, `/livez`, `/readyz`, and an optional
  `/metrics` endpoint when wired with `ProxyConfig.MetricsHandler`.
- Idempotent reads (`tools/list`, `resources/list`, `prompts/list`, `ping`)
  retry on `502`/`504`/connection-reset; `tools/call` does not retry.
- Set `MCP_RUNTIME_LOG_LEVEL=info` on the adapter to print runtime 4xx
  denials to stderr.

See [Agent Adapters](agent-adapters.md) for build commands and full
integration examples.

## Operator internals (high-level)

The operator is a single-controller `controller-runtime` manager:

1. Watches `MCPServer` (and owns Deployment / Service / Ingress).
2. Defaults and validates new API writes through admission webhooks; on reconcile, applies the same defaults to an in-memory copy for legacy objects without patching the stored spec.
3. Resolves the image string (respecting `imageTag`, `registryOverride`, and `PROVISIONED_REGISTRY_URL`).
4. Builds image-pull secrets, including auto-creating a docker-config secret from provisioned-registry env vars.
5. Reconciles Deployment → Service → Ingress in order.
6. Computes per-resource readiness, sets phase and conditions, writes status.

Source walkthroughs live under [internals/cmd-operator.md](internals/cmd-operator.md).

## Current scope

Implemented and stable enough to evaluate:

- Deployment, routing, image pull, registry handling.
- Grants, sessions, gateway policy generation.
- Trust evaluation and audit-event flow.
- Multi-ingress class support (Traefik, NGINX, Istio, generic).
- OAuth: when `spec.auth` is configured, the gateway acts as an MCP protected
  resource. It publishes protected-resource metadata, validates the token
  issuer and resource audience, and forwards the validated bearer to the MCP
  application server for validation against the same issuer and audience.
  Tokens come either from the opt-in bundled `mcp-auth-server`
  (`setup --with-mcp-auth-server`, authorization code with S256 PKCE, refresh
  rotation, CIMD or DCR client registration) or from an external authorization
  server you operate. Accounts stay in your identity provider. The bundled
  server federates to one configured OIDC connector per process. Policy
  decisions stay in the gateway. See
  [MCP authorization](mcp-authorization.md).
- Adapter certificates: optional session-bound certificates are verified at
  ingress on gateway routes. The gateway derives the adapter session identity
  from the verified certificate and applies grant/session policy. Direct
  clients use OAuth only when `spec.auth` is present. Adapters always use the
  certificate and add OAuth for an OAuth-enabled target; the gateway then
  binds the certificate identity to the OAuth subject. Set
  `MCP_ADAPTER_CERTIFICATES=true` with platform `MCP_MTLS_CLUSTER_ISSUER` and
  `MCP_TRUST_DOMAIN` to enable enrollment.

Not yet:

- Multi-cluster federation.

## Next

- [CLI](cli.md): every command and flag.
- [API](api.md): full CRD reference with examples.
- [Sentinel](sentinel.md): what happens after traffic enters the gateway.
