# Scoped credential broker

Credential bindings let an MCP tool use an approved upstream credential without
returning it to its human caller. An administrator registers an exact provider
reference; a namespace owner binds that source to a live server UID, explicit
tools, a human, a managed agent and a team. An optional session ID narrows the
binding further. Management requests contain metadata only.

Sources support Kubernetes Secrets, Google Secret Manager, AWS Secrets Manager,
Azure Key Vault and Vault KV v2. Runtime reads the current backing value for each
authorized call. It does not copy values into Postgres, policy ConfigMaps,
workload environment variables or management responses.

## Enable delivery

Use matching CLI, platform API, operator and gateway versions. Refresh an older
installation through supported `setup`, preserving its original host, TLS,
storage, registry and MCP Auth options. Setup installs the credential routes and
the platform API's read-only grant permission. Then configure the verified HTTPS
platform origin with an explicit administrator Kubernetes context:

```bash
mcp-runtime credential configure \
  --broker-url https://platform.example.com \
  --kubeconfig /path/to/cluster.yaml --context selected-context --yes
```

The command checks actual ingress destinations and grant-read permissions
before updating the operator. Missing migration fails without changing it.
The gateway verifies the platform certificate using its system trust roots;
there is no HTTP or insecure-TLS fallback. This feature requires an HTTPS
installation. Setup refreshes retain the configured origin, native cloud
identity labels and canonical Vault projected-token configuration.

Generate server metadata with `server init`. Add `credentialBindings` to that
server's `.mcp/servers.yaml` entry, keeping its normal tool declarations:

```yaml
credentialBindings: [upstream-token]
policy:
  mode: allow-list
  defaultDecision: deny
```

Run `server validate`, then the usual `server build image`, `server push` and
`server deploy` workflow. Binding-enabled servers require an enabled gateway
and enforced allow-list/deny policy; delegated-header and observe modes cannot
use the broker. The upstream must be the declared server's same-Pod loopback
port. The operator renders policy schema v6 with a required platform session,
and projects a ten-minute dedicated broker-audience token into the gateway
container only. Older gateways reject v6 policies.

Until source and binding metadata exist, calls fail closed. Use
`mcp-runtime server get SERVER --namespace NAMESPACE` to read the live server's
`uid` from the supported platform response. Recreated servers require updated
binding metadata naming their new UID.

## Kubernetes source and binding

The backing Secret must already be managed by your secret-management system in
the server namespace. Runtime never asks you to upload its value. An
administrator creates `source.json` using the actual namespace and team UUID:

```json
{
  "namespace": "mcp-team-example",
  "name": "upstream-provider",
  "provider": "kubernetes",
  "resource": "upstream-api-token",
  "key": "token",
  "team_id": "<TEAM_UUID>",
  "revision": 0
}
```

Exactly one `team_id` or `user_id` authorizes a source. Source registration,
updates and deletion require a current platform administrator:

```bash
mcp-runtime credential source apply --file source.json
mcp-runtime credential source authorize-kubernetes upstream-provider \
  --namespace mcp-team-example \
  --kubeconfig /path/to/cluster.yaml --context selected-context --yes
```

The permission command never reads Secret data. It creates a namespace Role and
RoleBinding for `mcp-platform-api`, with GET and `resourceNames` limited to that
exact Secret. It refuses to replace different existing permissions.
Cross-namespace Secret names and broad Secret permissions are unsupported.

A namespace owner authorized by that source creates `binding.json`. Replace the
IDs with the current human, managed agent, team and server UID:

```json
{
  "namespace": "mcp-team-example",
  "name": "upstream-token",
  "source": "upstream-provider",
  "server": "example",
  "server_uid": "<LIVE_SERVER_UID>",
  "tools": ["search"],
  "human_id": "<HUMAN_UUID>",
  "agent_id": "<MANAGED_AGENT_ID>",
  "team_id": "<TEAM_UUID>",
  "header": "X-Mcp-Credential-Token",
  "revision": 0
}
```

```bash
mcp-runtime credential binding apply --file binding.json
mcp-runtime credential binding list --namespace mcp-team-example
mcp-runtime credential binding get upstream-token --namespace mcp-team-example
```

