# Authentication and identity propagation

The client-to-server path can carry an adapter certificate, an OAuth bearer,
or both. These credentials answer different questions and are checked at
different boundaries.

## Adapter certificate

The adapter enrolls a certificate bound to one `MCPAgentSession` and presents
it to the HTTPS route. Ingress verifies the certificate and passes a trusted
SPIFFE identity assertion over its authenticated hop to the gateway. The
gateway resolves that binding to the human, agent, team, and session. It does
not trust caller-supplied governance identity headers in this mode. The
certificate establishes Runtime caller identity; it is not an OAuth access
token for the upstream MCP server.

## OAuth bearer

For a server with `spec.auth`, the client sends `Authorization: Bearer …` on
the MCP HTTP request. The adapter forwards that token with the request. The
gateway validates issuer, signature, audience, expiry, and required scope; on
an adapter route it also checks that the OAuth subject matches the certificate
session's human identity. The gateway then forwards the bearer to the
upstream, which remains the protected resource and should validate the token
for itself. A valid token does not bypass Runtime grants, sessions, trust, or
tool policy.

The optional mcp-auth server issues resource-bound tokens after login through
the configured identity provider. It does not make governance decisions. See
[MCP OAuth](../mcp-oauth.md) for issuer, audience, metadata, and provider
configuration.

## Failure boundaries

Missing, expired, invalid, or wrong-audience OAuth credentials are rejected at
the gateway, usually with `401` and a `WWW-Authenticate` challenge. A token
whose subject conflicts with the adapter session is also rejected. If the
gateway accepts a token but the upstream rejects it, the upstream response is
returned through the proxy; successful gateway authentication is not proof
that the upstream accepted its own authorization rules.

Header-auth mode is a separate local-credential integration. It validates the
configured header-presence contract and applies its configured delegated
tool rules. Do not treat arbitrary identity headers as a substitute for the
verified certificate or an OAuth token.

Credentials should be sent only over the configured HTTPS route. Never put
tokens in URLs, logs, issue comments, or server arguments. The adapter resolves
configured credential sources locally for each request and targets the
configured HTTPS Runtime route.

Source: [`services/mcp-gateway/filter_auth.go`](https://github.com/mcp-runtime/mcp-runtime/blob/main/services/mcp-gateway/filter_auth.go), [`services/mcp-gateway/oauth.go`](https://github.com/mcp-runtime/mcp-runtime/blob/main/services/mcp-gateway/oauth.go), and [`internal/agentadapter/credential_headers.go`](https://github.com/mcp-runtime/mcp-runtime/blob/main/internal/agentadapter/credential_headers.go).
