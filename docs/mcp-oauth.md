# MCP authorization with the optional mcp-auth server

MCP OAuth authorization is optional. An MCP server enables it by including
`spec.auth`; omitting `spec.auth` leaves OAuth disabled. The bundled
`mcp-auth-server` is also optional because a deployment may use an external
OAuth authorization server instead.

## What each component does

There are three separate responsibilities:

```text
MCP client → optional mcp-auth-server → Keycloak/OIDC provider
           → MCP access token → Runtime gateway → policy/governance → MCP server
```

- The bundled `mcp-auth-server` is the OAuth authorization server. It logs the
  user in through one configured identity provider and mints resource-bound MCP tokens.
- Keycloak is an external identity provider. It owns the realm, users, and
  upstream OIDC client credentials.
- The Runtime gateway is the protected-resource boundary. It validates the
  token and applies grants, agent sessions, trust, and per-tool policy before
  forwarding the call.

The gateway is the policy decision point because governance must inspect the
actual MCP JSON-RPC tool call and current grant/session state. The
authorization server only sees login and token requests.

Each MCP server can declare its allowed OAuth scopes in its `.mcp/servers.yaml`
manifest. The selected identity-provider connector supplies login and claims;
it does not need to define the MCP server's scopes:

```yaml
auth:
  issuerURL: https://auth.example.com/mcp-auth
  audience: https://mcp.example.com/cully/mcp
  scopes: [tools:read, tools:write]
```

The operator publishes this scope list to the bundled authorization server
for that exact resource. MCP Auth rejects requests for scopes outside the
resource list and advertises the list in protected-resource metadata. The
Runtime gateway also advertises the server's scopes in its public resource
metadata and checks token scope and per-call policy when a tool is
invoked. Servers without `auth.scopes` receive MCP Auth's read-only default;
set `auth.scopes` in the server manifest to grant write access explicitly.

## Responsibility boundary

Runtime ships and can deploy the optional `mcp-auth-server`. You operate the
identity provider. You are responsible for operating Keycloak,
Okta, PingOne, Entra ID, Auth0, or another OIDC/OAuth provider, including its
realm or tenant, users, client registration, client secret, redirect URI,
claims, scopes, availability, backups, and certificate/DNS configuration.

Runtime is responsible for loading the connector, validating its required
configuration, exposing MCP authorization metadata, and keeping the provider
secret in Kubernetes. The mcp-auth server authenticates through that provider
and mints MCP tokens; Runtime remains the MCP resource server and governance
decision point.

Before enabling the feature, confirm that the provider exposes HTTPS OIDC
discovery, has a confidential client with authorization-code flow and S256
PKCE enabled, has the exact callback URI registered, and includes a stable
subject claim. Runtime cannot repair an incorrect realm,
client registration, redirect URI, claim mapping, or provider outage.

## Public hostnames and TLS

For a public k3s deployment, create DNS A records pointing to the ingress node:

```text
keycloak.example.com → <public ingress IP>
auth.example.com     → <public ingress IP>
```

Issue certificates for both names with the cluster's ACME issuer. Setup creates
the auth-server Certificate and its Secret in the `mcp-platform` namespace;
`--mcp-auth-tls-secret` is only needed when certificates are externally managed.
Keycloak's certificate can be named `keycloak-tls`.

The platform hostname must also be present in the platform UI Ingress before
opening it in a browser. A browser with HSTS will not offer a certificate
exception: if DNS is missing, the Ingress is absent, or cert-manager has not
issued the certificate, Firefox may show a security error or Traefik may serve
its default certificate. Check these before debugging login:

```bash
dig +short platform.example.com
kubectl get ingress -n mcp-platform mcp-platform-ui
kubectl get certificate -n mcp-platform
kubectl describe certificate -n mcp-platform <platform-certificate>
```

Wait for the Certificate condition to become `Ready=True`, and verify that
the certificate SAN contains the exact platform hostname. Do not work around
this with an HTTP URL or a browser exception.

