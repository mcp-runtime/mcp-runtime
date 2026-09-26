---
name: mcp-runtime-governance
description: Apply and debug MCP Runtime access grants, agent sessions, gateway policy, and MCP JSON-RPC traffic with governance headers. Use when working on MCPAccessGrant, MCPAgentSession, adapter proxy/stdio, access CLI, platform API grant/session endpoints, or allow/deny tool calls.
---

# MCP Runtime — governance and MCP traffic

## Flows

| Path | Notes |
|------|--------|
| **UI** | Create/apply grants and sessions; toggle enable/revoke |
| **CLI (default)** | `mcp-runtime auth login --api-url <url>` → `agent create|list|...` and `access grant init` / `access grant apply --file …` |
| **Adapter (recommended for agents)** | `adapter stdio\|proxy --server <name> --agent <id> [--auto-refresh]` → `POST /api/v1/runtime/adapter/sessions` |
| **Admin kube fallback** | `kubectl apply -f` or `access … --use-kube` (bypasses platform auth and agent-directory checks) |

Session apply via platform API is **admin-only**. Adapters usually skip manual session apply.

## Rules (short)

- One namespace per team; `MCPServer.spec.teamID` and `SubjectRef.teamID` must match gateway identity fields exactly when set.
- Platform API rejects cross-namespace `serverRef` and shared-catalog writes for non-admins.
- Each `MCPServer.spec.tools[]` needs `sideEffect: read|write|destructive`; grants need explicit `allowedSideEffects` (empty = deny all classes).
- Managed agent IDs come from the team directory (`mcp-runtime agent create <team-slug> --name …`). New IDs use `agt_<26-character lowercase ULID>`; names can change, IDs cannot. Deactivation revokes active sessions.
- Agent subjects must use an active directory ID owned by the selected subject team. Unknown, malformed, inactive, and wrong-team IDs fail closed; the access forms do not accept free-text agent IDs.
- A cross-team grant names the subject's `teamID` and must expire; its TTL is capped by the runtime API. Audit fields distinguish the subject/actor team from the server/resource authority team.
- `server policy inspect` shows rendered policy; the operator stamps the policy revision on server pods so the gateway sees new grants/sessions within ~10s. Wait that long before assuming `session_not_found`.

## Example manifests

```yaml
apiVersion: mcpruntime.org/v1alpha1
kind: MCPAccessGrant
metadata:
  name: workspace-assistant-grant
  namespace: mcp-team-finance
spec:
  subject: {humanID: user-123, agentID: agt_01arz3ndektsv4rrffq69g5fav, teamID: team-finance-id}
  serverRef: {name: workspace-assistant-mcp, namespace: mcp-team-finance}
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
  serverRef: {name: workspace-assistant-mcp, namespace: mcp-team-finance}
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
BASE=http://localhost:18080/workspace-assistant-mcp/mcp
curl -sS -H "content-type: application/json" \
  -H "accept: application/json, text/event-stream" \
  -H "Mcp-Protocol-Version: $PROTO" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' -D - -o /dev/null "$BASE"
# Capture Mcp-Session-Id from response headers, then notifications/initialized and tools/call with -H "Mcp-Session-Id: <session>"
```

Kind e2e applies generated access YAML and exercises allow/deny over real MCP traffic — see `test/e2e/kind.sh` and `test/e2e/select_pr_scenarios.sh` (`governance`, `trust`, `adapter-proxy`).

## Code map

- CRDs: `api/v1alpha1/`, `config/crd/bases/`
- Shared policy: `pkg/access/`, `pkg/policy/`
- Adapters: `internal/cli/adapter/`, `internal/agentadapter/`, `services/runtime-api/internal/runtimeapi/adapter.go`
