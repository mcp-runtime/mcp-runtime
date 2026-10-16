# State model

Use the state owner to decide what survives a reconnect. Runtime does not
collapse protocol, authorization, and application state into one session.

| Scope | Owner and examples | Reconnect / restart behavior |
|---|---|---|
| Per request | JSON-RPC request ID, HTTP context, OAuth bearer, trace context, tool arguments | Applies to one exchange. Cancellation ends the proxy request; a lost response does not reveal whether an upstream side effect committed. |
| MCP transport session | Upstream server; optional `Mcp-Session-Id`, negotiated protocol version, server-side conversation state | Reuse the ID while the server accepts it. Runtime does not persist it. The server decides whether it survives process restart or replica changes. |
| Runtime agent session | Platform API/PostgreSQL record; human, agent, team, consented trust, expiry, revocation | Can span transport reconnects until it expires or is revoked. Gateway sync makes revocation effective according to the session-sync contract. |
| Adapter credential | Local adapter process; certificate bound to a Runtime agent session, or configured per-request credential source | Certificate can be renewed; renewal does not renew an MCP transport session or OAuth token. Local proxy restart may require restarting the client connection. |
| Server and policy | `MCPServer` declarations, `MCPAccessGrant`, rendered gateway snapshot | Reconciled declarations and policy are independent of an individual MCP connection. Gateways retain the last valid snapshot while a replacement is invalid. |
| Application data | MCP server and its backing stores | Survives only according to the server's persistence and transaction design. Runtime does not checkpoint or roll back tool effects. |

An `MCPAgentSession` is the authorization context used for a governed call;
`Mcp-Session-Id` is an upstream transport handle. They are not interchangeable.
The gateway's grant/session decision happens for each governed tool call, even
when the same transport session carries many calls. A transport reconnect does
not grant new access, and a valid transport ID does not override an expired or
revoked agent session.

For the persistence and revocation contract, see
[Gateway Session Sync](../contributor/gateway-session-sync.md). For policy
inputs and identity mapping, see [Identity and Authorization](../identity-and-authorization.md).

Source: [`services/mcp-gateway/session_sync.go`](https://github.com/mcp-runtime/mcp-runtime/blob/main/services/mcp-gateway/session_sync.go), [`internal/agentadapter/proxy.go`](https://github.com/mcp-runtime/mcp-runtime/blob/main/internal/agentadapter/proxy.go), and [`api/v1alpha1/access_types.go`](https://github.com/mcp-runtime/mcp-runtime/blob/main/api/v1alpha1/access_types.go).
