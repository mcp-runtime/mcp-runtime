# Tool registration and metadata

Runtime separates what an MCP server reports at `tools/list` from the metadata
used to govern `tools/call`.

## Declaring tools

The server manifest and resulting `MCPServer.spec.tools` declare tool names,
required trust, and side-effect class. These declarations are the policy
metadata used by the operator to render gateway policy. The gateway evaluates
the requested tool name against that snapshot, grant rules, trust, and allowed
side effects before forwarding a call. Unknown or undeclared tool metadata is
not inferred from a live `tools/list` response.

`mcp-runtime server init` can discover tool names from a running server by
performing `initialize`, `notifications/initialized`, and `tools/list`.
Discovery is a convenience for creating metadata; review the generated names
and explicitly classify side effects and required trust before deployment.
Discovery errors are returned to the CLI caller, not converted into an empty
registry.

## Runtime inventory and schema changes

The runtime API also probes live protocol inventory for the UI. This cached
inventory supports operational inspection and comparison; it does not replace
the declared tool policy and does not automatically authorize newly reported
tools. `tools/list` requests pass through the gateway and are not subject to
grant/session authorization; `tools/call` is the governed operation.

If a server adds, removes, or changes a tool schema, update the server
implementation and its Runtime metadata together, then deploy and verify the
resulting inventory. The gateway authorizes using the declared tool name and
policy metadata; it does not validate the server's argument schema or inspect
implementation behavior. The server's MCP implementation validates arguments
and returns protocol errors or tool results. A mismatch can therefore appear
as a successful `tools/list` followed by a denied call, a server-side invalid
argument response, or a tool-level error result.

After changing metadata, wait for the operator to reconcile and the gateway to
load its new policy revision. A policy reload failure keeps the last known-good
snapshot active and is reported by gateway status/metrics; it does not silently
replace that snapshot with a partial policy.

Related: [Server Publishing](../publish-mcp-server.md),
[MCPServer API](../api-reference.md#mcpserver), and
[runtime inventory](../platform-services.md).

Source: [`pkg/policy/evaluator.go`](https://github.com/mcp-runtime/mcp-runtime/blob/main/pkg/policy/evaluator.go), [`services/runtime-api/internal/runtimeapi/live_inventory.go`](https://github.com/mcp-runtime/mcp-runtime/blob/main/services/runtime-api/internal/runtimeapi/live_inventory.go), and [`internal/cli/server/sync_tools.go`](https://github.com/mcp-runtime/mcp-runtime/blob/main/internal/cli/server/sync_tools.go).
