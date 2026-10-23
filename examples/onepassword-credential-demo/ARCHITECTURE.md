# Recommended production boundary — for review

The existing [onepassword-mcp-plugin](https://github.com/tavric-labs/onepassword-mcp-plugin)
is the credential gateway. Extend that implementation for production instead
of growing another gateway in Runtime or moving its vault access into the
`mcp-auth` server. The code in this example is a protocol fixture for testing
login-before-token ordering and header injection; it is not a replacement for
the plugin's security and session model.

## Ownership

| Component | Owns |
|---|---|
| mcp-auth server | OAuth consent, PKCE, client registration, login routing, scopes, token issuance/refresh and session revocation |
| mcp-auth SDK | Provider-neutral verification/exchange and reusable session-binding/cache primitives |
| Credential plugin/gateway | Upstream validation, credential custody, approved identity/service bindings, header transformation, fixed-destination proxying and per-request authorization |
| 1Password implementation | SDK/CLI access, native unlock/approval, account selection and `op://` reference resolution |
| Runtime | Deployment, resource metadata and an example upstream MCP server that still expects its normal authentication headers |
| MCP client | Starting OAuth and sending the issued token; it does not carry upstream header values in its configuration |

The broker must not import 1Password/Vault SDKs, hold vault service-account
tokens, retain raw upstream passwords, know `op://` semantics or implement
header injection. Adding another vault implementation must not require adding
that vendor to the authorization-server binary.

The current Auth PR adds generic resource-specific login policy and fixes
plain OAuth connector configuration. Those changes do not implement 1Password
access. Shared `SessionBinder` and `SessionBoundCache` already exist in the
auth Go SDK; adopt those in resource-side code where appropriate rather than
moving the plugin into the authorization server.

## How an installation should be used

An operator deploys `mcp-auth` and the credential plugin. The plugin's service
configuration fixes the upstream URL and required header transforms. Private
user mappings select the approved vault item/fields; actual secret values
stay in the vault or an explicitly bounded plugin cache. The broker registers
the plugin's resource, scopes and generic login/credential-approval integration.
Runtime deploys the upstream and exposes the plugin's public MCP URL.

A user stores the service Login in 1Password and adds only the plugin URL to
their MCP client. OAuth starts; any required user login and credential-provider
approval take place. The plugin validates the resolved credentials with the
actual upstream. Only successful validation completes the MCP authorization.
Later calls carry the MCP OAuth token to the plugin, which authorizes the
request, resolves/reuses the approved credentials and injects the headers.

The broker sees the identity/approval/session binding it needs, not the
upstream header values. A vault unlock alone is not proof that credentials
work at the upstream service, nor does a vault service account authenticate
the human using the MCP client.

## Desktop and hosted provider modes

- **Hosted automation:** a plugin service account reads explicitly allowed
  shared/organization vaults. Human identity comes from the broker's login
  provider. A service-account read does not show a user's desktop unlock prompt.
- **Personal desktop:** a local 1Password component requests native approval
  and supplies the approved credential connection to the gateway. A remote
  container cannot invoke a user's desktop app without that local component.
  Password prompts and cached unlocks remain controlled by 1Password.

These are explicit deployment modes, not interchangeable authentication
settings. The existing plugin currently implements the service-account
resolver; its personal desktop/login bootstrap needs additional work.

## Reuse and next changes

1. Keep the generic broker changes small and review them independently.
2. Add the login/credential-approval bootstrap and desktop provider mode to
   the existing plugin, retaining its header allowlist, fixed target,
   per-request active-session check and upstream MCP session binding.
3. Keep a header-authenticated upstream and an integration harness in Runtime;
   use the real plugin in the final end-to-end deployment.
4. When a second vault is needed, implement it behind the credential
   provider/resolver boundary on the gateway side. Extract a provider-neutral
   gateway module only when there is an actual second implementation to share it.

The precise identity/credential-session handoff needs cross-repository review.
This fixture uses an opaque connection principal and a bounded memory store;
production must preserve stable verified identity and the plugin's existing
revocation and cross-user/session protections. The fixture does not establish
that this production integration is complete.
