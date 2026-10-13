# oauth-example-go-2025-11-25

Go server using `github.com/modelcontextprotocol/go-sdk` v1.6.0 and the
`mcp-auth` Go SDK. Its highest supported MCP protocol revision is `2025-11-25`;
the Streamable HTTP handler uses the legacy initialize/session flow. The server
source supports direct OAuth token validation through the `mcp-auth` SDK or no
server-side auth. The checked-in metadata keeps the Runtime gateway enabled so
OAuth requests retain grants, sessions, policy, and audit enforcement.

Runtime derives the OAuth issuer and resource audience from the platform
configuration and public MCP URL. To run the standalone Go process locally,
provide the issuer and resource URL:

```bash
MCP_AUTH_ISSUER=http://localhost:18080/mcp-auth \
MCP_AUTH_RESOURCE=http://localhost:18080/oauth-example-go-2025-11-25-standalone/mcp \
go run .
```

Run with no server-side authentication by omitting both OAuth environment
variables. Runtime gateway policy and adapter identity remain separate.

The gateway-enabled server identity is `oauth-example-go-2025-11-25-gateway`.
The server metadata and its example grant/session manifests are in `.mcp/`.
When `gateway.enabled` is false, the operator passes OAuth issuer/resource
settings to the server, which validates tokens and serves protected-resource
metadata directly. This standalone mode bypasses Runtime gateway governance
and audit features. Without OAuth settings, the server accepts unauthenticated
requests. The standalone OAuth path uses `oauth-example-go-2025-11-25-standalone`.

The `whoami` tool returns the verified token subject, agent, session, and scopes
in standalone OAuth mode. With the gateway enabled, Runtime owns the verified
identity, governance, and audit; the server process has no OAuth settings and
`whoami` reports anonymous status.

## Governance

Any server example can use Runtime governance with its gateway enabled.
After deploying this example and logging in to the platform, apply its sample
grant and session from the repository root:

```bash
./bin/mcp-runtime access grant apply --file examples/oauth-example-go-2025-11-25/.mcp/grant-example-agent.yaml
./bin/mcp-runtime access session apply --file examples/oauth-example-go-2025-11-25/.mcp/session-example-agent.yaml
```

The samples grant `example-agent` / `example-human` access to `aaa-ping` and
`whoami` at low trust with read-only side effects. Use the matching issued
identity and session when connecting through an adapter; see the
[adapter guide](../../docs/connect-clients.md). Change the grant and session to
exercise allow/deny policy on the same server, without another server fixture.

## Request tracing

Set `OTEL_EXPORTER_OTLP_ENDPOINT` to the collector's HTTP base URL (for example
`http://otel-collector.mcp-observability.svc.cluster.local:4318`) and optionally
`OTEL_SERVICE_NAME` / `OTEL_RESOURCE_ATTRIBUTES`. The server accepts W3C
`traceparent`, emits HTTP and MCP method/tool execution spans and marks both
protocol errors and `CallToolResult.IsError` as failures. Completion logs carry
trace/span IDs. Health probes are excluded. Arguments, results, bearer tokens
and identity claims are not captured. Without an exporter the server still
accepts propagation; it does not export recorded spans.

Third-party servers can use the same pattern: instrument the HTTP handler,
inherit its context in SDK method middleware, instrument external API calls,
and export to the configured collector. Keep the external API work inside the
method span and the asynchronous analytics tail separate from client latency.
