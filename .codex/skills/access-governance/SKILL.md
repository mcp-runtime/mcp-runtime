---
name: access-governance
description: Apply and debug MCP Runtime access grants, agent sessions, gateway policy, and MCP JSON-RPC traffic with session-bound SPIFFE adapter certificates. Use when working on MCPAccessGrant, MCPAgentSession, adapter proxy, access CLI, platform API grant/session endpoints, or allow/deny tool calls.
---

# Access Governance

## Flows

| Path | Notes |
|------|--------|
| **UI** | Create/apply grants and sessions; toggle enable/revoke |
| **CLI (default)** | `mcp-runtime auth login --api-url <url>` → `agent create|list|...` and `access grant init` / `access grant apply --file …` |
| **Adapter (recommended for agents)** | `adapter proxy --server <name> --agent <id> [--auto-refresh]` → certificate-backed platform session; add OAuth only when the server configures it |
| **Optional header adapter** | `adapter proxy --auth-mode header --runtime-url https://… --credential-header-env Name=ENV` injects client-local credential headers and does not enroll a certificate. Direct clients can send the same headers without the adapter. |
| **Explicit Kubernetes test/recovery** | `access … --use-kube` only when that path is explicitly requested; never bypass a failed CLI/UI flow |

Session apply via platform API is **admin-only**. Adapters usually skip manual session apply.

Adapter enrollment preflight reads the deployed operator's on/off choice and
ConfigMap-backed configuration, compares runtime issuer/trust domain, and follows
the selected route's actual TLSOption. Unreadable/Secret-backed configuration,
stale route ownership/readiness and missing client-certificate verification fail
closed. `update` carries read-only preflight RBAC; do not enable certificates by
hardcoding a runtime flag. Enrollment completion and pending cleanup use conditional
platform-store mutations, never a full session upsert that could undo revocation.

## Rules (short)

- For disposable human test identities, use secure `team user create`, enroll
  as that user, then sign in as an admin and run `user deactivate <id> --yes
  --json`. Cleanup removes memberships and revokes stored keys/human sessions
  atomically with audit, while preserving an identity tombstone. Transfer sole
  team ownership first. Repeated cleanup is idempotent. Existing gateway sync
  and stale-cache limits still apply; verify denied calls after sync rather
  than equating a committed revocation count with every gateway's acknowledgement.
- Workload issuer admission: `hack/qa/approval-policy-rehearsal.sh` uses a private,
  disposable Kind cluster; it refuses existing names and tests requester RBAC,
  exact session identities, hidden SAN types, public/internal TLS and reissue.
  Reviewed pins (2026-10-17): cert-manager v1.21.2, approver-policy v0.28.0,
  Kubernetes 1.36.4. Follow `docs/connect-clients.md` rollout/rollback order;
  passing direct CSR tests does not prove supported CLI enrollment.

- One namespace per team; `MCPServer.spec.teamID` and `SubjectRef.teamID` must match gateway identity fields exactly when set.
- Platform API rejects cross-namespace `serverRef` and shared-catalog writes for non-admins.
- Each `MCPServer.spec.tools[]` needs `sideEffect: read|write|destructive`; grants need explicit `allowedSideEffects` (empty = deny all classes).
- Grant mode defaults to `enforce`. Use `access grant init --mode monitor` and platform apply, or select Monitor in the dashboard, to shadow tool/trust/side-effect denials. Identity/session/expiry checks and matching enforcing-grant denials still block. Dashboard mode switches PATCH only `mode`, preserving restrictions; deploy schema-v5-capable gateways first. Audit `would_deny` and `would_deny_reason` and the bounded gateway counter expose shadow outcomes.
- Managed agent IDs come from the team directory (`mcp-runtime agent create <team-slug> --name …`). New IDs use `agt_<26-character lowercase ULID>`; names can change, IDs cannot. Deactivation revokes active sessions.
- Agent details show scoped lifecycle counts and exact recorded authorizing grants. Deactivation previews visible sessions and re-reads final state even after errors; never infer unknown grant or revocation metadata. Admin Gateway audit links use recorded namespace/name pairs and detail activity is a capped seven-day window (see `docs/connect-clients.md`).
- Agent subjects must use an active directory ID owned by the selected subject team. Unknown, malformed, inactive, and wrong-team IDs fail closed; the access forms do not accept free-text agent IDs.
- A cross-team grant names the subject's `teamID` and must expire; its TTL is capped by the runtime API. Audit fields distinguish the subject/actor team from the server/resource authority team.
- For foreign recipients in the UI, use **Delegate to another team** and look up its exact slug. `/runtime/delegation-recipients` requires management rights on the resource namespace before returning paginated active agents and stable member IDs. Revoke grant sessions through the supported action; it atomically covers all explicitly linked platform namespaces. `team_id` remains resource authority; `subject_team_id` is the separately recorded actor.
- `server policy inspect` shows rendered grants/tools, not session state. Platform-api owns sessions in Postgres; gateway sync polls at 500ms on the dedicated port 8085. Cold or known-reset caches deny; disconnected caches expire after at most 10m. See [the session sync contract](../../../docs/contributor/gateway-session-sync.md).
- `MCPAgentSession` YAML is an input format for admin platform apply. Inspect, create, revoke and delete sessions through the platform CLI/UI; direct `--use-kube` session management is rejected and Kubernetes edits do not change gateway authorization.
- CLI `server delete` and UI Retire disable grants and revoke sessions before removing exact server-reference authorization across namespaces. Cleanup failures require retry; `mcpruntime.org/retiring` blocks issuance/redeployment while recovery completes. Admins can repeat deletion for an absent server to clean orphaned access. Shared pull credentials and audit history remain.
- New grants/sessions bind to `mcpruntime.org/server-uid`; policy rendering rejects a binding from an earlier same-name deployment. Explicit `server delete --use-kube` removes only the Kubernetes server object and does not perform platform access retirement.
- OAuth is optional and is enabled by `MCPServer.spec.auth` without `mode: header`. Direct clients use a bearer only on OAuth-enabled servers. An adapter always presents its enrolled certificate and adds a bearer only when the target enables OAuth.
- Diagnose PKI with `cluster identity status --server <namespace>/<name>`. Resolve the selected containers' declared ConfigMap sources and the selected route's actual TLSOption; unreadable configuration must fail rather than appear disabled. Read only public certificate fields from Secrets.
- `auth.mode: header` forwards every configured credential header unchanged for the upstream server to authenticate. The gateway enforces allow-list tool rules and default deny, and it does not derive a Runtime identity from those headers. Grants, sessions, and observation mode are rejected on this path.
- When an adapter calls an OAuth-enabled server, the gateway requires the token subject to equal the session human and forwards the validated bearer to the same logical MCP resource server.
- The upstream MCP application validates the same issuer and audience and must never forward this token to a third-party API.