Do not use an HTTP issuer, an IP address, or a self-signed public certificate
outside local test mode.

If Traefik connects to Keycloak over HTTPS, configure a Traefik
`ServersTransport` with `serverName` set to the Keycloak hostname. Otherwise
the backend certificate is checked against the pod IP and discovery fails with
`x509: ... certificate ... doesn't contain any IP SANs`. Do not disable TLS
verification in production.

## Configure Keycloak

Deploy Keycloak separately, then create:

1. Realm: `mcp-runtime`.
2. Confidential client: `mcp-auth`.
3. Standard authorization-code flow enabled; direct password grants disabled.
4. Exact redirect URI:

   ```text
   https://auth.example.com/mcp-auth/identity/callback
   ```

The resulting OIDC issuer is:

```text
https://keycloak.example.com/realms/mcp-runtime
```

Create a test user in the realm. Keep the client secret in a secret manager or
environment variable; never place it in the connector JSON or Git.

Run the Runtime-side provider preflight before setup:

```bash
./bin/mcp-runtime auth provider-check \
  --issuer-url https://keycloak.example.com/realms/mcp-runtime
```

This checks discovery of the issuer, authorization endpoint, token endpoint,
and JWKS URI without sending client credentials. It does not prove the client
secret, callback, user login, claims, or scopes; complete those provider-side
checks with a dedicated non-admin test account.

## Write the connector file

The connector file holds provider configuration. The
`client_secret_env` value names the environment variable that setup reads and
stores in the Kubernetes Secret `mcp-auth-connector-secrets`.

Every referenced environment variable must be exported in the same shell that
starts setup. Setup fails before applying the auth server if a referenced
secret is unset, so a partially configured connector is never deployed. Read
the value from a protected file or secret manager, and keep it out of the
connector JSON:

```bash
export KEYCLOAK_CLIENT_SECRET="$(tr -d '\n' < /secure/keycloak-client-secret)"
```

```json
{
  "keycloak": {
    "issuer": "https://keycloak.example.com/realms/mcp-runtime",
    "authorization_endpoint": "https://keycloak.example.com/realms/mcp-runtime/protocol/openid-connect/auth",
    "token_endpoint": "https://keycloak.example.com/realms/mcp-runtime/protocol/openid-connect/token",
    "jwks_uri": "https://keycloak.example.com/realms/mcp-runtime/protocol/openid-connect/certs",
    "token_endpoint_internal": "https://keycloak.mcp-platform.svc.cluster.local:8443/realms/mcp-runtime/protocol/openid-connect/token",
    "jwks_uri_internal": "https://keycloak.mcp-platform.svc.cluster.local:8443/realms/mcp-runtime/protocol/openid-connect/certs",
    "token_endpoint_server_name": "keycloak.example.com",
    "client_id": "mcp-auth",
    "client_secret_env": "KEYCLOAK_CLIENT_SECRET",
    "exchange_client_id": "mcp-auth",
    "scopes": ["openid", "profile", "email"],
    "identity_claims": ["preferred_username"],
    "token_endpoint_auth_method": "client_secret_post",
    "allowed_upstream_callback_uris": [
      "https://auth.example.com/mcp-auth/identity/callback"
    ],
    "downstream_token_strategy": "upstream_session"
  }
}
```

All four provider endpoints must use HTTPS in production. Explicit endpoints
are useful when the auth pod cannot hairpin through the public ingress. For an
in-cluster provider, keep the public `token_endpoint` for discovery and add
`token_endpoint_internal` with the private HTTPS Service URL plus
`token_endpoint_server_name` matching the provider certificate. If an internal
endpoint is used, it must still be HTTPS and use a certificate trusted by the
auth server; an internal HTTP shortcut is test-only.

## Configure the MCPServer resource

To enable OAuth on a governed MCPServer, add `spec.auth` and choose one
canonical resource URI:

```yaml
spec:
  auth:
    # Defaults from the bundled issuer configured on the operator.
    audience: https://mcp.example.com/my-server/mcp
```

