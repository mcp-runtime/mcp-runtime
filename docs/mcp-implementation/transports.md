# MCP transports

Transport describes how JSON-RPC messages move between a client and server.
Runtime's public server route is HTTP; it does not launch an arbitrary local
stdio process on behalf of a client.

| Transport | Connection state | Reconnect behavior | Runtime boundary |
|---|---|---|---|
| stdio | The client and server share a local process pipe. There is no HTTP connection or `Mcp-Session-Id` header. | Restarting either process ends the pipe; the client starts the server again and initializes again. | Use stdio for local tools or place an HTTP-speaking server in the deployed container. The Runtime adapter itself accepts local Streamable HTTP. |
| Streamable HTTP | Each JSON-RPC exchange is an HTTP request. A stateful server may issue `Mcp-Session-Id`; an event stream may carry a response. | Reuse the server-issued session header while it remains valid. On an expired/unknown session, initialize a new one. | This is the Runtime client and server route model. Ingress, gateway, and adapter proxy HTTP requests and response streams. |
| HTTP + SSE (legacy) | Older implementations use a long-lived GET event stream and a separate POST message endpoint. Session and event IDs are defined by that server transport. | Reconnect/event replay is server-specific; do not assume a new stream restores lost events or in-flight calls. | Runtime is not an SSE session store or replay broker. The CLI's discovery helper can decode an SSE response, but that does not make Runtime responsible for SSE session state. |

## Streaming, timeouts, and backpressure

The gateway uses an HTTP reverse proxy with a 30-second response-header
timeout. That timeout bounds waiting for the upstream to begin a response; it is
not a maximum duration for a response body or a tool operation. After headers,
HTTP streaming follows the connection and client context. The adapter's
outbound request timeout is configured separately.

Backpressure is provided by the HTTP stream and its readers/writers. Runtime
does not buffer an entire MCP event stream for replay. A slow client can hold
open an upstream response; capacity planning should include concurrent
long-lived streams and the server's own limits. A client disconnect cancels
the proxy request, but cannot undo work already committed by the server.

Runtime does not promise session affinity across replicas. A server that keeps
transport state only in process memory should use a single replica or provide
shared/durable session state and handle requests arriving at another replica.

Related: [Client Connections](../connect-clients.md),
[request flow](../internals/request-flows.md), and
[Runtime Operations](../runtime-operations.md).

Source: [`services/mcp-gateway/proxy.go`](https://github.com/mcp-runtime/mcp-runtime/blob/main/services/mcp-gateway/proxy.go), [`internal/agentadapter/transport.go`](https://github.com/mcp-runtime/mcp-runtime/blob/main/internal/agentadapter/transport.go), and [`internal/cli/server/sync_tools.go`](https://github.com/mcp-runtime/mcp-runtime/blob/main/internal/cli/server/sync_tools.go).
