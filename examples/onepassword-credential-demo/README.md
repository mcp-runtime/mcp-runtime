# Upstream header authentication through 1Password and OAuth

This example moves upstream headers out of `mcp.json`. The upstream MCP server
still requires its username/password Basic header on **every** request.
The gateway supplies that header; the MCP client supplies an `mcp-auth` token.

The Login is validated **during sign-in, before OAuth completes**. There is no
post-login credential tool or MCP elicitation step, and the demo never saves
anything into 1Password. Use only the public fixture Login `demo-user` /
`demo-password`. Real credentials are rejected by the fixed test upstream.

The login-first implementation depends on the changes in
[mcp-auth PR #35](https://github.com/tavric-labs/mcp-auth/pull/35).
These PRs remain unmerged; the planned auth release and the updated example
have not been deployed. See [validation](VALIDATION.md).

For the intended production design, reuse the existing credential plugin.
This example is the protocol test fixture; the [ownership recommendation](ARCHITECTURE.md)
keeps vault access and credential handling in the plugin and the broker generic.

## Flow

1. A new MCP client discovers `mcp-auth`, registers and starts OAuth with PKCE.
2. After consent, the resource-specific login method opens the gateway's
   upstream sign-in page. The person supplies the saved Login through the
   1Password browser extension, or a local adapter reads it through the CLI.
3. The gateway sends those credentials to its fixed upstream authentication
   check. Wrong credentials produce no authorization code or MCP token.
4. Successful validation creates a bounded, in-memory credential connection.
   The gateway returns an opaque login identity through its OAuth 2.0 adapter;
   `mcp-auth` then issues the client's authorization code and access token.
5. On every subsequent MCP request, the gateway verifies that OAuth token,
   resolves its credential connection and replaces the bearer with the
   upstream Basic header. This includes initialize and tools/list, not only
   tool calls. The client never receives the upstream header values.
6. OAuth refresh retains the credential connection. A new client starts its
   own upstream sign-in and gets a separate connection.

The container hosts the gateway and a separate loopback MCP upstream. The
upstream exposes `upstream_status` and `echo`; its own Basic-auth middleware
is unchanged by the OAuth wrapper. Runtime's platform gateway sidecar is
disabled for this standalone example. No `onepassword-mcp-plugin` is involved.

## Where configuration belongs

| Configuration | Location | Contents |
|---|---|---|
| Client-facing OAuth and deployment | [`.mcp/servers.yaml`](.mcp/servers.yaml) | Issuer, resource audience, scopes, image, two tools, and `UPSTREAM_AUTH_CONFIG` file path |
| Upstream header requirements | [`.mcp/upstream-auth.json`](.mcp/upstream-auth.json) | Basic header scheme, username/password field names, connection TTL and subject prefix |
| Broker login policy | [`.mcp/login-methods.example.json`](.mcp/login-methods.example.json) | The resource must use `upstream-demo`; other resources retain the existing company login |
| Broker-to-gateway OAuth adapter | [`.mcp/mcp-auth-connectors.example.json`](.mcp/mcp-auth-connectors.example.json) | Public login/token/userinfo endpoints, callback, client ID and PKCE-compatible public-client settings |
| Per-user vault selection | Private local adapter configuration | `op://` references to that user's saved test Login |
| Standard remote MCP client | `mcp.json` | Gateway URL only |

No usernames, passwords, vault tokens or personal item references are shipped
in the server metadata or broker examples. `auth.mode: header` is not used:
that existing Runtime mode expects credential headers from the client.
The gateway's header configuration is a separate application config file,
mounted into this example through supported Runtime `envVars`; it is not a
new Runtime CRD field.

## Broker configuration

Merge the `credential-gateway` connector example into the existing
`MCP_AUTH_CONNECTORS_FILE`; preserve all existing connector entries and their
secret environment references. Configure `MCP_AUTH_LOGIN_METHODS_FILE` with
the methods example and clear `MCP_AUTH_CONNECTOR` (those modes are mutually
exclusive). For an existing login-methods installation, append the scoped
method to the existing methods file instead of replacing it.

An unscoped company method remains the default for other resources. Its
explicit empty `subject_prefix` preserves existing provider subject IDs.
For this resource, the explicit `resources` assignment makes
`upstream-demo` mandatory, including direct method submissions and callbacks.
The prefix `upstream-demo:` must match the gateway's upstream-auth config.

The adapter uses plain OAuth 2.0 with PKCE and userinfo rather than OIDC ID
tokens. It needs no fake JWKS endpoint and no shared client secret: the exact
broker callback, one-use codes and PKCE verifier bind the handoff. All public
production endpoints require verified HTTPS. The broker never retains the
upstream username/password; it sees only the opaque credential identity.

The examples use `https://auth.mcpruntime.org/mcp-auth` and the public MCP
resource below. For another installation, update issuer, callback, resource
and endpoints consistently. The application defaults match these examples.

## Browser / URL-only MCP client

```json
{
  "mcpServers": {
    "onepassword-credential-demo": {
      "type": "http",
      "url": "https://mcp.mcpruntime.org/onepassword-credential-demo/mcp"
    }
  }
}
```

Save a dedicated 1Password Login with `demo-user`, `demo-password`, and website
`https://mcp.mcpruntime.org/onepassword-credential-demo/mcp/login/authorize`.
Connect with a client supporting HTTP OAuth discovery and PKCE. At the
upstream login page, select that saved Login through the 1Password extension
and select **Sign in and authorize MCP client**. The OAuth callback happens
after this step. Then call `upstream_status` twice.

The extension and desktop app control their own unlock prompts. An already
unlocked/authorized account can autofill without asking for another password.
The gateway validates the credentials; it cannot distinguish autofill from
manual entry. Do not claim that a fresh native unlock was verified just
because a browser form was successfully submitted.

## Local desktop adapter

Enable **1Password → Settings → Developer → Integrate with 1Password CLI**.
Build from this directory:

```bash
go build -o bin/onepassword-connect ./cmd/connect
```

Configure a local MCP server with references rather than header values:

```json
{
  "mcpServers": {
    "onepassword-desktop-demo": {
      "command": "/absolute/path/to/bin/onepassword-connect",
      "args": [
        "--stdio", "--credential-mode", "desktop", "--approve-consent",
        "--gateway", "https://mcp.mcpruntime.org/onepassword-credential-demo/mcp",
        "--issuer", "https://auth.mcpruntime.org/mcp-auth",
        "--username-ref", "op://YOUR-VAULT/YOUR-DEMO-ITEM/username",
        "--password-ref", "op://YOUR-VAULT/YOUR-DEMO-ITEM/password"
      ]
    }
  }
}
```

`--approve-consent` explicitly authorizes this local adapter's `tools:read`
OAuth consent. The adapter owns one cookie-bound sign-in flow, reads the two
configured fields through `op`, submits them at the upstream login page,
and receives its MCP OAuth token only after the upstream accepts them. It
then serves the authenticated upstream tools over stdio. Browser mode is
also available with `--credential-mode browser`.

Use `--account ACCOUNT-NAME-OR-ID` to select an account. The connector removes
inherited service-account, Connect and CLI session-token environment variables.
It pins the broker and gateway origins, refuses external redirects, and never
prints field values, tokens or provider errors. A cached 1Password approval
can suppress another unlock prompt; the adapter does not force-lock accounts.

## Validation and local acceptance

With an isolated kubeconfig, run from this directory:

```bash
KUBECONFIG=/path/to/empty-kubeconfig go test -race ./...
KUBECONFIG=/path/to/empty-kubeconfig go vet ./...
go build -o bin/credential-gateway .
```

Build the auth server from the Auth PR checkout, then run the loopback-only
integration check using both real binaries:

```bash
python3 acceptance.py \
  --auth-binary /path/to/mcp-auth-server \
  --gateway-binary ./bin/credential-gateway
```

The script starts and cleans up isolated processes with public test fixtures,
memory stores and loopback HTTP. It checks wrong credentials before OAuth,
real code/token exchange, initialize, tool listing, repeated upstream calls,
OAuth refresh and a separate sign-in for a second client. `DEMO_LOCAL_TEST`
allows HTTP only for explicit loopback testing; production remains HTTPS.

For an interactive installation with the new auth configuration:

```bash
go run ./cmd/check --credential-mode browser
go run ./cmd/check --credential-mode desktop --approve-consent \
  --username-ref 'op://YOUR-VAULT/YOUR-DEMO-ITEM/username' \
  --password-ref 'op://YOUR-VAULT/YOUR-DEMO-ITEM/password'
```

## Build, push and deploy after review

From the repository root, using the Runtime CLI:

```bash
mcp-runtime server validate --metadata-dir examples/onepassword-credential-demo/.mcp
mcp-runtime server build image onepassword-credential-demo \
  --context examples/onepassword-credential-demo \
  --dockerfile examples/onepassword-credential-demo/Dockerfile \
  --metadata-dir examples/onepassword-credential-demo/.mcp \
  --platform linux/amd64 --tag YOUR-REVIEWED-TAG
mcp-runtime server push \
  --image registry.mcpruntime.org/public/onepassword-credential-demo:YOUR-REVIEWED-TAG \
  --scope public
mcp-runtime server deploy onepassword-credential-demo \
  --metadata-dir examples/onepassword-credential-demo/.mcp --scope public --update
```

Coordinate this with the separately reviewed `mcp-auth` release/configuration.
Preserve existing login providers, signing keys, databases and TLS settings;
use the supported Runtime release/update workflow for the auth component.
This PR does not merge, publish that release or change production auth config.

## Limits

The credential gateway's OAuth endpoints are a minimal adapter for this fixed
demo upstream, not a general identity provider. Its subject is an opaque
credential-connection reference, not an employee-directory identity. Only Basic
authentication is implemented by this example's header config.

Run one replica. Credentials stay in memory for at most one hour; restart,
redeployment or expiry requires signing in again. Refreshed OAuth tokens keep
the same connection. Maps are bounded to 100 entries per stage and expired
entries are pruned on access and periodically. The gateway verifies JWT expiry
per request but does not implement online revocation checks. There is no
durable multi-user vault or cross-process credential cache here.
