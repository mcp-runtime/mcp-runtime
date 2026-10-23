# Validation — login-first implementation

The corrected flow validates upstream credentials before the MCP client gets
its OAuth token. It replaces the original post-login connection experiment.
The updated code is in Runtime PR #856 and depends on Auth PR #35.

## Current checks

- Authorization-server race tests and vet cover resource-specific login
  selection, default-method preservation, direct-method bypass, callback
  bypass, resource configuration and plain OAuth without fake OIDC metadata.
- Example tests cover the pre-token login form, wrong credentials producing no
  grant, cookie/CSRF/origin binding, PKCE verification and one-use codes.
- Gateway protocol tests verify Basic headers on initialize and tool calls,
  OAuth signature/audience/expiry enforcement, opaque connection isolation,
  credential-free responses, OAuth-token refresh reuse and cache expiry.
- Desktop-adapter tests use a fake `op` executable and verified local TLS.
  They check that the Login is read during the owned cookie-bound OAuth flow,
  inherited automation credentials are removed, external redirects are
  rejected, and consent requires explicit opt-in.

The loopback acceptance script passed on 2026-10-23 using the real modified
auth-server and gateway binaries. It verified that wrong credentials produce
no OAuth grant; initialize, tools/list and two tool calls succeed with OAuth
only from the client; a refreshed token reuses the retained upstream Login;
and a second client must complete a new upstream sign-in and gets a separate
credential identity. Tokens stayed in memory. No real vault or production
server was used by that script.

## Production status

Per the review request, these PRs remain unmerged. No new `mcp-auth` release
has been published and this login-first implementation has not been deployed.
The public endpoint may still serve the earlier `demo-20261023-5` experiment.
That earlier experiment passed credential delivery/reuse, but it issued OAuth
before connecting credentials, and did not verify a fresh 1Password unlock
prompt. Those results do not establish acceptance of this corrected flow.

After review, release/configure the broker and deploy the updated example,
then verify both interactive paths with the actual saved test Login. A native
unlock prompt must be observed separately; cached access can avoid it.
