# Session lifecycle

There are two independent session lifecycles in a governed MCP request. The
upstream MCP server owns the transport session. MCP Runtime owns the optional
agent authorization session. They have different identifiers and failure
conditions.

## MCP transport session

For stateful Streamable HTTP, a client sends `initialize`; the server may return
`Mcp-Session-Id`. The client then includes that header on subsequent requests
for the same MCP session, including `notifications/initialized`. The server
defines when the ID expires or is terminated. A stateless server may omit it.
Runtime's gateway and adapter proxy the header; neither persists or renews the
upstream session.

If a server reports that a transport ID is no longer valid, start a fresh MCP
`initialize` exchange. Runtime does not translate that server-specific
response into a new session. A deployment restart may invalidate process-local
transport sessions; the server can preserve them only if it implements durable
state itself. Runtime does not guarantee that successive requests carrying a
session ID reach the same replica, so stateful servers must account for the
deployment's load-balancing behavior.

## Governed agent session

An `MCPAgentSession` represents delegated identity and consent, not an MCP
protocol connection. Platform API stores platform-managed sessions in
PostgreSQL and synchronizes their binding to gateways. CLI/UI operations create,
inspect, revoke, and renew these records; a required session is checked with a
matching grant on governed tool calls. Expiry or revocation causes a denial;
gateways learn platform session changes through the documented sync contract
(normally within the policy refresh window). See [gateway session sync](../contributor/gateway-session-sync.md).

The adapter's enrolled client certificate is bound to one `MCPAgentSession`.
Certificate renewal refreshes that credential binding; it does not restore an
expired upstream MCP session. Reconnecting the MCP client can establish a new
transport session while continuing to use the same still-valid agent session.

## Disconnects and in-flight calls

The gateway proxies the HTTP request context to the upstream server. When the
client disconnects or cancels the request, the proxy cancels its upstream
request as well. This is a cancellation signal, not a transaction rollback:
the server may already have committed a side effect, and Runtime cannot promise
that the work stopped at the exact point of disconnect. The adapter does not
retry `tools/call`, avoiding an automatic duplicate side effect. Design tools
to be idempotent where retries are needed and reconcile ambiguous outcomes at
the application layer.

Related implementation: [gateway request flow](../internals/request-flows.md),
[identity and authorization](../identity-and-authorization.md), and
[adapter behavior](../runtime-operations.md#agent-adapters).

Source: [`services/mcp-gateway/filter_auth.go`](https://github.com/mcp-runtime/mcp-runtime/blob/main/services/mcp-gateway/filter_auth.go), [`services/mcp-gateway/proxy.go`](https://github.com/mcp-runtime/mcp-runtime/blob/main/services/mcp-gateway/proxy.go), and [`internal/agentadapter/proxy.go`](https://github.com/mcp-runtime/mcp-runtime/blob/main/internal/agentadapter/proxy.go).