Bindings require current personal-namespace ownership or team-owner membership,
plus source authority. Team members may inspect authorized metadata but cannot
manage the team's bindings. The human and managed agent must remain active in
the declared team. `tools` contains explicit unique names without wildcards.
Optional `session_id` is the exact `namespace/name` session key.

Bindings for different tools may coexist; every binding applicable to a called
tool must authorize the current subject. Conflicting delivery headers fail
closed. A tool without an applicable binding receives no broker credential.

## GCP source

Configure [Workload Identity Federation for GKE](https://docs.cloud.google.com/kubernetes-engine/docs/how-to/workload-identity)
for the existing `mcp-platform/mcp-platform-api` ServiceAccount and grant access
to the exact backing secret. For linked IAM service accounts, the supported
command adds the ServiceAccount annotation and rolls the platform API:

```bash
mcp-runtime credential configure \
  --broker-url https://platform.example.com \
  --gcp-service-account broker@your-project.iam.gserviceaccount.com \
  --kubeconfig /path/to/cluster.yaml --context selected-context --yes
```

Use `provider: "gcp"`,
`resource: "projects/your-project/secrets/upstream-api-token"` and
`version: "latest"` or an explicit version/alias. Omit `key`. The broker uses
Application Default Credentials and the authenticated
[Secret Manager access API](https://docs.cloud.google.com/secret-manager/docs/reference/rest/v1/projects.secrets.versions/access).
It does not accept service-account keys through management APIs. Reuse the same
binding workflow. A new accessible `latest` version is read on the next call;
an unavailable or disabled version denies delivery.

## Other providers

| Provider | Source reference | Workload identity |
|---|---|---|
| `aws` | Exact Secrets Manager ARN; optional `version` is a version stage, default `AWSCURRENT`; omit `key` | Official SDK default chain. `--aws-role-arn ARN` configures the existing ServiceAccount after [EKS IAM federation](https://docs.aws.amazon.com/eks/latest/userguide/iam-roles-for-service-accounts.html) is established. Scope IAM/KMS permission to the exact secret. HTTP data endpoint overrides are rejected. |
| `azure` | `https://VAULT.vault.azure.net/secrets/NAME`; optional version; omit `key` | Official SDK `DefaultAzureCredential`. `--azure-client-id UUID` sets ServiceAccount and Pod identity metadata after [AKS federation](https://learn.microsoft.com/en-us/azure/aks/workload-identity-overview) is configured. Restrict Key Vault read authority. |
| `vault` | KV v2 `MOUNT/PATH`; `key` selects a string field; optional numeric version | `--vault-url https://vault.example.com --vault-role ROLE` configures a ten-minute `mcp-runtime-vault` projected token. The Vault Kubernetes auth role must bind `mcp-platform/mcp-platform-api`, that audience and a narrow KV read policy. The broker uses `/v1/auth/kubernetes/login`, then `/v1/MOUNT/data/PATH`. Use short Vault token TTLs. |

Use these flags with `credential configure`, including `--broker-url`, explicit
`--kubeconfig`, `--context` and `--yes`. Cloud federation and provider IAM are
infrastructure prerequisites; administrator credentials are never uploaded to
Runtime. Vault's HTTPS origin is administrator-selected process configuration,
not a binding-supplied endpoint. Provider data reads use verified HTTPS or the
authenticated Kubernetes API. Missing identity, keys, IAM or provider APIs fail
without returning provider error bodies or values.

## Rotation, revocation and cleanup

Rotate values in their backing provider. Kubernetes reads use current Secret
data; unpinned cloud/Vault references read the current version per call, without
server redeployment. To change metadata, save `get` output, edit permitted fields
and apply it with its returned `revision`. Stale updates/deletes return conflict.
A Kubernetes reference change may require revoking its old exact-Secret grant
before authorizing the new reference.

Disable a binding or source with an exact-revision update to stop subsequent
delivery. Current account, managed-agent, team membership, session revocation,
expiry and current grant restrictions are checked for each resolution.
Grant/server policy is checked again after provider I/O. CRUD and runtime-use
audits must commit before a value is released. Audit records contain identities,
namespace, binding name and outcome, never provider payloads or values.

Delete bindings first, then revoke a Kubernetes source's permission before
deleting its metadata. Use the actual current revisions:

```bash
mcp-runtime credential binding delete upstream-token \
  --namespace mcp-team-example --revision 1 --yes
mcp-runtime credential source authorize-kubernetes upstream-provider \
  --namespace mcp-team-example --revoke \
  --kubeconfig /path/to/cluster.yaml --context selected-context --yes
mcp-runtime credential source delete upstream-provider \
  --namespace mcp-team-example --revision 1 --yes
```

Runtime does not delete backing provider secrets. Removing metadata prevents new
resolutions; deleting a source that still has bindings is refused.

## Delivery and trust boundary

The gateway verifies the session-bound caller and authorizes the tool. The
broker separately verifies the gateway's dedicated Pod-bound token and
UID-checked Pod → ReplicaSet → Deployment → MCPServer ownership, then derives
human/agent/team identity from the authoritative session. Identity headers,
human platform JWTs and API keys cannot select a workload for this protocol.

Only the authenticated gateway receives values, over verified HTTPS. It injects
the reserved `X-Mcp-Credential-*` header into that tool request to the same-Pod
server. Caller-supplied reserved headers/trailers and reserved response
headers/trailers are removed. The trusted server must consume credentials for
the intended operation without logging, persisting or returning them. A server
operator controls that workload; the broker cannot prevent a malicious
authorized server from disclosing its own credential.

This delivery mode accepts nonempty UTF-8 text up to 64 KiB, without control
bytes or leading/trailing spaces that HTTP would change. Binary, multiline and
invalid UTF-8 values are rejected instead of being silently encoded or corrupted.
Credentials are unavailable to unrelated tools, delegated-header mode,
revoked/incomplete sessions or arbitrary network upstreams. There is no raw-value
management/UI endpoint or workload environment/file projection in this mode.

### Residual risk: gateway token scope

The broker authenticates the server's own gateway Pod-bound token, not a
per-call proof. That token is accepted for any live session of that server and
for any tool its current grant and a matching binding allow. This is an
accepted design residual risk: the token is mounted only into the gateway
sidecar (never the MCP server container), the broker re-checks the session,
grant, server UID/retiring state and binding revisions on every resolution,
and every use is audited. Compromise of the gateway container therefore exposes
the credentials bound to that server's already-authorized tools, nothing wider.

## Availability controls

- The provider read runs outside the platform-wide session lock. The broker
  reads provider metadata, fetches the value without holding any database lock,
  then takes the lock and re-validates the session, subjects, bindings and
  sources (exact revisions), server UID/retiring state and grant before it
  audits and returns anything. A revocation that lands during the read denies
  the request and no value is returned.
- `POST /api/v1/credential-resolutions` is on the public ingress because the
  gateway reaches it over HTTPS. Before any TokenReview, requests are limited
  per source address (10/s, burst 20; behind a proxy set
  `MCP_TRUSTED_PROXY_CIDRS` so the client address, not the proxy, is used).
  Callers that exceed it receive `429` with `Retry-After: 1`. A valid Pod-bound
  token for the dedicated audience is still required.
- After authentication each server is limited to 20/s (burst 40) and returns
  `429` beyond that.
- A failed provider read is remembered for 5 seconds per server and source
  revision (bounded to 1024 entries, no values stored), so a down provider is
  not hammered and returns `503` immediately.
- Source `version` values accept only `[A-Za-z0-9_-]` (or `latest`); dots and
  slashes are rejected.

## Upgrade gate: analytics TLS

`config/release/wiring.yaml` sets `requiresAnalyticsTLS: true`. Release
manifests require the analytics mutual-TLS wiring before ordinary image
updates, and the updater refuses legacy plaintext wiring. Existing plaintext
Kafka installs must take encrypted backups and restore into a fresh TLS
installation (see "Analytics transport security" in
[platform-services.md](platform-services.md)) before updating to a release that
includes the credential broker. Test-mode installs are exempt.
