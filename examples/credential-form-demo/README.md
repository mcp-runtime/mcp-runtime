# Credential form demo

A header-authenticated MCP server for trying the optional mcp-auth credential
gateway. It accepts only two headers, `X-Demo-Username` and `X-Demo-Password`, and
exposes two tools, `upstream_status` and `echo`. The gateway shows a sign-in form
for those headers, checks them at `/auth/check`, and then supplies them on every
request, so the MCP client needs only a URL and the bearer token it gets.

Use only the public fixtures `demo-user` and `demo-password`; the server accepts no
other values.

## Files

| File | Purpose |
| --- | --- |
| `.mcp/servers.yaml` | The deployment and its `auth.mode: header` contract, with `auth.credentialGateway.validationPath` opting into the gateway |
| `main.go` | The upstream: checks both headers on every request and answers `GET <path>/auth/check` |

The headers named in `auth.headers` become the inputs of the sign-in form, labeled
with their own names. Nothing about a password manager or vault is configured
anywhere; a browser's password manager can fill the inputs if it has a saved login
for the gateway's host.

## Build and deploy

```bash
mcp-runtime server validate
mcp-runtime server build image credential-form-demo --tag form-review
mcp-runtime server push --image registry.example.com/public/credential-form-demo:form-review --scope public
mcp-runtime server deploy credential-form-demo --scope public --tag form-review
```

Adjust the image repository to your registry. Runtime sets `MCP_PATH` to the
public MCP path; local runs default to `/mcp` on port 8088.

## Connect it to the gateway

Derive the gateway service from the pushed server and install the gateway next to
mcp-auth (a dry run first, then `--yes`). It runs Runtime's existing `mcp-gateway`
image in credential mode, once, for every opted-in server; `--gateway-image`
defaults to the operator's `MCP_GATEWAY_PROXY_IMAGE`, so you can usually omit it:

```bash
mcp-runtime cluster mcp-auth-connector credential-gateway --file gateway.json \
  --secret-file session.key=./session.key --secret-file client.pem=./client.pem \
  --gateway-image <registry>/mcp-gateway:<tag> \
  --tls-secret <auth tls secret> --kubeconfig ... --context ... --expect-cluster-id ... --dry-run
```

`gateway.json` holds only the listener, the issuer and the session key reference;
the services come from the pushed servers.

Then the client configuration is just the gateway resource URL:

```json
{"mcpServers":{"credential-form-demo":{"url":"https://auth.example.com/credential-form-demo/mcp"}}}
```

On first connect the client opens the sign-in form. Enter `demo-user` and
`demo-password`, or let your password manager fill them. A wrong value is refused
before any token is issued.

The gateway must be able to reach this server's public URL and `/auth/check` from
where it runs, and mcp-auth must be able to reach the gateway's `/login/token` and
`/login/userinfo`. Header values live in the gateway's memory only; restarting the
gateway clears them and people sign in again.