The audience must equal the MCP server's canonical public URL. The operator
keeps the bundled mcp-auth resource list aligned with OAuth MCPServer
audiences. An optional `--mcp-auth-resource-url` is an initial bootstrap value
and must exactly match the corresponding `spec.auth.audience`. The
gateway rejects tokens with a different issuer or audience, then forwards the
validated bearer to the MCP application server. The application validates the
same issuer and audience and must not pass the token to another API.

You can omit `audience`. The operator then derives it from the server's public
URL: `https://` when the operator runs with `MCP_DEFAULT_INGRESS_TLS=true` or
the ingress has the `traefik.ingress.kubernetes.io/router.tls: "true"`
annotation, then `spec.ingressHost` (or the shared MCP host resolver's
`MCP_MCP_INGRESS_HOST`, `MCP_DEFAULT_INGRESS_HOST`, or `mcp.<MCP_PLATFORM_DOMAIN>`),
then `/<publicPathPrefix>/mcp`. A path-based
server named `my-server` on `mcp.example.com` gets
`https://mcp.example.com/my-server/mcp`. If no host is known (for example in
local test mode), set `audience` explicitly.

Derived `audience` and `issuerURL` values are computed on every reconcile and
are not written back to `spec`. Changing the host, path, TLS setting, or
platform domain therefore changes them too; `status.url` shows the current
public URL.

Each public route has one owner. When two MCPServers resolve to the same path
on the same host (or one of them is path-based and matches every host), the
older one keeps the route. The later one reports an `Error` phase naming the
owner, gets no Ingress, and its audience is not added to the bundled
authorization server. Otherwise the two servers would accept each other's
tokens.

The operator owns `MCP_PATH` for every server and derives it from the public
ingress route. When the gateway sets `gateway.stripPrefix`, it is the route
with that prefix removed, because that is the path the gateway forwards to the
server. Do not add it to `spec.envVars` or `.mcp/servers.yaml`; the operator
replaces any value set there.

For every OAuth server, with the gateway enabled or disabled, the operator also
injects the values the application needs to validate the bearer and publish
matching metadata: `MCP_AUTH_RESOURCE` (the audience),
`MCP_AUTH_RESOURCE_METADATA_URL`
(`<origin>/.well-known/oauth-protected-resource<path>`), and `MCP_AUTH_ISSUER`
(`auth.issuerURL`). The gateway forwards the original validated bearer rather
than exchanging it, so that token is valid for the same issuer and public
audience upstream. `MCP_PATH` can differ from the audience path when the gateway
strips a prefix; the token audience stays the public resource URL. Remove
hand-set copies of these derived values so the advertised resource stays in
step with the ingress host. The gateway challenge
and ingress metadata route use the same metadata URL derived from `audience`.
MCP clients reject metadata whose `resource` names a different origin than the
URL they connected to.

## Deploy through setup

Create the persistent signing-key Secret with the RSA key stored as
`private-key.pem`, export the client secret only in the setup environment, and
run:

```bash
KEYCLOAK_CLIENT_SECRET='from-your-secret-manager' \
./bin/mcp-runtime setup \
  --with-tls --tls-cluster-issuer letsencrypt-prod \
  --with-mcp-auth-server \
  --mcp-auth-signing-key-secret mcp-auth-signing-key \
  --mcp-auth-connectors-file /secure/mcp-auth-connectors.json \
  --mcp-auth-connector keycloak
```

The TLS certificate for the issuer host is provisioned by setup using the
configured TLS ClusterIssuer. Pass `--mcp-auth-tls-secret` only when the
certificate is externally managed (and use `--provided-tls-secrets`).

The default image is `docker.io/princekrroshan01/mcp-auth-server:0.4.4`.
The server uses SQLite on a PVC in production and memory storage only in
`--test-mode`. The setup flag is opt-in; when it is absent, Runtime does not
deploy this authorization server.

