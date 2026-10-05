# Server and Client Guides

These guides are for server publishers, team administrators, and people
connecting MCP clients to a running platform. If you still need a platform,
start with [Getting Started](start-here.md).

## From a server to a tool call

1. [Server Publishing](publish-mcp-server.md): discover tool metadata, validate
   it, build and push an image, deploy the server, and confirm readiness.
2. [Teams and Access](teams-and-access.md): understand server ownership,
   namespace isolation, and grants, including cross-team delegation. A server
   owner or authorized administrator must grant the intended caller access.
3. [Client Connections](connect-clients.md): connect through the adapter using
   the authorized agent and its platform-issued session certificate.

A ready server is the deployment milestone. An allowed tool call also needs
verified caller identity and policy that permits the requested tool. Read
[Identity and Authorization](identity-and-authorization.md) when deciding which
human, agent, team, grant, and session should take part.

## OAuth-enabled servers

[MCP OAuth](mcp-oauth.md) explains the optional authorization server, identity
provider, resource audience, and token flow. Configure it before connecting to
a server that requires OAuth. The bearer token and adapter certificate have
separate roles; the client guide explains how to forward both.

## Verify and troubleshoot

Use [Services and Observability](platform-services.md) to inspect gateway
decisions and audit events. For a failed deployment or tool call, use
[Troubleshooting](troubleshooting.md), then consult [CLI and API Reference](reference-overview.md)
for flags, resource fields, or endpoint requirements.
