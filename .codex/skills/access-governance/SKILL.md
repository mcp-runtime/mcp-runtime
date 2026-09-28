---
name: access-governance
description: Apply and debug MCP Runtime access grants, agent sessions, gateway policy, and MCP JSON-RPC traffic. Use when working on MCPAccessGrant, MCPAgentSession, the OAuth plus certificate HTTP adapter proxy, access CLI, platform API grant/session endpoints, or allow/deny tool calls.
---

# Access Governance

## Flows

| Path | Notes |
|------|--------|
| **UI** | Create/apply grants and sessions; toggle enable/revoke |
| **CLI (default)** | `mcp-runtime auth login --api-url <url>` → `agent create|list|...` and `access grant init` / `access grant apply --file …` |
| **Adapter (recommended for agents)** | `adapter proxy --server <name> --agent <id> [--auto-refresh]` → certificate-backed `MCPAgentSession`; add OAuth only when the server configures it |
| **Explicit Kubernetes test/recovery** | `access … --use-kube` only when that path is explicitly requested; never bypass a failed CLI/UI flow |

Session apply via platform API is **admin-only**. Adapters usually skip manual session apply.

## Rules (short)

- One namespace per team; `MCPServer.spec.teamID` and `SubjectRef.teamID` must match gateway identity fields exactly when set.
- Platform API rejects cross-namespace `serverRef` and shared-catalog writes for non-admins.
- Each `MCPServer.spec.tools[]` needs `sideEffect: read|write|destructive`; grants need explicit `allowedSideEffects` (empty = deny all classes).
- Managed agent IDs come from the team directory (`mcp-runtime agent create <team-slug> --name …`). New IDs use `agt_<26-character lowercase ULID>`; names can change, IDs cannot. Deactivation revokes active sessions.
- Agent subjects must use an active directory ID owned by the selected subject team. Unknown, malformed, inactive, and wrong-team IDs fail closed; the access forms do not accept free-text agent IDs.
- A cross-team grant names the subject's `teamID` and must expire; its TTL is capped by the runtime API. Audit fields distinguish the subject/actor team from the server/resource authority team.
- `server policy inspect` shows rendered policy; the operator stamps the policy revision on server pods so the gateway sees new grants/sessions within ~10s. Wait that long before assuming `session_not_found`.
- OAuth is optional and is enabled by `MCPServer.spec.auth`. Direct clients use a bearer only on OAuth-enabled servers. An adapter always presents its enrolled certificate and adds a bearer only when the target enables OAuth.
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

## MCP JSON-RPC (local Kind, port-forward 18080)

```bash
PROTO=2025-06-18
BASE=http://localhost:18080/oauth-example-go-2025-11-25-gateway/mcp
curl -sS -H "content-type: application/json" \
  -H "accept: application/json, text/event-stream" \
  -H "Authorization: Bearer $MCP_ACCESS_TOKEN" \
  -H "Mcp-Protocol-Version: $PROTO" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' -D - -o /dev/null "$BASE"
# Capture Mcp-Session-Id from response headers, then notifications/initialized and tools/call with -H "Mcp-Session-Id: <session>"
```

QA E2E applies generated access YAML and exercises allow/deny over real MCP
traffic — run it with the explicit contributor kubeconfig, for example
`KUBECONFIG="$HOME/.kube/test-mcp-runtime-config" E2E_SCENARIOS=governance,trust,adapter-proxy bash test/e2e/qa-e2e.sh`.
See `test/e2e/qa-e2e.sh` and `test/e2e/select_pr_scenarios.sh` for scenario names.

## Code map

- CRDs: `api/v1alpha1/`, `config/crd/bases/`
- Shared policy: `pkg/access/`, `pkg/policy/`
- Adapters: `internal/cli/adapter/`, `internal/agentadapter/`, `services/runtime-api/internal/runtimeapi/adapter.go`