The mcp-auth server image is released independently from the MCP Runtime CLI
and platform images. For a public production rollout, use a unique image ref
such as `registry.mcpruntime.org/mcp-auth-server:<tag>` and update it through
the [reference deployment guide](reference-deployment.md#separate-release-tracks-and-user-verification).
An image-only update preserves the existing connector configuration, SQLite
PVC, signing key, and TLS Secret; it does not require rerunning `setup` or
issuing a certificate.

Client ID Metadata Documents (CIMD) are disabled by default in mcp-auth. To
test CIMD, deploy an image containing the feature and set
`MCP_AUTH_CLIENT_ID_METADATA_ENABLED=true`; then confirm the authorization
server metadata advertises `client_id_metadata_document_supported: true`.
`MCP_AUTH_CLIENT_ID_METADATA_HOSTS` can restrict fetched metadata documents to
a comma-separated host allowlist. When unset, mcp-auth still rejects non-HTTPS
URLs and blocks private/reserved network destinations during DNS resolution.
After enabling CIMD, clear a client's cached OAuth credentials before
reconnecting so it does not reuse a previously registered DCR client ID.

The same values can be supplied through the public deployment environment:

```bash
export MCP_SETUP_WITH_MCP_AUTH_SERVER=1
# Issuer defaults to https://auth.<MCP_PLATFORM_DOMAIN>/mcp-auth.
# Resource audiences are reconciled from OAuth MCPServer objects.
# Optional only for externally managed TLS (required with provided-tls-secrets).
export MCP_SETUP_MCP_AUTH_TLS_SECRET=mcp-auth-server-tls
export MCP_SETUP_MCP_AUTH_SIGNING_KEY_SECRET=mcp-auth-signing-key
export MCP_SETUP_MCP_AUTH_CONNECTORS_FILE=/secure/mcp-auth-connectors.json
export MCP_SETUP_MCP_AUTH_CONNECTOR=keycloak
```

## Verify the flow

Check that the authorization server and Keycloak publish metadata. For a
path-mounted issuer such as `https://auth.example.com/mcp-auth`, RFC 8414
inserts the well-known segment before the issuer path, so discovery is served
at `https://auth.example.com/.well-known/oauth-authorization-server/mcp-auth`,
not under the issuer URL:

```bash
curl -fsS https://auth.example.com/.well-known/oauth-authorization-server/mcp-auth
curl -fsS https://keycloak.example.com/realms/mcp-runtime/.well-known/openid-configuration
kubectl -n mcp-platform rollout status deploy/mcp-auth-server
```

Then use an MCP client or the Go example's OAuth metadata (`examples/oauth-example-go-2025-11-25/.mcp/servers.yaml`)
to run the real PKCE flow.
Verify all of these outcomes:

- no token returns `401` and a proper `WWW-Authenticate` challenge;
- a valid token reaches the Runtime gateway;
- a granted tool succeeds and produces an audit event;
- an ungranted tool returns `403` and does not reach the upstream server;
- changing or revoking a Runtime grant takes effect without issuing a new IdP
  token.

## Local test mode

For Kind-only testing:

```bash
./bin/mcp-runtime setup --test-mode --with-mcp-auth-server \
  --ingress-manifest config/ingress/overlays/http
```

Test mode may use the loopback issuer, in-memory storage, generated signing
keys, and the local development token exchange. It does not prove production
OIDC federation. Use the Keycloak connector and HTTPS settings above for a
real provider test.

## Other identity providers

The connector is provider-neutral. The bundled auth server loads a named
connector from the JSON file at startup; the gateway contains no Okta, PingOne,
Auth0, Microsoft Entra ID, Google, or other provider-specific adapter:

```text
MCP_AUTH_CONNECTORS_FILE → MCP_AUTH_CONNECTOR → IdentityProvider/TokenExchanger
```

One auth-server process selects one connector and one MCP resource. A file may
contain several named connectors for different environments, but changing the
selected provider requires changing `--mcp-auth-connector` and restarting or
rolling out the deployment. This keeps provider credentials and provider
specific behavior outside the Runtime gateway and MCP tool handlers.

For any OIDC provider, start with this shape and replace the issuer, client ID,
and callback settings from that provider's console:

```json
{
  "oidc": {
    "issuer": "https://idp.example.com/<tenant-or-realm>",
    "client_id": "mcp-auth",
    "client_secret_env": "OIDC_CLIENT_SECRET",
    "exchange_client_id": "mcp-auth",
    "scopes": ["openid", "profile", "email"],
    "identity_claims": ["sub", "email"],
    "token_endpoint_auth_method": "client_secret_post",
    "allowed_upstream_callback_uris": [
      "https://auth.example.com/mcp-auth/identity/callback"
    ],
    "downstream_token_strategy": "upstream_session"
  }
}
```

OIDC discovery fills in authorization, token, and JWKS endpoints when they are
omitted. Explicitly configure them when the browser-facing provider hostname
and the auth pod's back-channel hostname differ. Request `openid` for OIDC and
use a stable claim such as `sub` or `preferred_username`; plain OAuth 2.0
providers need a `userinfo_endpoint` and an identity claim from that response.

Typical issuer patterns are:

| Provider | Typical issuer pattern | Notes |
|---|---|---|
| Okta | `https://<org>.okta.com/oauth2/<authorization-server-id>` | Register the exact `/identity/callback` URI and use the authorization server's discovery document. |
| PingOne | `https://auth.pingone.<region>/<environment-id>/as` | Use the environment's OIDC discovery URL and a confidential client. |
| Auth0 | `https://<tenant>.auth0.com/` | Enable `openid profile email`; set the API/resource audience according to the Auth0 tenant. |
| Microsoft Entra ID | `https://login.microsoftonline.com/<tenant-id>/v2.0` | Use a tenant-specific issuer for deterministic claim and issuer validation. |
| Google | `https://accounts.google.com` | Configure the OAuth client redirect URI and request `openid profile email`. |
| Generic OIDC | provider's `issuer` URL | The provider must expose discovery, authorization-code login, token, JWKS, and suitable identity claims. |

Support for optional features varies by provider. Verify discovery, callback behavior, token endpoint
authentication, scopes, refresh behavior, and claims with the provider before
using it in production. The mcp-auth project has provider-specific notes and a
verified-provider matrix in its [authorization-server guide](https://github.com/Agent-Hellboy/mcp-auth/blob/main/docs/auth-server.md).

Before setup, run the Runtime preflight against the provider issuer:

```bash
./bin/mcp-runtime auth provider-check \
  --issuer-url https://idp.example.com/<tenant-or-realm>
```

This fetches `/.well-known/openid-configuration` without sending client
credentials and checks the issuer, authorization endpoint, token endpoint, and
JWKS URI. It does not prove client-secret validity, redirect registration, user
login, refresh behavior, or provider claim mappings; complete those with a
dedicated test user before production rollout.

## Clean setup and credential preservation

`hack/deploy/mcpruntime-org/clean.sh --yes --wait` destructively resets MCP
Runtime namespaces and workload data. Use it for a clean reinstall, not normal
upgrades. By default it writes a `0700` backup directory under
`~/.mcpruntime/backups/mcpruntime-org` and backup files are `0600`.

The backup preserves platform bootstrap secrets, TLS material, OIDC
configuration, and auth configuration when those resources are included by the
deployment version. It does not preserve tenant/user database rows, API keys,
grants, sessions, registry blobs, or analytics history. Keycloak realm data is
owned by the identity provider and must be exported/restored with Keycloak's
realm-export tooling; a Kubernetes Secret backup is not a realm backup.

Keep the platform admin password and OIDC client secret in a password manager
or a `0600` file outside Git, and export them only for setup:

```bash
umask 077
export MCP_PLATFORM_ADMIN_EMAIL=admin@example.com
export MCP_PLATFORM_ADMIN_PASSWORD='<from-password-manager>'
export KEYCLOAK_CLIENT_SECRET='<from-password-manager>'
MCP_DEPLOY_ENV=/secure/mcpruntime-org.env \
  hack/deploy/mcpruntime-org/setup.sh
```

Before cleaning, run `clean.sh --dry-run` and confirm its namespace list. For a
fresh provider-backed install, prepare the env file, TLS Secret, signing-key
Secret, connector file, selected connector, and provider secret first. Never
use `--no-backup` on production unless the loss is intentional.

After setup, verify in order: provider discovery; mcp-auth discovery and JWKS;
mcp-auth readiness; platform admin login; Runtime server catalog, sessions,
and grants; governed MCP metadata; unauthenticated `401` challenge; valid token;
allowed tool; denied tool; and grant/session revocation.

If setup stops during image publication with a Kubernetes API TLS handshake
timeout, first verify the k3s API and registry pod, then rerun the same setup
command. Image publication is idempotent. If it stops with `references unset
environment variable`, export the named connector secret and rerun; do not
disable connector validation.

After an installation that enables mcp-auth, run:

```bash
./bin/mcp-runtime cluster diagnostics
```

The doctor skips mcp-auth when it is not installed. When it is installed, it
checks the deployment rollout and the required signing-key and TLS Secret data;
use the reported `kubectl rollout status` or Secret/Certificate remedy before
testing OAuth.

## Credentials and secret handling

Never put passwords, OIDC client secrets, private signing keys, bearer tokens,
or realm exports in the repository, Docker command arguments, shell history,
issue reports, or CI logs. Connector JSON contains references such as
`client_secret_env`; setup copies the referenced value into the Kubernetes
Secret `mcp-auth-connector-secrets`. Rotate provider secrets and platform admin
passwords after test deployments. Rotate the mcp-auth signing key only with an
explicit token/JWKS rollover plan because existing tokens will become invalid.

## How the MCP SDK fits

The SDK runs at the application boundary, outside Runtime governance:

- MCP clients use the SDK's discovery, PKCE, token, and `WWW-Authenticate`
  helpers to obtain an MCP token from the authorization server.
- A standalone MCP resource server can use the SDK's `JWTVerifier` to validate
  issuer, JWKS signature, audience, expiry, and required scopes before invoking
  tools.
- A governed MCP server validates the bearer in both the Runtime gateway and
  the SDK-backed application. They are one logical protected resource and use
  the same issuer and audience. Runtime applies grants, sessions, and policy;
  the application must not pass the token to downstream APIs.

The SDK consumes provider-neutral MCP authorization metadata and JWTs. Switching
from Keycloak to Okta or PingOne changes the connector and IdP client
configuration; MCP tool code stays the same. Read the
mcp-auth project's [auth-server architecture](https://github.com/Agent-Hellboy/mcp-auth/blob/main/docs/architecture.md)
and [auth-client SDK guide](https://github.com/Agent-Hellboy/mcp-auth/blob/main/docs/auth-client.md)
for the Python and Go APIs, verifier options, metadata discovery, and token
exchange boundaries.

## Troubleshooting

- `issuer and resource must use HTTPS`: a public deployment has an HTTP issuer
  or resource; use `https://` and a valid TLS Secret.
- `connector has non-HTTPS token_endpoint`: the provider endpoint is HTTP;
  expose Keycloak through trusted HTTPS or use the shortcut only in local test
  mode.
- discovery connection refused from the auth pod: do not point the pod at the
  node's public IP. Use a reachable, trusted HTTPS service endpoint or fix
  cluster egress/DNS.
- `audience mismatch`: compare `spec.auth.audience` with the canonical MCP URL
  and confirm the bundled auth server resource list reflects current OAuth MCPServers.
- tokens fail after restart: use a persistent RSA signing-key Secret; do not
  rely on the test-mode ephemeral key.

## Next steps

Once the OAuth resource and authorization server are configured, use
[Client Connections](connect-clients.md) to supply the bearer token together
with adapter identity, and follow this guide's verification steps.
[Identity and Authorization](identity-and-authorization.md) explains how the
OAuth subject is bound to the session human during enforcement.
