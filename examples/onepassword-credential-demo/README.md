# 1Password credential delivery demo

This example is a standalone OAuth-protected MCP server that acts as a small
credential gateway for a fixed loopback test API. It demonstrates two ways to
connect a saved 1Password Login without putting usernames or passwords in
`mcp.json`. It does not use `onepassword-mcp-plugin`.

**Use only the public fixtures `demo-user` / `demo-password`.** The server
rejects other values. Do not use this example as a production credential store.

## What runs where

- The MCP client gets an audience-bound access token from `mcp-auth` through
  browser sign-in and PKCE. Its configuration contains the MCP URL only.
- This example verifies that token with the published `mcp-auth` Go SDK.
  `mcp-auth` does not receive the upstream username or password.
- `connect_upstream` creates a random, one-use, five-minute connection link
  bound to the verified subject and access token ID (`jti`).
- The browser form or local connector submits the dedicated test Login over
  HTTPS. The example validates and keeps it in memory for at most 15 minutes,
  bounded by the access token expiry.
- `upstream_status` uses HTTP Basic to call the fixed API listening on
  `127.0.0.1:9099` in the same container. It returns authentication status only;
  it does not forward the MCP OAuth bearer to that API.

The Runtime sidecar is disabled for this example: the application itself
verifies OAuth and supplies upstream credentials. Runtime's separate
grants, adapter certificates and Kubernetes credential broker are not exercised.
The checked-in `auth` metadata lets Runtime register this resource with its
bundled authorization server.

## Connect an MCP client

The reference deployment uses:

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

Use a client supporting MCP HTTP OAuth discovery and PKCE. Sign in and call
`connect_upstream`. Keep the returned connection URL private until consumed or
expired; possession permits connecting the public test credentials to that
access token. No password is passed through MCP tool arguments or results.

## Browser and 1Password extension

1. Save a dedicated 1Password Login with username `demo-user`, password
   `demo-password`, and website
   `https://mcp.mcpruntime.org/onepassword-credential-demo/mcp/connect`.
2. Open the URL returned by `connect_upstream` in a browser with the 1Password
   extension installed and enabled.
3. Focus Username or select the extension icon, choose the saved Login, and
   select **Connect test upstream**.
4. Call `upstream_status` twice from the same signed-in MCP client. Both calls
   should report `authenticated: true` without asking for credentials again.
5. Call `disconnect_upstream`. The next `upstream_status` call must fail.

The page exposes standard `username` and `current-password` autocomplete
fields. Browser autofill is controlled by the extension and the user; the
website does not launch or unlock the native 1Password app automatically.

