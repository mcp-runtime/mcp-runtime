# Credential gateway ownership

The optional mcp-auth bundle now consolidates the existing credential plugin
into separate gateway/provider and composition modules in the mcp-auth
repository. The original onepassword-mcp-plugin repository is retained. Its
runtime protections are migrated, including per-request credential-access
assertions, mapping isolation, bounded cache, fixed upstream destinations,
header allowlists and bound upstream MCP sessions.

| Component | Owns |
| --- | --- |
| mcp-auth OAuth core | Verified identity, consent, PKCE, tokens, refresh and active sessions; generic resource pre-authorization check |
| Optional credential gateway | Private identity/service mappings, upstream validation, credential custody, header rendering, request authorization and proxying |
| 1Password provider | Service-account SDK or local desktop CLI access and complete `op://` grammar |
| Combined service | Runs OAuth and gateway listeners in one process/deployment; installs login checks |
| Runtime | Deploys an upstream MCP server and records its normal header-authentication contract |
| MCP client | OAuth and the gateway URL, without upstream credential headers |

`auth-server` and the language SDKs remain provider neutral. The optional
`credential-gateway` Go module contains the vendor dependency;
`combined-server` is the composition root. Adding a vault uses the resolver,
factory and reference-validator interface without changing OAuth protocol code.

For the current path, use [the header-only example](../vault-header-demo/README.md)
and [mcp-auth's gateway guide](https://github.com/tavric-labs/mcp-auth/blob/feat/upstream-credential-login/docs/credential-gateway.md).
This directory remains the earlier protocol fixture, whose opaque connection
principal and temporary credential memory are not the combined gateway's
stable-identity/session model. The corrected implementations remain unmerged
and undeployed pending review.

Hosted service-account access does not open a user's desktop. Desktop mode in
the optional provider requires a gateway on the same machine as the 1Password
app and CLI. A personal-desktop-to-hosted-gateway handoff still requires a local
connector protocol; the bundle does not provide that handoff. 1Password controls
unlock and approval reuse; a successful CLI read does not prove a fresh prompt.
