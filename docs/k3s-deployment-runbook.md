# MCP Runtime k3s Deployment Runbook

Deploy, redeploy, and test MCP Runtime on a public k3s cluster with DNS and
TLS. For the reference topology, see [k3s-on-prem-cluster.md](k3s-on-prem-cluster.md).

## Reference cluster

The public example at `platform.mcpruntime.org` runs on the project's k3s
cluster. Cluster size, node names, and addresses can change; inspect the
selected kubeconfig context with `kubectl get nodes`. The multi-node topology
in [k3s-on-prem-cluster.md](k3s-on-prem-cluster.md) is a reference design; the
live example may have a different node count.

## Obtain and select cluster access

For a provider-managed cluster, use the provider's supported login/configure
command to write a kubeconfig context; see the per-distribution overview in
[Deployment Targets](deployment-targets.md#get-a-kubeconfig-for-the-target-distribution).
For this self-managed k3s cluster, use the isolated production file
`$HOME/.kube/prod-mcp-runtime-config` when it is provisioned. Keep the default
`~/.kube/config` on the contributor test context and do not merge production
credentials into it. If the production file is missing, an authorized
operator can copy the k3s server kubeconfig from the control-plane VM:

```bash
source config/deployments/mcpruntime-org.env
install -d -m 700 "$HOME/.kube"
PROD_KUBECONFIG="$HOME/.kube/prod-mcp-runtime-config"
scp "root@${MCP_PRODUCTION_SSH_HOST}:/etc/rancher/k3s/k3s.yaml" "$PROD_KUBECONFIG"
chmod 600 "$PROD_KUBECONFIG"

# Use the control-plane API address reachable from this workstation. This
# changes only the endpoint; keep certificate-authority-data and TLS checks.
CLUSTER_NAME="$(kubectl --kubeconfig "$PROD_KUBECONFIG" config view --minify \
  -o jsonpath='{.clusters[0].name}')"
kubectl --kubeconfig "$PROD_KUBECONFIG" config set-cluster "$CLUSTER_NAME" \
  --server="https://<reachable-control-plane-address>:6443"
```

If SSH targets a worker or the control-plane API is not reachable from the
workstation, obtain the supported API address/network path and a kubeconfig
from the cluster operator. Do not disable TLS verification. Keep this file
outside the repository and private (`chmod 600`).

Select and confirm the context before deploying. Use the isolated production
kubeconfig and explicit context:

```bash
PROD_KUBECONFIG="${PROD_KUBECONFIG:-$HOME/.kube/prod-mcp-runtime-config}"
kubectl --kubeconfig "$PROD_KUBECONFIG" config get-contexts
kubectl --kubeconfig "$PROD_KUBECONFIG" --context prod-mcp-runtime get nodes
```

## Prerequisites

```bash
# Keep the default kubeconfig on test; use the production file explicitly.
PROD_KUBECONFIG="$HOME/.kube/prod-mcp-runtime-config"
kubectl --kubeconfig "$PROD_KUBECONFIG" --context prod-mcp-runtime get nodes

# Build the current CLI from the selected Runtime ref.
make build
./bin/mcp-runtime --version
```

Production setup commands use the selected `PROD_KUBECONFIG` explicitly; do
not export it as the workstation's default kubeconfig.

## Required environment variables

Saved deployment profile (committed template + local override):

```bash
cp config/deployments/mcpruntime-org.env.example config/deployments/mcpruntime-org.env
# edit mcpruntime-org.env, see the Environment variable reference below
```

`config/deployments/mcpruntime-org.env` is gitignored. The `.example` file is the
team-shared template; your local `.env` holds workstation-specific paths.

The hack scripts under `hack/deploy/mcpruntime-org/` source this file by
default. Override the path with `MCP_DEPLOY_ENV=/path/to/other.env`. See
`hack/README.md` for the full layout.

### Environment variable reference

#### Cluster access and domain

| Variable | Required | Used by | Purpose |
|----------|----------|---------|---------|
| `KUBECONFIG` | yes | all hack scripts, manual `kubectl` | Path to the selected cluster kubeconfig. Must match the cluster you target. |
| `MCP_KUBE_CONTEXT` | optional | CLI setup and public hack scripts | Context within the kubeconfig; use when the file has multiple clusters. |
| `MCP_SETUP_KUBECONFIG` | yes (setup) | `hack/deploy/mcpruntime-org/setup.sh` | Same as `KUBECONFIG`; passed to `mcp-runtime setup --kubeconfig`. |
| `MCP_PLATFORM_DOMAIN` | yes | setup | Apex domain only (no `https://`). Derives `registry.`, `mcp.`, and `platform.` hostnames. |
| `MCP_PLATFORM_ADMIN_EMAIL` | yes (non-test setup) | setup | Seeds the platform admin account during bootstrap. |

#### Image build and registry pulls

| Variable | Required | Used by | Purpose |
|----------|----------|---------|---------|
| `MCP_IMAGE_PLATFORM` | strongly recommended | setup, rollout | Target OS/arch for platform images (for example `linux/amd64` when nodes are amd64). |
| `MCP_REGISTRY_ENDPOINT` | yes (`bundled-https`) | setup, rollout (via configmap patch) | Hostname nodes use to **pull** platform and tenant images. With public TLS, set to `registry.<domain>`; **do not** use the registry Service ClusterIP. |
| `MCP_REGISTRY_INGRESS_HOST` | optional | rollout, CLI build/push | Public registry hostname for `docker push` / `server push`. Defaults from `MCP_PLATFORM_DOMAIN` when unset. |
| `MCP_REGISTRY_HOST` | do not set | — | Public ingress hostname; derived from `MCP_PLATFORM_DOMAIN`. Do not use as the internal pull URL. |
| `MCP_REGISTRY_INTERNAL` | optional | rollout | Override registry ClusterIP:port for **build/push** inside rollout script only. Pull path still uses `MCP_REGISTRY_ENDPOINT` in configmap. |
| `MCP_REGISTRY_PUSH_MODE` | `internal` | rollout | `public` pushes directly to `registry.<domain>` using the workstation's selected Docker daemon. |
| `MCP_UPDATE_MCP_AUTH` | `0` | rollout | Set to `1` only when updating the bundled authorization server. |
| `MCP_AUTH_IMAGE_SOURCE` | `published` | rollout | `published` pulls Docker Hub `latest`; choose `local` only when intentionally testing a selected mcp-auth source ref. |
| `MCP_AUTH_DOCKERHUB_IMAGE` | `docker.io/princekrroshan01/mcp-auth-server:latest` | rollout | Published image source, copied to the Runtime registry under a unique candidate tag. |
| `MCP_AUTH_SOURCE` | sibling `mcp-auth` checkout | rollout | Source checkout, used only with `MCP_AUTH_IMAGE_SOURCE=local`. |
| `MCP_AUTH_BUILD_REF` | required for local source | rollout | Selected branch, tag, or commit; rollout requires a clean checkout at this ref. |
| `MCP_AUTH_IMAGE_TAG` | `<MCP_ROLLOUT_TAG>-auth` | rollout | Unique tag for the candidate mcp-auth image. |
| `MCP_AUTH_CLIENT_ID_METADATA_ENABLED` | `true` (mcp-auth default) | rollout | Optional mcp-auth setting; set to `false` to disable CIMD. Verify authorization-server metadata advertises the feature after rollout. |
| `MCP_AUTH_CLIENT_ID_METADATA_HOSTS` | unset | rollout | Optional comma-separated CIMD metadata host allowlist; an empty list allows public hosts and still applies mcp-auth's SSRF checks. |

#### Setup behavior (read by `hack/deploy/mcpruntime-org/setup.sh`)

| Variable | Default | Purpose |
|----------|---------|---------|
| `MCP_SETUP_WAIT_TIMEOUT` | `900` | Seconds to wait for setup rollouts. |
| `MCP_CERT_TIMEOUT` | `5m` (CLI default) | Certificate issuance wait on first install. Use `15m` on fresh clusters. |
| `MCP_SETUP_PLATFORM_MODE` | `tenant` | Passed to `setup --platform-mode`. |
| `MCP_SETUP_REGISTRY_MODE` | `bundled-https` | Passed to `setup --registry-mode`. |
| `MCP_SETUP_INGRESS` | `none` | `none` when k3s Traefik in `kube-system` already serves ingress. |
| `MCP_SETUP_TLS_CLUSTER_ISSUER` | `letsencrypt-prod` | ClusterIssuer name on reruns. **Do not** pass `--acme-email` when this issuer already exists. |
| `MCP_SETUP_SKIP_CERT_MANAGER_INSTALL` | unset | Set to `1` when cert-manager is already installed (typical reruns). |

#### k3s Traefik integration (written to `mcp-sentinel-config`)

| Variable | Typical value | Purpose |
|----------|---------------|---------|
| `PLATFORM_TRAEFIK_NAMESPACE` | `kube-system` | Namespace of the live Traefik deployment on k3s. |
| `PLATFORM_TEAM_TRAEFIK_WATCH` | `disabled` | Prevents `team create` from patching repo-managed `traefik/traefik` when k3s Traefik is external. |

#### Browser sign-in (public / tenant UI)

| Variable | Required | Purpose |
|----------|----------|---------|
| `GOOGLE_CLIENT_ID` | yes (public TLS) | Google OAuth client for dashboard sign-in. |
| `MCP_GOOGLE_CLIENT_ID` | optional | Alias for `GOOGLE_CLIENT_ID`. |
| `OIDC_ISSUER` | optional | Non-Google provider; setup fills Google defaults when `GOOGLE_CLIENT_ID` is set. |
| `OIDC_AUDIENCE` | optional | OIDC audience; defaults to Google client ID. |
| `OIDC_JWKS_URL` | optional | JWKS URL for token validation; discovered from `OIDC_ISSUER` when omitted and the provider publishes OIDC metadata. |

#### MCP OAuth authorization server

Applies to MCP servers with `spec.auth.mode: oauth`. These variables configure
the authorization server that MCP clients use. Browser sign-in above configures
OIDC for the dashboard.

| Variable | Required | Purpose |
|----------|----------|---------|
| `MCP_SETUP_MCP_AUTH_ISSUER_URL` | optional | Public HTTPS issuer URL; defaults to `https://auth.<MCP_PLATFORM_DOMAIN>/mcp-auth`. |
| `MCP_SETUP_MCP_AUTH_RESOURCE_URL` | optional | Initial canonical MCP resource URL; any supplied value must exactly match a protected server's `spec.auth.audience`. The operator reconciles the accepted list from current OAuth MCPServers. |
| `MCP_SETUP_MCP_AUTH_CONNECTORS_FILE` | required when enabled | Provider-neutral connector JSON; client secrets are referenced by environment variable, never stored in this file. |
| `MCP_SETUP_MCP_AUTH_CONNECTOR` | required when enabled | Named connector selected by the mcp-auth server. |
| `MCP_SETUP_MCP_AUTH_TLS_SECRET` | optional | Override the managed TLS Secret for the authorization-server hostname; required only with `--provided-tls-secrets`. |
| `MCP_SETUP_MCP_AUTH_SIGNING_KEY_SECRET` | required when enabled | Persistent RSA signing-key Secret containing `private-key.pem`. |

The issuer must be the exact public URL configured for the optional
`mcp-auth-server`, normally `https://auth.<domain>/mcp-auth`. This
authorization-server hostname is separate from dashboard OIDC and from the
Runtime gateway. The identity provider hostname, realm, client, users,
redirect URI, scopes, and certificates are operated by the platform user.

Discovery is served at the authorization-server metadata URL:

```bash
curl -s https://auth.<domain>/.well-known/oauth-authorization-server/mcp-auth
```

The protected MCP resource separately publishes Protected Resource Metadata;
clients should follow its `WWW-Authenticate` challenge or query the resource
metadata URL generated for that server.

A ready-to-adapt protected server definition is in `examples/oauth-example-go-2025-11-25/.mcp/servers.yaml`. Its
`auth.issuerURL` defaults from the bundled issuer configured during setup.
`auth.audience` must be the
server's canonical resource URI (`https://mcp.<domain>/<prefix>/mcp`). The
gateway fails closed with 401 when a token's `aud` does not match.

#### Optional bundled MCP authorization server

MCP authorization is optional. Without it, an MCP client connects to a server
with no bearer token. Enable it when the server needs standards-based user
login, PKCE, token issuance, and Protected Resource Metadata discovery. The
bundled `mcp-auth-server` is the OAuth authorization server: it authenticates
users through one external OIDC identity provider such as Keycloak and issues
MCP access tokens. Runtime governance decisions stay in the gateway.

The Runtime gateway is the protected-resource boundary. It verifies the
issuer, signature, audience/resource, expiry, and scope, then applies grants,
agent sessions, trust, and tool policy. The authorization server sees only
login and token requests; Runtime policy needs the MCP JSON-RPC tool call and
current grant/session state, so the two stay separate.

For a public test deployment, create DNS records for two hosts pointing to the
ingress node:

```text
keycloak.<domain>  -> <public ingress IP>
auth.<domain>      -> <public ingress IP>
```

Deploy Keycloak with a realm such as `mcp-runtime` and a confidential client
named `mcp-auth`. Configure this exact upstream callback URI:

```text
https://auth.<domain>/mcp-auth/identity/callback
```

The connector file references the Keycloak issuer and client but never stores
the client secret:

```json
{
  "keycloak": {
    "issuer": "https://keycloak.<domain>/realms/mcp-runtime",
    "authorization_endpoint": "https://keycloak.<domain>/realms/mcp-runtime/protocol/openid-connect/auth",
    "token_endpoint": "https://keycloak.<domain>/realms/mcp-runtime/protocol/openid-connect/token",
    "jwks_uri": "https://keycloak.<domain>/realms/mcp-runtime/protocol/openid-connect/certs",
    "client_id": "mcp-auth",
    "client_secret_env": "KEYCLOAK_CLIENT_SECRET",
    "exchange_client_id": "mcp-auth",
    "scopes": ["openid", "profile", "email"],
    "mcp_scopes": ["tools:read"],
    "identity_claims": ["preferred_username"],
    "token_endpoint_auth_method": "client_secret_post",
    "allowed_upstream_callback_uris": [
      "https://auth.<domain>/mcp-auth/identity/callback"
    ],
    "downstream_token_strategy": "upstream_session"
  }
}
```

Deploy the optional bundled server through normal setup:

```bash
./bin/mcp-runtime setup \
  --with-tls --tls-cluster-issuer letsencrypt-prod \
  --with-mcp-auth-server \
  --mcp-auth-signing-key-secret mcp-auth-signing-key \
  --mcp-auth-connectors-file /secure/mcp-auth-connectors.json \
  --mcp-auth-connector keycloak
```

The issuer defaults to `https://auth.<MCP_PLATFORM_DOMAIN>/mcp-auth`, and the
operator derives `auth.issuerURL` and reconciles accepted resource audiences
from OAuth MCPServers. Any optional `--mcp-auth-resource-url` must exactly
match an MCPServer's `spec.auth.audience`. Production requires a certificate
covering the auth host (provisioned by the configured TLS
ClusterIssuer), a selected connector, and a persistent RSA signing key
stored in the Secret key `private-key.pem`. Use `--mcp-auth-tls-secret` only
for an externally managed certificate. The connector's
`KEYCLOAK_CLIENT_SECRET` value is read from the environment and converted into
a Kubernetes Secret; it must not be committed to Git.

Setup waits for the current auth and operator Deployment revisions to finish
rolling out. A healthy old auth pod does not prove the new image or resource
allowlist is active. If authorization returns `resource is not recognized`,
compare `MCP_AUTH_RESOURCES` on the serving pods with the Deployment and inspect
replacement pod startup logs. A published image that rejects connector fields
must be replaced with a compatible image; preserve the connector config,
signing key, and data PVC during recovery.
`cluster doctor` checks the current auth revision, observed generation, and
replacement replicas; API access failures are reported rather than treated as
an absent optional auth installation.

The bundled server uses SQLite on a PVC in production and memory storage only
in `--test-mode`. Test mode also permits the loopback development issuer and an
ephemeral signing key. A public deployment must use HTTPS for Keycloak's
issuer, authorization endpoint, token endpoint, and JWKS endpoint. Use internal
HTTP only for local testing.

#### Production backups

Before a production `setup` redeployment, capture resources and credentials
that setup may reapply. The snapshot includes PVC/PV definitions, but does not
copy live volume contents because setup does not delete claims or their data:

```bash
hack/deploy/mcpruntime-org/backup.sh --setup
```

The command requires the isolated `prod-mcp-runtime` context. It creates a
timestamped snapshot under
`~/.mcpruntime/backups/mcpruntime-org/` and updates `latest` only after all
parts validate. The snapshot directory is mode `0700`; files containing
Secrets are mode `0600`. Setup merges existing resources and reuses matching
PVCs, but may update config/Secrets and roll workloads. An immutable
StatefulSet change can recreate that StatefulSet while leaving its claims in
place. The resource inventory is for recovery reference, not bulk
`kubectl apply`; its selected platform files are consumed by the existing
setup restore path.

For destructive cleanup or full node recovery, create a full backup:

```bash
hack/deploy/mcpruntime-org/backup.sh --full --online-copy
```

The full snapshot also requires the configured SSH host and captures K3s
control-plane state and local-path volume files without stopping workloads.

A full snapshot includes namespaced and cluster-scoped Kubernetes objects,
CRDs, Secrets, grants, sessions, PV/PVC specs, a consistent SQLite online
backup of the K3s control-plane database, `/etc/rancher/k3s`, K3s server
credentials, and every file under `/var/lib/rancher/k3s/storage`. That volume
archive includes Postgres, ClickHouse, Kafka, Keycloak, mcp-auth,
observability, and registry data. It also contains the TLS and platform
config/Secret files used by the existing setup restore path, plus a SHA-256
manifest.

The bundle is **not encrypted by the backup command**; file permissions reduce
local access but do not protect it from device loss or disk compromise. Encrypt
it with the team's approved storage before copying it off-host.

The full backup runs without stopping workloads. Kubernetes objects and SQLite
state are captured online; PVC files are copied live and are not guaranteed to
be an application-consistent point-in-time image. Databases may need WAL
recovery after restore. Store this bundle on encrypted storage and copy it off
the production node and workstation. Full node recovery restores the K3s state
and volume archive on the original node. Validate the SHA-256 manifest before
restoring.

For a full node restore, provision the same host and K3s version, stop K3s,
extract `k3s-host-and-pv-data.tar.gz` at `/`, copy `k3s-state.db` to
`/var/lib/rancher/k3s/server/db/state.db` with owner `root:root` and mode `0600`,
then start K3s and validate node and workload readiness. Keep the existing
server token and `/etc/rancher/k3s` configuration from the archive. Verify the
bundle first with `cd ~/.mcpruntime/backups/mcpruntime-org/latest && shasum -a
256 -c SHA256SUMS`. Check each database's recovery logs before accepting
traffic. This does not restore an external identity provider or data stored
outside the cluster.

`hack/deploy/mcpruntime-org/clean.sh` still takes a smaller **platform-runtime
restore snapshot** before its intentional namespace wipe. It covers platform
TLS/config/bootstrap material, not the full node or PVC data.

| Variable | Default | Purpose |
|----------|---------|---------|
| `MCP_TLS_BACKUP_DIR` | `~/.mcpruntime/backups/mcpruntime-org` | Root directory for timestamped setup or full snapshots. |
| `MCP_RESTORE_TLS_AFTER_SETUP` | `1` | When `1`, `hack/deploy/mcpruntime-org/setup.sh` runs `hack/deploy/mcpruntime-org/restore.sh` after setup. |
| `MCP_DEPLOY_ENV` | `config/deployments/mcpruntime-org.env` | Env file path for all hack scripts. |

The `clean.sh` snapshot covers TLS, cert-manager, OIDC, and bootstrap secrets.
Use `backup.sh --setup` before setup, or `backup.sh --full --online-copy` for
the Kubernetes object and persistent volume recovery bundle described above.

#### Rollout-only (`hack/deploy/mcpruntime-org/rollout.sh`)

| Variable | Default | Purpose |
|----------|---------|---------|
| `MCP_ROLLOUT_TAG` | `verify-MMDDHHMM` | Image tag for API/UI build and push. |

#### Multitenancy test (`hack/deploy/mcpruntime-org/multitenancy-test.sh`)

These are **not** in the deployment profile. Export them when you run the test against production URLs:

| Variable | Example | Purpose |
|----------|---------|---------|
| `PLATFORM_URL` | `https://platform.mcpruntime.org` | Platform API base (no trailing slash). |
| `MCP_URL` | `https://mcp.mcpruntime.org` | Public MCP ingress base. |
| `REGISTRY_HOST` | `registry.mcpruntime.org` | Registry hostname for tenant image build/push. |
| `ADMIN_EMAIL` / `ADMIN_PASSWORD` | test admin creds | Platform admin login when not using token. |
| `ADMIN_TOKEN` | optional | Admin API token instead of password login. |
| `VERIFY_DEPLOY_PULL_SECRET` | `0` | When `1`, retain the supplied kubeconfig for read-only checks that `server deploy` created the namespace-local image pull Secret and attached it to `mcp-workload`. |

The test script clears `KUBECONFIG` for CLI tenant flows, so `server build`,
`server push`, and `server deploy` exercise the platform API without admin
cluster access. To also assert the k3s namespace pull Secret after each deploy,
set `VERIFY_DEPLOY_PULL_SECRET=1` and provide the production `KUBECONFIG`; the
script uses it only for those read-only Secret and ServiceAccount checks.

#### Do not set on this TLS production cluster

| Variable | Why |
|----------|-----|
| `MCP_REGISTRY_ENDPOINT=10.x.x.x:5000` | ClusterIP breaks `bundled-https` TLS cert validation on pod pulls. |
| `MCP_ACME_EMAIL` on reruns | Re-applies Let's Encrypt issuer and can trigger duplicate-cert rate limits. Use `MCP_SETUP_TLS_CLUSTER_ISSUER` instead. |
| `MCP_RUNTIME_TEST_MODE=1` | Dev/test-mode guardrails; omit for production-shaped installs. |

#### Minimal profile example

```bash
PROD_KUBECONFIG="$HOME/.kube/prod-mcp-runtime-config"
export MCP_PLATFORM_DOMAIN=mcpruntime.org
export MCP_IMAGE_PLATFORM=linux/amd64
export MCP_PLATFORM_ADMIN_EMAIL=admin@example.com
export MCP_REGISTRY_ENDPOINT=registry.mcpruntime.org
export GOOGLE_CLIENT_ID=<google-oauth-client-id>
```

See `config/deployments/mcpruntime-org.env.example` for the full saved profile used by the hack scripts.

## Step 0: Back up platform-runtime state before any wipe

Let's Encrypt enforces a **5 duplicate-certificate / 7 days per domain** rate
limit. Use the helper script to back up platform-runtime material (TLS,
cert-manager ownership, OIDC, bootstrap secrets) before wiping app namespaces:

```bash
hack/deploy/mcpruntime-org/clean.sh --yes --wait
```

The backup covers platform-runtime state only. Tenant/user data (teams,
Postgres identity store, MCP CRs, registry images) is **not** preserved. See
[Deployment Targets - k3s Production](deployment-targets.md#option-a-bundled-https-registry-on-prem-reference).

Manual TLS-only backup (legacy):

```bash
kubectl get secret registry-tls -n registry -o yaml \
  > /tmp/registry-tls-backup.yaml 2>/dev/null || true
kubectl get secret mcp-sentinel-platform-tls -n mcp-sentinel -o yaml \
  > /tmp/platform-tls-backup.yaml 2>/dev/null || true
```

Restore after setup (prefer automatic restore via `hack/deploy/mcpruntime-org/setup.sh`):

```bash
hack/deploy/mcpruntime-org/restore.sh
# or from clean.sh:
hack/deploy/mcpruntime-org/clean.sh --restore-platform
```

## Safe cluster wipe (app workloads only)

Delete only app namespaces. Deleting kube-system resources breaks k3s's
reconciliation loop: CoreDNS, Traefik, svclb-traefik, and
local-path-provisioner cannot recover without an SSH restart.

```bash
# 1. Back up TLS secrets (see Step 0)

# 2. Delete only app namespaces, and leave kube-system untouched
kubectl get ns --no-headers \
  | awk '{print $1}' \
  | grep -Ev '^(kube-system|kube-public|kube-node-lease|default)$' \
  | xargs -r kubectl delete ns --grace-period=0

# 3. Delete cluster-scoped MCP resources
kubectl delete mcpserver,mcpaccessgrant,mcpagentsession \
  --all -A --ignore-not-found 2>/dev/null || true
kubectl delete clusterrole,clusterrolebinding \
  -l app.kubernetes.io/managed-by=mcp-runtime \
  --ignore-not-found 2>/dev/null || true
```

### If you accidentally wiped kube-system

If kube-system pods are gone (no CoreDNS, no Traefik), restart k3s on the
control plane to trigger full reconciliation from
`/var/lib/rancher/k3s/server/manifests/`:

```bash
ssh root@103.181.176.28 "systemctl restart k3s"
# Wait for CoreDNS, Traefik, and svclb pods to come up
kubectl wait pod -n kube-system \
  -l app.kubernetes.io/name=traefik \
  --for=condition=Ready --timeout=120s
```

Verify port 80 is reachable before running setup with TLS:

```bash
curl -sm5 http://registry.mcpruntime.org/ && echo "port 80 OK"
# Expected: "404 page not found" from Traefik
```

## Setup

### First install (creates Let's Encrypt ClusterIssuer and certificates)

```bash
cp config/deployments/mcpruntime-org.env.example config/deployments/mcpruntime-org.env
# add GOOGLE_CLIENT_ID to mcpruntime-org.env when browser sign-in is required

export MCP_PLATFORM_ADMIN_EMAIL=admin@example.com

MCP_SETUP_WAIT_TIMEOUT=900 MCP_CERT_TIMEOUT=15m \
./bin/mcp-runtime setup \
  --kubeconfig "$KUBECONFIG" \
  --with-tls \
  --acme-email ops@example.com \
  --ingress none \
  --registry-mode bundled-https \
  --platform-mode tenant
```

### Reruns / upgrades (reuse existing certs to avoid LE rate limits)

When cert-manager already issued `registry-cert` and
`mcp-sentinel-platform-tls`, **do not** pass `--acme-email` again. Use the saved
profile and helper script:

```bash
hack/deploy/mcpruntime-org/setup.sh
```

For code-only changes (registry push, team create, API fixes) without a full
platform rebuild, use the targeted Sentinel rollout:

```bash
hack/deploy/mcpruntime-org/rollout.sh
```

That rebuilds/pushes the three split API images (`mcp-platform-api`,
`mcp-runtime-api`, `mcp-analytics-api`), `mcp-sentinel-ui`, and the doctor
smoke image, patches the production registry/Traefik settings and pull secret,
then waits for rollouts. It does not run setup or request certificates.

Build and push from the workstation's selected Docker daemon. Target the k3s
node architecture explicitly; the example cluster currently runs amd64 nodes.
The rollout script uses `MCP_IMAGE_PLATFORM` for every build and pushes tagged
images directly to the public bundled registry. Kubernetes commands use the
shared `prod-mcp-runtime` kubeconfig context.

```bash
source config/deployments/mcpruntime-org.env
PROD_KUBECONFIG="$HOME/.kube/prod-mcp-runtime-config"
docker info --format '{{.Name}} {{.OSType}}/{{.Architecture}}'
RUNTIME_COMMIT="$(git rev-parse --short HEAD)"
ROLLOUT_TAG="prod-$(date -u +%Y%m%dT%H%M%S)-${RUNTIME_COMMIT}"

make build
KUBECONFIG="$PROD_KUBECONFIG" ./bin/mcp-runtime cluster doctor
KUBECONFIG="$PROD_KUBECONFIG" MCP_SETUP_KUBECONFIG="$PROD_KUBECONFIG" \
MCP_KUBE_CONTEXT=prod-mcp-runtime MCP_IMAGE_PLATFORM=linux/amd64 \
MCP_REGISTRY_PUSH_MODE=public \
MCP_ROLLOUT_TAG="$ROLLOUT_TAG" \
bash hack/deploy/mcpruntime-org/rollout.sh
```

The script uses a temporary Docker credential directory and removes it when it
exits. It reads the registry credential from the existing Kubernetes Secret;
the key is not printed or stored in the deployment profile. Record the previous
image tags before rollout so they remain available for rollback.

By default, rollout leaves the mcp-auth Deployment on its current release. To
deploy the published Docker Hub image, opt in to copying `latest` into the
Runtime registry under a unique tag:

```bash
MCP_UPDATE_MCP_AUTH=1 \
MCP_AUTH_IMAGE_SOURCE=published \
MCP_AUTH_IMAGE_TAG="prod-$(date -u +%Y%m%dT%H%M%S)-auth" \
MCP_REGISTRY_PUSH_MODE=public \
MCP_ROLLOUT_TAG="$ROLLOUT_TAG" \
bash hack/deploy/mcpruntime-org/rollout.sh
```

To test mcp-auth source changes, set the source checkout path and selected
ref in the rollout environment. The workstation's
Docker daemon receives the local build context and builds for the target
platform. First confirm the selected ref is checked out and the worktree is
clean:

```bash
MCP_AUTH_SOURCE=/path/to/mcp-auth
MCP_AUTH_BUILD_REF=issue-13-cimd # replace with the selected branch, tag, or commit
MCP_AUTH_COMMIT="$(git -C "$MCP_AUTH_SOURCE" rev-parse --short "$MCP_AUTH_BUILD_REF")"
MCP_UPDATE_MCP_AUTH=1 \
MCP_AUTH_IMAGE_SOURCE=local \
MCP_IMAGE_PLATFORM=linux/amd64 \
MCP_AUTH_CLIENT_ID_METADATA_ENABLED=true \
MCP_AUTH_SOURCE="$MCP_AUTH_SOURCE" \
MCP_AUTH_BUILD_REF="$MCP_AUTH_BUILD_REF" \
MCP_AUTH_IMAGE_TAG="prod-$(date -u +%Y%m%dT%H%M%S)-auth-${MCP_AUTH_COMMIT}" \
MCP_REGISTRY_PUSH_MODE=public \
MCP_ROLLOUT_TAG="$ROLLOUT_TAG" \
bash hack/deploy/mcpruntime-org/rollout.sh
```

Local mode builds, while published mode pulls Docker Hub `latest`; both copy
the candidate to `registry.mcpruntime.org/mcp-auth-server:<tag>` and deploy
that immutable candidate ref while
preserving the existing mcp-auth configuration, SQLite PVC, signing key, and
TLS Secret. Do not rerun `setup --with-tls` for an image-only release.

### Separate release tracks and user verification

The Runtime CLI release and the hosted platform images are separate artifacts.
Tagging a Runtime release publishes platform-specific CLI binaries through
`.github/workflows/release.yaml`; it does not update the hosted platform.
The production rollout updates platform APIs/UI (and mcp-auth only when explicitly
selected); it does not publish a new CLI release. Publish either project's
release only after its candidate passes the checks below.

Verify the user path after rollout:

1. Install the candidate CLI built from the selected Runtime ref and confirm
   `mcp-runtime --version` reports that commit. After release publication,
   verify the `releases/latest` binary download reports the release tag.
2. Follow [Quickstart](quickstart.md) against
   `https://platform.mcpruntime.org`: log in, build/push/deploy a temporary
   `qa-audit-*` server, create a grant, call a tool through the adapter, and
   confirm the request appears under **Analytics → Tools** in the platform UI.
3. Check the signed-out platform page and signed-in role-gated UI with browser
   evidence. Use only temporary `qa-audit-*` resources and clean them up.
4. Verify `mcp-runtime status`, server listing, deployment readiness, and
   `cluster doctor`; confirm the existing certificate resources remain Ready.

Users access the same `https://platform.mcpruntime.org` URL after a platform
rollout. They update the CLI separately from the GitHub Releases page. Until a
new tag is published, `releases/latest` still downloads the previously
published CLI.

That sources `config/deployments/mcpruntime-org.env` (or the `.example` template)
and runs setup with `--tls-cluster-issuer letsencrypt-prod` and
`--skip-cert-manager-install`. Existing certificates stay on the same revision
when SANs are unchanged.

Equivalent manual command:

```bash
set -a && source config/deployments/mcpruntime-org.env && set +a
MCP_SETUP_WAIT_TIMEOUT=900 ./bin/mcp-runtime setup \
  --kubeconfig "$KUBECONFIG" \
  --with-tls \
  --tls-cluster-issuer letsencrypt-prod \
  --skip-cert-manager-install \
  --ingress none \
  --registry-mode bundled-https \
  --platform-mode tenant
```

**Why no `--test-mode`:** CI does not publish pre-built container images, so
every deployment builds operator/gateway/Sentinel images from the source tree.
Without `--test-mode`, setup requires `MCP_PLATFORM_ADMIN_EMAIL` and is
otherwise identical. At run time, `--test-mode` only sets
`MCP_RUNTIME_TEST_MODE=1` inside deployed pods. For a production deployment
without that flag, set the admin email env var above.

**Flag notes:**
- `MCP_PLATFORM_DOMAIN=mcpruntime.org`: derives `registry.`, `mcp.`, and
  `platform.` hostnames. Do not also export a registry ClusterIP as
  `MCP_REGISTRY_ENDPOINT`.
- `MCP_PLATFORM_ADMIN_EMAIL`: required by non-test-mode setup validation;
  seeds the platform admin account in the `mcp-sentinel-secrets` Secret.
- `--ingress none`: k3s already runs Traefik in `kube-system`, so setup skips
  the second ingress stack. Setup sets `PLATFORM_TRAEFIK_NAMESPACE=kube-system`
  and `PLATFORM_TEAM_TRAEFIK_WATCH=disabled` so `team create` does not patch
  k3s Traefik (it watches ingresses cluster-wide).
- `--registry-mode bundled-https`: bundled registry with TLS ingress at
  `registry.mcpruntime.org`.
- `--tls-cluster-issuer letsencrypt-prod` (reruns): reuses the existing
  ClusterIssuer; cert-manager keeps current certs when specs are unchanged.
- `--acme-email` (first install only): creates/applies the Let's Encrypt
  ClusterIssuer. Omit it on reruns to avoid duplicate ACME orders.
- `MCP_CERT_TIMEOUT=15m`: extends the default 5-minute certificate-issuance
  wait on a fresh cluster.
- `--kubeconfig`: pass it explicitly when multiple kubeconfig files exist on
  the workstation. TLS and cert-manager operations use a package-level client
  that requires the explicit path, so the `KUBECONFIG` env var alone is not
  enough (see `internal/cli/setup/platform/kube_client.go`).

If setup reports "cert-manager already installed" but TLS issuance times out,
check two things: (1) Traefik serves port 80; (2) cert-manager pods are
Running. The "already installed" check tests only for the CRDs. After a k3s
restart the CRDs survive, but the pods may be gone. Reinstall manually if
needed:
```bash
kubectl get pods -n cert-manager
# If not running:
curl -sL https://github.com/cert-manager/cert-manager/releases/download/v1.16.2/cert-manager.yaml \
  | kubectl apply -f -
kubectl wait pod -n cert-manager --all --for=condition=Ready --timeout=120s
```

### Post-setup check

```bash
./bin/mcp-runtime cluster diagnostics

# Confirm TLS certs are Ready
kubectl get certificate registry-cert -n registry
kubectl get certificate -n mcp-sentinel

# Check Sentinel pods
kubectl get pods -n mcp-sentinel
```

Expected: all mcp-sentinel pods `1/1 Running`, certificate `READY=True`.

## Tenant push and deploy smoke test

After setup, verify a non-admin team member can publish and deploy:

```bash
ADMIN_KEY="$(kubectl get secret mcp-sentinel-secrets -n mcp-sentinel \
  -o jsonpath='{.data.ADMIN_API_KEYS}' | base64 -d | cut -d, -f1)"

# Admin: create team + user
MCP_PLATFORM_API_URL=https://platform.mcpruntime.org MCP_PLATFORM_API_TOKEN="$ADMIN_KEY" \
  ./bin/mcp-runtime team create myteam --name "My Team"
MCP_PLATFORM_API_URL=https://platform.mcpruntime.org MCP_PLATFORM_API_TOKEN="$ADMIN_KEY" \
  ./bin/mcp-runtime team user create myteam \
  --email member@example.com --password 'YourPassword123!' --role member

# Team member: login, build, push, deploy from metadata
MCP_PLATFORM_API_URL=https://platform.mcpruntime.org \
  ./bin/mcp-runtime auth login --email member@example.com --password 'YourPassword123!' \
  --profile myteam-user

cd examples/oauth-example-go-2025-11-25
# .mcp/servers.yaml already exists in the example; for a new server run:
# ../../bin/mcp-runtime server init <name> --tool <tool> --metadata-dir .mcp

MCP_PLATFORM_API_URL=https://platform.mcpruntime.org MCP_PLATFORM_API_PROFILE=myteam-user \
  ../../bin/mcp-runtime server build image oauth-example-go-2025-11-25-gateway \
  --metadata-dir .mcp \
  --tag verify-e2e \
  --platform linux/amd64

IMAGE_REF="$(awk '$1=="image:"{i=$2} $1=="imageTag:"{t=$2} END{print i ":" t}' .mcp/servers.yaml)"

MCP_PLATFORM_API_URL=https://platform.mcpruntime.org MCP_PLATFORM_API_PROFILE=myteam-user \
  ../../bin/mcp-runtime server push --scope tenant --image "$IMAGE_REF"

MCP_PLATFORM_API_URL=https://platform.mcpruntime.org MCP_PLATFORM_API_PROFILE=myteam-user \
  ../../bin/mcp-runtime server deploy oauth-example-go-2025-11-25-gateway \
  --scope tenant \
  --metadata-dir .mcp
```

Expected: push succeeds in under ~30s; deploy reports `status Ready`; the team
namespace contains `mcp-runtime-registry-pull` and a running MCPServer pod.
This platform CLI path also verifies that the API provisions a namespace-local
registry pull Secret and attaches it for workload pulls. Keep this `server push`
and `server deploy` smoke in k3s production QA. `kubectl apply` alone bypasses
the platform's namespace and pull-secret provisioning path.
The `.mcp` metadata must contain `tools[*].sideEffect`; `server deploy` copies
that metadata into the platform request so governed `tools/call` requests can
authorize side effects.

If `team create` returns `500 failed to provision team namespace`, confirm
`PLATFORM_TEAM_TRAEFIK_WATCH=disabled` is present in `mcp-sentinel-config`
(or set it in `config/deployments/mcpruntime-org.env` before rerunning setup).

## Multi-tenancy end-to-end test

```bash
hack/deploy/mcpruntime-org/multitenancy-test.sh
```

Default assumptions:
- `PLATFORM_URL=https://platform.mcpruntime.org`
- `MCP_URL=https://mcp.mcpruntime.org`
- `REGISTRY_HOST=registry.mcpruntime.org` (image build tagging and push target resolution)
- Team owners publish images with `server push --scope tenant` (platform API), not `admin registry push`
- Builds and deploys `acme-tools`, `globex-tools`, and `techcorp-tools` example servers
- Creates Acme, Globex, and TechCorp teams, applies cross-tenant grants
- Verifies adapter success, dashboard events, and no-kubeconfig smoke checks

To skip the build/deploy and only verify an existing setup:

```bash
SKIP_SETUP=1 hack/deploy/mcpruntime-org/multitenancy-test.sh
```

## Troubleshooting

### TLS cert not issued after 5+ minutes

1. `kubectl describe challenge -A`: look for ACME HTTP-01 status
2. `kubectl logs -n cert-manager deploy/cert-manager --tail=60`
3. Check Traefik is serving port 80: `curl -sm5 http://mcp.mcpruntime.org/`
4. Verify DNS: `dig registry.mcpruntime.org +short` should return
   `103.181.177.16`
5. If a stale Certificate owns `registry/registry-tls`, delete it before
   rerunning setup:
   ```bash
   kubectl delete certificate registry-tls -n registry --ignore-not-found
   ```

### Traefik 404 on the registry host / node pulls fail with `NotFound`

Symptom: every request to `https://registry.<domain>/v2/...`, including
anonymous `/v2/`, returns Traefik's plain `404 page not found` instead of
`401`, and new pods fail to pull `registry.<domain>/...` with `NotFound`.
Check the registry Ingress:

```bash
kubectl get ingress registry -n registry \
  -o jsonpath='rules={.spec.rules[*].host} tls={.spec.tls[*].hosts}{"\n"}'
./bin/mcp-runtime cluster doctor   # "registry Ingress hosts" check
```

If `rules=registry.local` while `tls=registry.<domain>`, Traefik has no router
for the public host. This happens when the non-TLS base manifest
(`config/registry/base`, rule host `registry.local`) is applied over a public
install, for example by a registry apply from a shell that lacks
`MCP_PLATFORM_DOMAIN`, or by `kubectl apply -f config/registry/base/ingress.yaml`
(the Kind e2e cache refresh) against the wrong context. The apply replaces
`spec.rules` but keeps the existing `spec.tls`. Setup now resolves the host from
the live Ingress TLS host and `mcp-sentinel-config`, and refuses to apply a
`registry.local` rule host over a public Ingress. Fix a live cluster with the
command doctor prints:

```bash
kubectl patch ingress registry -n registry --type=json \
  -p '[{"op":"replace","path":"/spec/rules/0/host","value":"registry.<domain>"}]'
curl -s -o /dev/null -w '%{http_code}\n' https://registry.<domain>/v2/   # expect 401
```

Also confirm `traefik.ingress.kubernetes.io/router.entrypoints` is `websecure`;
the base manifest sets `web`.

### Setup fails "bundled registry platform setup requires MCP_REGISTRY_ENDPOINT"

You omitted `--test-mode` and used `--registry-mode auto`. For this k3s cluster
use `--registry-mode bundled-https` (included in `hack/deploy/mcpruntime-org/setup.sh`).
Do not export a ClusterIP as `MCP_REGISTRY_ENDPOINT` on the public TLS deployment.

### Setup fails "MCP_IMAGE_PLATFORM does not match Kubernetes node architecture"

Set `MCP_IMAGE_PLATFORM=linux/amd64` (cluster nodes are amd64; local Mac is arm64).

### kube-system empty / HelmChart CRD missing

See **If you accidentally wiped kube-system** above. Restart k3s on the control
plane. Do not re-create the HelmChart CRDs manually.

### Namespaces stuck in Terminating

```bash
for ns in $(kubectl get ns --no-headers | awk '$2=="Terminating"{print $1}'); do
  kubectl get ns "$ns" -o json \
    | jq '.spec.finalizers = []' \
    | kubectl replace --raw "/api/v1/namespaces/$ns/finalize" -f -
done
```

### Let's Encrypt rate limit hit

Restore the backed-up TLS secrets (Step 0). Do not re-request certs:

```bash
kubectl apply -f /tmp/registry-tls-backup.yaml
kubectl apply -f /tmp/platform-tls-backup.yaml
```

Check current usage at <https://crt.sh/?q=mcpruntime.org>.