The form requires an exact same-origin POST, a Secure/HttpOnly/SameSite cookie,
and a ticket-bound CSRF token. Its `same-origin` referrer policy preserves the
Origin header on native form submissions while suppressing cross-origin
referrers. Using `no-referrer` here would turn the form POST's Origin into
`null` ([Fetch specification](https://fetch.spec.whatwg.org/#append-a-request-origin-header)).

## Local desktop connector

Install 1Password CLI and enable **Settings → Developer → Integrate with
1Password CLI** in the desktop app. Sign in to your account in that app.
Copy the dedicated test Login's username and password secret references.

Build from this directory:

```bash
go build -o bin/onepassword-connect ./cmd/connect
```

The connector supports either a one-shot command or a local MCP tool. For a
one-shot connection, first call the hosted `connect_upstream` tool, then run:

```bash
./bin/onepassword-connect \
  --gateway https://mcp.mcpruntime.org/onepassword-credential-demo/mcp \
  --username-ref 'op://YOUR-VAULT/YOUR-DEMO-ITEM/username' \
  --password-ref 'op://YOUR-VAULT/YOUR-DEMO-ITEM/password' \
  --connect-url 'PASTE-THE-ONE-USE-CONNECTION-URL'
```

Optionally select the account using `--account ACCOUNT-NAME-OR-ID`. When
required, 1Password prompts you to unlock or approve CLI access using your
configured method, including your account password. Its own authorization
session can suppress repeated prompts; this demo does not force a new unlock
for every field read.

To expose the local `connect_from_1password` MCP tool, add a second server entry:

```json
{
  "mcpServers": {
    "onepassword-desktop-demo": {
      "command": "/absolute/path/to/bin/onepassword-connect",
      "args": [
        "--stdio",
        "--gateway", "https://mcp.mcpruntime.org/onepassword-credential-demo/mcp",
        "--username-ref", "op://YOUR-VAULT/YOUR-DEMO-ITEM/username",
        "--password-ref", "op://YOUR-VAULT/YOUR-DEMO-ITEM/password"
      ]
    }
  }
}
```

Call the hosted `connect_upstream` tool, then pass only its `connect_url` to
the local `connect_from_1password` tool. Call hosted `upstream_status` twice
and `disconnect_upstream` as above. Both client entries contain references
or URLs, never credential values.

The local connector pins the exact HTTPS destination from `--gateway`, refuses
redirects, and reads only the `op://` references supplied by its owner at
startup. It removes inherited service-account, Connect, and CLI session
environment variables before invoking `op`; this path exercises desktop/CLI
authentication rather than a service account. It never returns `op` output or
provider errors to the model.

## Validate, build, push and deploy

Prerequisites: Docker, Runtime CLI on PATH, a saved platform login with public
publishing permissions, and a platform with bundled `mcp-auth`. Commands below
run from the repository root. Change the checked-in issuer, audience, image
and the application `MCP_AUTH_RESOURCE` / `MCP_AUTH_ISSUER` environment values
when deploying to another platform. The application defaults match this
reference deployment.

```bash
mcp-runtime server validate --metadata-dir examples/onepassword-credential-demo/.mcp
mcp-runtime server build image onepassword-credential-demo \
  --context examples/onepassword-credential-demo \
  --dockerfile examples/onepassword-credential-demo/Dockerfile \
  --metadata-dir examples/onepassword-credential-demo/.mcp \
  --platform linux/amd64 --tag YOUR-UNIQUE-TAG
mcp-runtime server push \
  --image registry.mcpruntime.org/public/onepassword-credential-demo:YOUR-UNIQUE-TAG \
  --scope public
mcp-runtime server deploy onepassword-credential-demo \
  --metadata-dir examples/onepassword-credential-demo/.mcp --scope public
```

For an existing deployment, add `--update` to `server deploy`. Inspect using
`mcp-runtime server get onepassword-credential-demo --namespace mcp-servers-public`.
Retire only this example using
`mcp-runtime server delete onepassword-credential-demo --namespace mcp-servers-public`.

From this example directory, with an isolated kubeconfig:

```bash
KUBECONFIG=/path/to/empty-kubeconfig go test -race ./...
KUBECONFIG=/path/to/empty-kubeconfig go vet ./...
go run ./cmd/check --paths browser,desktop
```

The interactive acceptance client prints an OAuth sign-in URL, verifies the
MCP handshake and tool inventory, then waits for each connection path. It
checks two authenticated upstream calls and immediate denial after
disconnect. Type `renew` in its terminal to obtain a fresh connection link
after a rollout or an expired form. Tokens remain in the client process and
are never printed or saved. The callback listens on loopback; open its login
link on the same machine.

## Limits

Run exactly one replica. Connections and tickets are in memory; restart,
redeployment, access-token refresh or token expiry requires reconnecting.
The server verifies JWT expiry on each call, but does not implement online
OAuth session revocation checks. `disconnect_upstream` immediately discards
the current token's credentials and pending tickets. Expired entries are
pruned on access and every 30 seconds, with at most 100 pending links and 100
connections. These are demonstration limits, not a durable multi-user vault.

The hosted reference used `mcp-auth` 0.4.4 when this demo was validated. It
issues `jti` but not the later `sid` refresh-family claim; binding to `jti`
deliberately prevents credentials carrying over to a refreshed access token.
No update of the authorization server is required for this example.
