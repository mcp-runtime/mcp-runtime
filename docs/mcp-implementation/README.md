# MCP implementation reference

This section is for MCP server and client implementers who need to diagnose
behavior across a request path. It assumes familiarity with JSON-RPC and MCP;
read [Session lifecycle](session-lifecycle.md) and [State model](state-model.md)
first, then use the topic pages as a reference. It describes operational
behavior at the MCP Runtime boundary and complements the MCP specification:
the upstream server remains authoritative for MCP transport sessions and tool
execution, while Runtime supplies routing, identity integration, and optional
per-tool governance.

| Topic | Reference |
|---|---|
| Session creation, expiry, disconnects | [Session lifecycle](session-lifecycle.md) |
| stdio, SSE, and Streamable HTTP | [Transports](transports.md) |
| Tool metadata, discovery, and changes | [Tool registration](tool-registration.md) |
| Adapter, gateway, and upstream credentials | [Auth propagation](auth-propagation.md) |
| Protocol, HTTP, and policy failures | [Error reference](error-reference.md) |
| Per-call, transport-session, and platform state | [State model](state-model.md) |

Runtime's public MCP endpoint is HTTP. Containerized servers must expose the
HTTP endpoint configured by their `MCPServer`; a local stdio process is not
itself a deployable Runtime endpoint. For product setup and client examples,
see [Client Connections](../connect-clients.md), [MCP OAuth](../mcp-oauth.md),
and the [API reference](../api-reference.md).

The references distinguish MCP transport state from Runtime governance state.
An MCP `Mcp-Session-Id` belongs to the upstream server. An `MCPAgentSession`
belongs to Runtime's access model and binds identity, consent, expiry, and
revocation. Creating, expiring, or reconnecting one does not create or restore
the other.
