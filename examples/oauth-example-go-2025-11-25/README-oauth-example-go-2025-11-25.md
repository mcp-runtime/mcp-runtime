# oauth-example-go-2025-11-25

Go server using `github.com/modelcontextprotocol/go-sdk` v1.6.0 and the
`mcp-auth` Go SDK. Its highest supported MCP protocol revision is `2025-11-25`;
the Streamable HTTP handler uses the legacy initialize/session flow. The server
source supports direct OAuth token validation through the `mcp-auth` SDK or no
server-side auth. The checked-in metadata keeps the Runtime gateway enabled so
OAuth requests retain grants, sessions, policy, and audit enforcement.

To run locally, set the issuer, resource audience, and protected-resource
metadata URL for the authorization server you are using:

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
in OAuth mode, and reports anonymous status when no OAuth settings are present.
