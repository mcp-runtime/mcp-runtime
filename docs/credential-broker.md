# Scoped credential broker

Credential bindings let an MCP tool use an approved upstream credential without
returning it to its human caller. An administrator registers an exact provider
reference; a namespace owner binds that source to a live server UID, explicit
tools, a human, a managed agent and a team. An optional session ID narrows the
binding further. Management requests contain metadata only.

**Only the Kubernetes Secret provider is supported today.** GCP Secret Manager,
AWS Secrets Manager, Azure Key Vault and Vault are planned and not implemented;
source creation for them is refused with "provider ... is not yet supported"
(HTTP 422 from the API, the same message from the CLI). See
[Providers](#providers). Runtime reads the current backing value for each
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
installation. Setup refreshes retain the configured origin.

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

## Providers

| Provider | Status |
|---|---|
| `kubernetes` | Supported. Exact same-namespace Secret name and key; no `version`. |
| `gcp`, `aws`, `azure`, `vault` | Planned, not implemented. Not registered anywhere; sources are refused. |

The credential configure flags for cloud workload identity are hidden and
refuse to run. The Kubernetes provider needs no cloud account.

### Provider interface (for contributors)

`pkg/credentialbroker` defines the one seam the broker resolves through:

- `Provider` is the reader interface: `Read(ctx, Source) (Value, error)`. It
  returns the current value and an opaque version, and must not leak provider
  error text or values.
- `Registry` maps a provider kind to its `Provider`. `Registry.Register` and
  `Registry.Reader` honour only kinds enabled by `ProviderSupported`
  (`pkg/credentialbroker/registry.go`), so a reader for a disabled kind can
  neither be registered nor resolved.
- The platform API builds its registry in
  `services/platform-api/internal/credentialapi/providers.go` (`Providers`),
  and `Handler.fetch` resolves each source through `Registry.Reader`.
- `Source.Validate` (`pkg/credentialbroker/types.go`) refuses disabled kinds
  with `ErrProviderNotSupported` before any provider-specific check;
  `validateReference` holds the reference rules per kind.

`credentialapi/cloud_readers.go` still holds reference readers for GCP, AWS,
Azure and Vault. They are untested reference code: never verified against a live
provider, not registered, and unreachable from the broker or gateway. Their SDK
and HTTP fixture tests call them directly.

### Adding a provider later

1. Implement `cb.Provider` for the kind (start from the reference reader if one
   exists); return `cb.ErrUnavailable` on any failure and never include provider
   text or values.
2. Enable the kind: add it to `ProviderSupported` and remove it from
   `knownUnsupportedProvider` in `pkg/credentialbroker`, and register the reader
   in `credentialapi.providers`.
3. Add validation: the kind's exact-reference rules in `validateReference`
   (and any `credential configure` support it needs, with the flags unhidden).
4. Add tests: reference validation, the reader against a fixture and, before
   claiming support, a live-provider verification; update the registry tests
   that assert only `kubernetes` is registered.
5. Add a docs row to the table above and a CHANGELOG entry.

## Rotation, revocation and cleanup

Rotate the Secret value in Kubernetes. Reads use current Secret data on every
call, without server redeployment. To change metadata, save `get` output, edit permitted fields
and apply it with its returned `revision`. Stale updates/deletes return conflict.
A reference change may require revoking its old exact-Secret grant before
authorizing the new reference.

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
  sources (full content, so a delete and recreate that reuses a revision is caught), server UID/retiring state and grant before it
  audits and returns anything. A revocation that lands during the read denies
  the request and no value is returned.
- `POST /api/v1/credential-resolutions` is on the public ingress because the
  gateway reaches it over HTTPS. A valid Pod-bound token for the dedicated
  audience is always required. The verdict for each exact token is cached by
  SHA-256 hash (never the token), bounded to 4096 entries. A valid verdict is
  honored for 10 seconds, so a deleted or terminating Pod's token can still
  resolve for up to 10 seconds; session, grant, server and binding state is
  re-checked on every call. Only definite rejections (not authenticated,
  audience mismatch, not Pod-bound, ownership-chain mismatch) are cached, for 5
  seconds; a TokenReview or API-server error fails that request with `503` and
  is never cached. Cached tokens skip every pre-authentication limiter, so
  unauthenticated traffic sharing the ingress or proxy address cannot deny a
  valid caller. A token that was valid within the last 60 seconds renews through
  its own per-token budget (1/s, burst 3), separate from cold traffic. Only
  unknown tokens consume the per-source budget (10/s, burst 20; set
  `MCP_TRUSTED_PROXY_CIDRS` so the client, not the proxy, is the key) and the
  global TokenReview budget (50/s, burst 100); excess receives `429` with
  `Retry-After: 1`. Residual: a gateway presenting a brand-new token during a
  flood of unknown tokens may see `429` and should retry.
- After authentication each server is limited to 20/s (burst 40) and returns
  `429` beyond that.
- A failed provider read is remembered for 5 seconds per server and source
  revision (bounded to 1024 entries, no values stored), so a down provider is
  not hammered and returns `503` immediately.

## Upgrade gate: analytics TLS

`config/release/wiring.yaml` sets `requiresAnalyticsTLS: true`. Release
manifests require the analytics mutual-TLS wiring before ordinary image
updates, and the updater refuses legacy plaintext wiring. Existing plaintext
Kafka installs must take encrypted backups and restore into a fresh TLS
installation (see "Analytics transport security" in
[platform-services.md](platform-services.md)) before updating to a release that
includes the credential broker. Test-mode installs are exempt.

## QA E2E

`test/e2e/scenarios/credential-broker.sh` (scenario `credential-broker`) drives
the Kubernetes provider through the CLI and gateway with a fixture server that
reports only SHA-256 fingerprints of the reserved headers it receives. It needs
`E2E_CREDENTIAL_BROKER_URL`, a verified HTTPS platform origin trusted by gateway
system roots; default Kind test-mode has none, so the scenario then reports a
loud skip. It had not run against a cluster when added.