## Example manifests

```yaml
apiVersion: mcpruntime.org/v1alpha1
kind: MCPAccessGrant
metadata:
  name: workspace-assistant-grant
  namespace: mcp-team-finance
spec:
  subject: {humanID: user-123, agentID: agt_01arz3ndektsv4rrffq69g5fav, teamID: team-finance-id}
  serverRef: {name: oauth-example-go-2025-11-25-gateway, namespace: mcp-team-finance}
  maxTrust: high
  allowedSideEffects: [read]
  toolRules:
    - {name: add, decision: allow, requiredTrust: low}
---
apiVersion: mcpruntime.org/v1alpha1
kind: MCPAgentSession
metadata:
  name: sess-ops-agent
  namespace: mcp-team-finance
spec:
  subject: {humanID: user-123, agentID: agt_01arz3ndektsv4rrffq69g5fav, teamID: team-finance-id}
  serverRef: {name: oauth-example-go-2025-11-25-gateway, namespace: mcp-team-finance}
  consentedTrust: high
  policyVersion: v1
```

## HTTP API (admin `x-api-key`)

- `POST /api/v1/runtime/grants`, `POST /api/v1/runtime/sessions` — create
- `GET|DELETE|PATCH /api/v1/runtime/grants/{ns}/{name}` — `PATCH {"disabled": true|false}` is the current toggle
- `GET|DELETE|PATCH /api/v1/runtime/sessions/{ns}/{name}` — `PATCH {"revoked": true|false}` is the current toggle
- `POST /api/v1/runtime/grants/{ns}/{name}/revoke-sessions` — revoke every linked session and retain the grant; `mcp-runtime access grant revoke-sessions` is the CLI entry point
- `POST .../grants/{ns}/{name}/enable|disable` and `POST .../sessions/{ns}/{name}/revoke|unrevoke` still work but are marked legacy in the handler comments (`services/runtime-api/internal/runtimeapi/grants.go`, `sessions.go`) — prefer PATCH for new callers

## MCP JSON-RPC (local Kind)

Port-forward both Traefik ports: `18080:8000` (HTTP / plain Ingress) and
`18443:8443` (HTTPS / adapter-certificate IngressRoute). Cert-capable gateway
routes are websecure-only; HTTP `:18080` will not reach them.

```bash
PROTO=2025-06-18
# Plain Ingress + OAuth bearer (samples that still publish Ingress):
BASE=http://localhost:18080/oauth-example-go-2025-11-25-gateway/mcp
curl -sS -H "content-type: application/json" \
  -H "accept: application/json, text/event-stream" \
  -H "Authorization: Bearer $MCP_ACCESS_TOKEN" \
  -H "Mcp-Protocol-Version: $PROTO" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' -D - -o /dev/null "$BASE"
# Capture Mcp-Session-Id from response headers, then notifications/initialized and tools/call with -H "Mcp-Session-Id: <session>"

# Adapter-certificate route (omit spec.auth on the MCPServer for cert-only):
CERT_BASE=https://127.0.0.1:18443/<publicPathPrefix>/mcp
curl -skS --cert ~/.mcpruntime/certs/<scope>/client.crt \
  --key ~/.mcpruntime/certs/<scope>/client.key \
  --cacert ~/.mcpruntime/certs/<scope>/ca.crt \
  -H "content-type: application/json" \
  -H "accept: application/json, text/event-stream" \
  -H "Mcp-Protocol-Version: $PROTO" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' -D - -o /dev/null "$CERT_BASE"
```

QA E2E applies generated access YAML and exercises allow/deny over real MCP
traffic — run it with the explicit contributor kubeconfig, for example
`KUBECONFIG="$HOME/.kube/test-mcp-runtime-config" E2E_SCENARIOS=governance,trust,adapter-proxy bash test/e2e/qa-e2e.sh`.
See `test/e2e/qa-e2e.sh` and `test/e2e/select_pr_scenarios.sh` for scenario names.

## Code map

- CRDs: `api/v1alpha1/`, `config/crd/bases/`
- Shared policy: `pkg/access/`, `pkg/policy/`
- Adapters: `internal/cli/adapter/`, `internal/agentadapter/`, `services/runtime-api/internal/runtimeapi/adapter.go`
