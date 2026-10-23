# Header-only upstream with the bundled vault gateway

This example MCP server accepts its normal Basic header on every request. It
contains no OAuth, 1Password, credential enrollment tools or MCP elicitation.
The optional mcp-auth bundle performs OAuth, reads approved vault references,
checks those credentials with this server before code issuance, and injects
headers for later MCP calls.

Use only a dedicated test Login containing the public fixtures `demo-user` and
`demo-password`. The server deliberately accepts no other credentials.

## Configuration ownership

| File | Consumed by | Contents |
| --- | --- | --- |
| `.mcp/servers.yaml` | Runtime CLI/operator | Upstream image, route, port, tools and `auth.mode: header` contract |
| `.mcp/credential-gateway.example.json` | mcp-auth combined service | Provider mode, mounted-secret paths, fixed upstream URL, validation URL, OAuth resource, header transforms |
| `.mcp/mappings.example.json` | mcp-auth gateway operator, as a private mounted file | Exact verified subject/service to vault-field references |
| MCP client configuration | MCP client | Gateway resource URL only |

The file is **`.mcp/servers.yaml`**, plural. No new YAML field or Runtime CRD is
needed for this layout. Runtime does not upload companion JSON to mcp-auth or
rewrite its catalog entry; the upstream URL and gateway resource URL are
different endpoints. Actual provider tokens, private keys and private mappings
must stay out of Git and container images.

The upstream example disables Runtime's sidecar and authenticates Basic itself.
Consequently it does not demonstrate Runtime agent grants, sessions or policy.
The optional mcp-auth gateway checks its own OAuth session on every request.

## Build and deploy the upstream

After review, using the supported Runtime CLI workflow from this directory:

```bash
mcp-runtime server validate
mcp-runtime server build
mcp-runtime server push
mcp-runtime server deploy
```

The review image tag has not been published. These instructions do not imply a
production rollout has happened. Runtime sets `MCP_PATH` to the public MCP path;
local runs default to `/mcp` on port 8088.

The upstream URL is the Runtime endpoint, for example
`https://mcp.example.com/vault-header-demo/mcp`. It requires Basic authentication.
Its authenticated validation endpoint is that URL plus `/auth/check` and accepts
GET only. A successful credential check returns 204; incorrect headers return 401.
The two tools are `upstream_status` and `echo`.

## Configure the optional mcp-auth bundle

Use [mcp-auth PR #35](https://github.com/tavric-labs/mcp-auth/pull/35), which includes
the optional gateway and combined binary. Follow its
[gateway guide](https://github.com/tavric-labs/mcp-auth/blob/feat/upstream-credential-login/docs/credential-gateway.md)
for building, mounting secrets, configuring identity login and exposing the
gateway's port 8090 beside OAuth's port 8080.

1. Replace all example hosts in the companion JSON files. `resource_url` is the
   **gateway's public URL**, such as
   `https://credentials.example.com/vault-header-demo/mcp`; `upstream_url` is
   the Runtime endpoint. Keep `validation_url` on the same upstream origin.
2. Mount the gateway config and private mappings on mcp-auth. Replace the
   placeholder subject with the verified identity provider subject, including
   any login-method prefix. The mappings select a dedicated test Login's
   username and password fields. Reference paths alone confer no access.
3. Provision a read-only 1Password service account with access to that test vault
   and mount its token file. Mount a gateway RSA private key and a random
   session-binding key (at least 32 characters). Keep their paths consistent
   with the config. Do not reuse the OAuth signing key as a resource-client key.
4. Add the gateway resource URL and `tools:read` scope to mcp-auth's existing
   allowed resources/scopes. Set
   `MCP_AUTH_CREDENTIAL_GATEWAY_CONFIG=/etc/mcp-auth/credential-gateway.json`.
   Keep the existing identity connector/login registry. The bundle registers its
   own resource client for credential assertions, so no extra external connector
   entry is needed for this resource.
5. Configure the gateway's public ingress, including protected-resource
   discovery routes, to reach its listener. Permit its fixed upstream traffic.

One combined service runs both listeners; this does not require a separately
deployed plugin service. The original plugin repository has not been removed.

The client configuration contains only the gateway URL:

```json
{
  "mcpServers": {
    "vault-demo": {
      "url": "https://credentials.example.com/vault-header-demo/mcp"
    }
  }
}
```

A new OAuth authorization checks the user's mapped credentials at the upstream
before issuing a code. Wrong credentials deny login. Later client requests carry
only the issued OAuth bearer; mcp-auth supplies Basic to this upstream. Refresh
preserves the session, while revocation blocks the next request even on a cached
header. The gateway never returns the upstream password to the client.

Hosted service-account access does not open a desktop app. The provider's
`desktop` mode needs a gateway running on the same machine as the user's `op`
and desktop app; the shipped container is for service-account mode. A local
desktop-to-hosted gateway handoff is not implemented. 1Password controls cached
approvals and the unlock method.

## Validation

```bash
go test -race ./...
go vet ./...
```

The upstream tests verify rejection of wrong credentials, authenticated
validation and MCP initialization, and absence of passwords in responses.
mcp-auth's combined integration tests exercise the actual OAuth core and gateway
against a fake vault/upstream, including login ordering, a new client, refresh,
header injection and revocation. Desktop-provider tests verify CLI environment
isolation with a fake CLI. The loopback harness exercises the real bundle and
this upstream process using that fake CLI:

```bash
# Build combined-server/cmd/mcp-auth in the matching mcp-auth PR checkout first.
go build -o /tmp/vault-header-upstream .
python3 acceptance.py --auth-binary /path/to/combined-mcp-auth --upstream-binary /tmp/vault-header-upstream
```

It checks wrong credentials before code issuance, MCP initialize/list/repeated
tool calls, refresh and revocation. It generates only temporary fixture keys and
configuration and terminates its local processes. A real service-account canary,
a native desktop prompt, and deployment of the bundle remain separate acceptance
steps.

Local review evidence (2026-10-23): race tests and `go vet` passed for both
Runtime example modules and the mcp-auth modules; Runtime CLI metadata validation
passed; the combined service container built; the loopback harness above passed.
These checks use fixture credentials and a fake vault CLI and do not establish
production, real-vault or native-prompt acceptance.
