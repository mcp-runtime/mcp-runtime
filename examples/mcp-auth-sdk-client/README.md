# mcp-auth SDK client check

This is intentionally separate from setup and uses the Go client SDK from
[`Agent-Hellboy/mcp-auth`](https://github.com/Agent-Hellboy/mcp-auth/tree/main/auth-client/go).

After deploying the auth server and the shipped SDK resource example
`examples/oauth-example-go-2025-11-25/.mcp/servers.yaml`, obtain a
development token from the auth server's local authorization flow, then run:

```bash
cd examples/mcp-auth-sdk-client
go run .
```

with `MCP_AUTH_ISSUER`, `MCP_AUTH_RESOURCE`, and `MCP_AUTH_ACCESS_TOKEN` set.
Also set `MCP_SERVER_URL` to the deployed example endpoint to verify an
authenticated MCP `initialize` request. The verifier discovers JWKS through
authorization-server metadata and fails closed on issuer, audience, expiry,
signature, or algorithm mismatch.
