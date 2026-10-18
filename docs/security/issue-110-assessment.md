# In-cluster communication audit (issue #110): current-source assessment

This maps each item of the historical in-cluster communication audit to the
current source. The original audit was static and sanitized; file and line
references in it are obsolete. This page records what each item was, whether it
is still valid, which behavior is a deliberate contract that was kept, and what
still needs live acceptance. It is source-only: no cluster, Kind, E2E or Staging
run backs it.

## Live proof on an enforcing CNI

NetworkPolicy isolation only exists on a CNI that enforces it (Kindnet does not).
The only live proof of the network boundaries is the **Cilium Scoped
Observability** CI job (`.github/workflows/scoped-observability-e2e.yaml`). It
becomes effective once the pull requests that repair and extend it (#800, #811;
#819 follows) merge and the job runs green. No other live proof exists: nothing
in this page, the unit tests or the contract tests demonstrates datapath
enforcement. Until that job is green on main, treat the network items below as
"source implemented, enforcement unproven".

## Item map

| Item | What it was | Current status |
|---|---|---|
| C1 gateway bypass | Upstream MCP servers trusted any pod; identity in plain headers | Gateway upstream defaults to pod loopback; the ingress-to-gateway identity assertion requires verified mTLS and is stripped before upstream. Per-MCPServer ingress policy isolates the raw upstream listener (#798). Signing forwarded identity headers is intentionally not done (they are no longer forwarded). Live enforcement: see above. |
| C2 no NetworkPolicy | No policies in repo | All-pod default-deny ingress per managed namespace with explicit peers; the generic namespace exception needs an API-managed marker and never applies to stamped MCPServer pods (#798). Live enforcement: see above. |
| C3 header-mode identity | Caller headers trusted as identity | Superseded. Header mode only delegates upstream credentials with an explicit allow-list and deny default; it never yields platform human/session identity. OAuth and certificate paths verify their own identity. |
| C4 only `tools/call` gated | Other methods bypass grants | Deliberate contract, see below. |
| C5 ingest fail-open | No auth source meant open intake | Fixed: startup refuses without an auth source (#792). |
| C6 gateway Secret grant | Cluster-wide secrets read | Removed; ingress uses namespace Roles. |
| H1 API ServiceAccount | Deployment CRUD, impersonation | Actively used; deliberate contract, see below. |
| H2 plaintext transport | Kafka PLAINTEXT, no TLS | Kafka mutual TLS, verified ClickHouse TLS and HTTPS ingest outside test mode (#796). Test mode keeps plaintext by design. Existing plaintext Kafka needs backup/restore migration; not live-tested as a quorum. |
| H3 registry auth | Unauthenticated registry | Forward-auth/native token auth and a registry NetworkPolicy exist. Runtime acceptance is separate. |
| H4 Grafana/Prometheus | Unauthenticated, placeholder password | Grafana goes through platform-session authorization; Prometheus is not a public proxy; scoped telemetry gated on verified Cilium. Live: see above. |
| H5 shared keys | Shared admin key model | Per-principal keys/JWT identity, short-lived per-audience exchange tokens; explicit admin keys only. Ingest attribution is bound to the verified Pod and its owning MCPServer (#802). Legacy opt-ins remain deliberate. |
| H6 replay/idempotency | No idempotency for tool calls | Optional `Idempotency-Key` contract, [ADR](../internals/gateway-idempotency.md) (#824). Per-sidecar only. |
| M1 namespace delete | Namespace/NetworkPolicy delete | Removed; no delete or deletecollection. |
| M2 operator Secret read | Cluster-wide secrets:get | Removed; per-namespace bindings. |
| M3 admin password env | Persisted in API env | Consumed by a bounded bootstrap Job. Retention of the install Secret is a separate operator choice. |
| M4 reload latency | 5 s policy reload | Still bounded polling (file policy 5 s, session sync 500 ms). A watch is not claimed. |
| M5 redactor headers | PII redactor keeps auth headers | Not a defect: upstream authentication needs them; no registry overlay references the dev redactor. |
| M6 JWT `exp` | Unverified | Verified in the pinned mcpauth verifier (`exp`, issuer, audience, signature, subject; 60 s default skew). |
| M7 bind 0.0.0.0 | Services listen on all interfaces | Mitigated only by NetworkPolicy plus auth; listener binding itself is unchanged. Live: see above. |
| M8 health leak | Initialization details in `/health` | Fixed: boolean/generic probe responses (#792). |
| L1 unauthenticated metrics | Metrics open | Gateway metrics restricted to Prometheus by policy (#798). Live: see above. |
| L2 forwarded-for rate key | Spoofable | Fixed: socket peer unless explicit trusted proxy CIDRs (#792). |
| L3 placeholder credentials | Example secrets | Setup rejects imported `changeme` placeholders outside test mode (#806). Only that known placeholder is rejected; this is not a general password-strength check. |
| L4 SA token mounts | Default automount | Disabled where no Kubernetes calls are made; kept, with narrow RBAC, where needed. |

## Deliberate contracts kept

### C4: non-tool methods are not grant-gated

The gateway authorizes `tools/call` against grants and sessions. Other
authenticated methods (`initialize`, `tools/list`, `resources/*`, `prompts/*`,
`ping`) are intentionally passed through after authentication, because they have
different authorization semantics per caller type:

- Adapter sessions: identity and session binding are verified, tool calls are
  grant-governed.
- Direct OAuth callers: the upstream MCP server validates the forwarded bearer
  token and owns resource and prompt authorization.
- Delegated-header servers: the upstream credential owns authorization; the
  gateway enforces only an explicit tool allow-list.
- Catalog/discovery: must keep working for initialization.

Inventing grant semantics for resources and prompts would be a new product
contract, and would break discovery. The historical finding is therefore
reclassified: unauthenticated access is not possible (authentication runs for
every request), but per-resource grants for non-tool methods do not exist. If
that is wanted it belongs to the MCP protocol compliance work (excluded issue
#662) and needs its own design. Planned, not built.

### H1: deployment CRUD and impersonation

The runtime API creates and updates Deployments and impersonates the
authenticated principal when talking to Kubernetes
(`DeploymentService.clientForPrincipal`). Reducing to patch-only or removing
impersonation would break supported server management. Scope was instead
narrowed by splitting platform-api from runtime-api, using namespace-scoped
Secret and Role bindings, and removing namespace deletion. Any further
reduction needs a per-verb review against the CLI/UI flows, not a blanket
removal. Planned, not built.

## Remaining acceptance, stated plainly

Source work for the audit is complete once #824 and this page merge. What cannot
be shown here and is documented as planned: live NetworkPolicy enforcement on a
Cilium cluster (the Cilium Scoped Observability job, once #800/#811 land green),
a replica-shared idempotency store, per-resource grants for non-tool methods,
and a watch-based policy reload.
