# Gateway session sync

Platform-api owns session persistence and the agent directory in Postgres.
Runtime-api writes through its authenticated control-plane client. Gateways
read only their MCPServer generation and hold no database credentials or shared
internal token. The storage/sync foundation is tracked in [#718](https://github.com/mcp-runtime/mcp-runtime/issues/718);
[migration #659](https://github.com/mcp-runtime/mcp-runtime/issues/659) wires the
runtime management and gateway authorization paths onto it.

## Audiences and routes

| Listener | Authentication | Routes |
| --- | --- | --- |
| Platform control plane, `8080` | Shared `INTERNAL_AUTH_TOKEN`, trusted services only | `POST/GET /internal/access/sessions`, `GET/PATCH/DELETE /internal/access/sessions/{namespace}/{name}`, `POST /internal/access/sessions/revoke` |
| Gateway sync, `8085` | Projected Pod-bound service-account token, audience `mcp-runtime-gateway` | `GET /internal/gateway/v1/sessions`, `GET .../changes?epoch=...&after=...`, `GET .../{namespace}/{name}` |

The OpenAPI path groups declare `x-audience`. Gateway routes are mounted only
on their own listener, and the service's sync port has no ingress route. The
platform NetworkPolicy permits tenant gateway pods only on `8085`; `8080`
accepts platform peers and the ingress controller. Operator gateway egress
and projected token mounting are delivered by the migration PR.

The control listener also provides `PATCH/DELETE
/internal/access/sessions/{namespace}/{name}/enrollment`. Completion checks the
immutable session UID, expiry, active agent and current revocation under the
session-state lock and removes only pending metadata. Cleanup additionally
checks the owner and that enrollment is still pending before deleting and
publishing its tombstone. Neither operation accepts replacement authorization
fields. Runtime-api uses these mutations after signing or failed enrollment;
using a full session upsert here would undo concurrent revocation.
Ingress-controller access on `8080` matches the bundled Traefik's `app: traefik`
label and Helm/k3s Traefik's `app.kubernetes.io/name: traefik` label, including
controllers outside the `traefik` namespace. This permits public API routing
and registry forward-auth; neither Traefik selector permits `8085`. Session sync
still requires a managed gateway pod and the Pod-bound token checks below.

The operator uses `http://mcp-platform-api.mcp-platform.svc.cluster.local:8085`
by default. Set `MCP_GATEWAY_SESSION_SYNC_URL` on the operator to use a different
trusted internal endpoint, and configure matching network policy for that
deployment. It must expose the dedicated gateway audience, never the shared
control-plane API. Tokens are mounted only in the gateway container.

TokenReview must confirm the requested audience and return the bound Pod name
and UID. Platform-api then reads the live Pod, ReplicaSet, Deployment, and
MCPServer controller references and checks every UID. A shared tenant service
account username, Pod label, or caller's query field cannot select another
server. Deleted or recreated resources and retiring servers fail authentication.
See Kubernetes' [bound token and TokenReview documentation](https://kubernetes.io/docs/reference/access-authn-authz/service-accounts-admin/).
Platform-api needs cluster-wide `get` on those four resource kinds because
gateways run in dynamically created namespaces, plus `create` TokenReview.
It receives no tenant write, list/watch, or tenant Secret permissions for sync.
The namespace's Kubernetes workload administrator remains a trusted actor:
controller references are not an isolation boundary against someone who can
mutate the actual server workload or its tokens.

## Consistency and failure behavior

Session identity is the composite `namespace/name`; the binding name remains
local to its namespace. The record retains MCPServer UID, immutable session
UID and creation time, grant linkage, subject and authority teams, labels,
annotations, and revocation timestamp/cause. Reusing a server name with a new
UID cannot reuse the old sessions. Bulk retirement with cause `server_retired`
also persists a UID fence: delayed issuance and unrevocation for that old UID
are rejected. Agent deactivation and its session revocations commit together;
issuance validates and locks the agent in the same transaction.
Management supports human-only and team-only sessions too; directory validation
applies only when an agent ID is present. Deletion can supply `?uid=<session UID>`
to reject a stale retirement snapshot rather than delete a replacement session.
Deleted session names are permanently reserved within their namespace. Create a
new name or re-enroll the adapter after deletion; reusing the deleted name
returns `409 Conflict`, even for a republished server with a different UID.
Certificates identify `namespace/name`, so allowing that name to identify a new
row would let an old certificate inherit the new binding. Identity reservations
are retained separately from the bounded change log and must be included in
database backups. Existing rows can still be updated or explicitly unrevoked
when their agent and server remain active.

A snapshot contains all active, unexpired sessions for the authenticated server
and one epoch/sequence cursor. Snapshot and changes use a repeatable-read SQL
transaction. Mutations lock the singleton sync-state row before allocating
sequences and retain that lock until commit, so a committed higher sequence
cannot hide a still-uncommitted lower revocation. Changes carry `kind: session`
and either an upsert or a delete tombstone. The change query returns at most
1,000 entries; `more: true` requires draining additional pages immediately
before counting the cache as caught up. Polling every 500 ms targets roughly
one-second revocation propagation, subject to database and network latency.

The most recent 100,000 change events are retained globally; lagging gateways
receive `410 Gone` and relist. An epoch mismatch, negative cursor, or cursor
ahead of the current sequence also forces a relist. Gateways deny before the
first successful snapshot, serve last-known-good for at most ten minutes after
a successful catch-up, and deny once stale. A get-on-miss result must be scoped
to the same server UID and must not advance the global freshness timestamp.
Authorization remains in process and still enforces expiry, revocation, grants,
side effects, and trust restrictions.

Validated policy reloads compile immutable tool-name and subject-grant indexes
before atomically activating the new document. Legacy embedded session bindings
also have ID/subject indexes; current platform-synchronized sessions continue to
use the separate server-scoped session cache. A failed load or compilation keeps
the previous complete document and its indexes. Indexes do not change policy JSON,
revision digests or schema versions. Expiry, revocation and monitor/enforcing
decisions are evaluated on every request; they are not cached decisions.
See the [policy benchmark results](../../.codex/skills/cluster-ops/references/performance.md#policy-lookup-microbenchmarks)
for the bounded VM comparison and its limits.

A known epoch/cursor reset or an explicit `401`/`403` identity rejection
immediately invalidates cached authority. It cannot keep serving the old
snapshot while a relist fails. `SESSION_SYNC_STALE_WINDOW` can shorten the
ten-minute maximum for network/store outages. `/config/status` and
`mcp_gateway_session_sync_age_seconds` report freshness; `-1` on the metric
means no successful sync yet. Policy publication rejects grant/tool documents
larger than 700 KiB and increments `mcp_runtime_gateway_policy_oversize_total`.

Roll out matching platform-api, runtime-api, operator and gateway images.
Re-deploy servers through the supported CLI so gateway token mounts and
namespace egress rules are refreshed. Old Kubernetes sessions are not imported;
adapters re-enroll through the platform. This is a policy schema v4 change and
requires a new minor release while the project is `0.x`.

Every platform-api process start rotates the shared database epoch. Ordinary
restarts therefore cause a safe additional snapshot; concurrent replicas also
observe the shared epoch and converge after startup. **After restoring
Postgres, restart platform-api before reconnecting gateways.** This rotates
the epoch even if a restore has the same numerical sequence. Restoring a
database while leaving the platform process running cannot be detected reliably
from a numerical cursor alone. The cursor-ahead check additionally detects a
restore that lowers the sequence.

The platform API ingress policy preserves port 8080 for Traefik, platform
namespace callers, and the analytics API in `mcp-observability` (which uses it
for authentication). Analytics is selected by both namespace and pod labels;
that exception does not grant access to session-sync port 8085.

Revocation E2E measures from the completed management write and requires an
HTTP 401 `session_revoked` response within one second. Each probe can use only
the remaining overall budget; shorter per-request timeouts must not discard a
valid observation. Failures include the last curl exit code and HTTP status.

## Verification

Run platform-api and runtime client tests with an empty kubeconfig:

```sh
export KUBECONFIG=$(mktemp)
cd services/platform-api
go test -race -count=1 ./...
cd ../runtime-api
go test -race -count=1 ./internal/platformclient
```

The platform persistence tests cover snapshots with 3,001 sessions, retention
and restore fences, tombstones, pagination, retirement fences, directory
membership, and rollback when agent revocation fails. Authentication tests cover
audience/port separation, missing bound-Pod identity, live controller generations,
and denial of cross-server gets. Route ownership and manifest tests verify the
gateway group is absent from the public listener and ingress paths.

For actual SQL and issuance/deactivation concurrency, point
`PLATFORM_SESSION_TEST_DSN` explicitly at a disposable Postgres database and run:

```sh
export KUBECONFIG=$(mktemp)
cd services/platform-api
go test -race -count=1 ./internal/platformstore -run TestPostgresSessionSyncLifecycle
```

The test creates and removes its own schema; it never falls back to the
deployment's `POSTGRES_DSN`. Migration E2E must exercise CLI session creation
and revocation with real MCP traffic, check the policy ConfigMap revision stays
unchanged, measure revoke-to-deny delay, and test cold/warm/stale behavior.
Use the explicit Kind/staging target guards from the contributor runbooks;
never point these suites at production.

After retiring and republishing a server, prove the old certificate receives the
exact `401 session_not_found` denial through the recreated ingress. A transient
Traefik 404 is route convergence, not an authorization decision. The certificate
initialize helper retries it only when its bounded route-startup option is
explicitly enabled; persistent 404s time out and accepting the retired
certificate fails immediately. Tool invocations are never retried for readiness.
