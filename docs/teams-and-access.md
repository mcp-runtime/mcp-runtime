# Multi-Team Isolation

For approved upstream credentials scoped to a server, tool and current managed
agent session, see [Scoped credential broker](credential-broker.md). Its CLI
and management APIs contain metadata only.

Each team has its own place to deploy servers and manage access. Kubernetes
namespaces keep those resources separate, and the platform checks team
membership before allowing changes. A team can share a server with another
team through an explicit grant.

In the resource definitions, `MCPServer.spec.teamID` records the owning team.
`SubjectRef.teamID` identifies the team allowed to use a grant or session.

The source-of-truth data plane is:

- `MCPServer` and `MCPAccessGrant` are namespaced Kubernetes resources.
  Agent sessions are namespace-qualified platform records stored by platform-api;
  Kubernetes session objects do not control authorization.
- `MCPServer.spec.teamID` is the stable platform team ID that owns the server.
- `SubjectRef` contains `humanID`, `agentID`, and `teamID`; the gateway matches
  every non-empty field by exact string equality.
- A server owner may grant a different team access by setting `subject.teamID`
  to that team's ID. The platform verifies that each named human or agent
  belongs to that team; it never treats server ownership as subject identity.
- Cross-team grants require `expiresAt` and are limited by
  `MCP_CROSS_TEAM_GRANT_MAX_TTL` (default `168h`, seven days). Adapter sessions
  issued from a grant cannot outlive the grant.
- A subject with only `teamID` grants or binds any authenticated principal in
  that team.
- `MCPAccessGrant.spec.expiresAt` can end a delegation automatically. Any
  session issued from the grant expires no later than the grant, including
  sessions requested by an auto-refreshing adapter.
- The gateway reads team identity from OAuth `team_id`, `tenant_id`, or `tid`
  claims, or from the
  session named by a verified adapter certificate.

## Inspect an agent's ownership and activity

Open **Agents** and select an agent. The detail view shows its owning team,
creator's recorded user ID, creation/update times, applicable grants and visible
sessions. The creator is historical attribution; the owning team controls the
agent. Session rows identify the delegating human and authorizing grant, with
expiry and revocation state. Counts cover only sessions visible to your account.

**Recorded certificate issuance** is separate from session counts because one
session can receive several renewed certificates. The count covers recorded
issuances in the last seven days; the table shows the latest 100 with human,
session, server, serial, fingerprint and certificate expiry. Recording begins
with v0.12.0. Older or unrecorded certificates are unknown, so zero recorded
issuances does not mean no certificates have ever existed. Private keys and
certificate PEM are never shown. A revoked/expired session denies access even
before a certificate's expiry. History failures leave identity/access usable
and display unavailable data instead of zero.

**MCP tool activity** filters analytics by the exact Agent ID and `tools/call`,
with one-hour, 24-hour and seven-day windows. It shows call totals, gateway
allow/deny decisions, HTTP failures, per-tool/server groups and recent human and
session attribution. Tables are capped at 50 rows; totals cover the selected
window. Team users see their authorized namespace/resource-team activity;
shared catalog and foreign-resource calls can be excluded. Administrators can
see platform-wide activity for the selected agent.

An allowed gateway decision is not proof a tool succeeded: MCP errors can use
HTTP 200. The view shows observed MCP activity, not the agent's full task,
prompts or intent. Analytics failures are reported separately from directory,
grant, session and certificate data.

## When to use this

To delegate through the dashboard, open **Access control**, select the resource
namespace, create a grant, and choose **Delegate to another team**. Enter the
recipient's exact team slug and select an active agent, a stable member ID, both,
or explicitly the entire recipient team. Recipient lookup requires resource
namespace management rights and does not expose general foreign-team directory
or membership-management access. Choices are paginated and are cleared when the
resource or lookup changes. The form defaults to a 24-hour expiry; the API
enforces the configured maximum and revalidates recipients when saving.

Use **Revoke sessions** on a grant to atomically revoke all explicitly linked
platform sessions, including links stored in another namespace. Disable or
expire the grant as well to prevent future issuance. Retrying revocation reports
only newly revoked sessions; revoked history remains inspectable. Administration's
Gateway audit shows actor and resource teams separately. Unknown historical
actor attribution stays unknown; the resource `team_id` does not prove actor
membership.

The default `mcp-servers` namespace is fine for a single team, local
development, and simple evaluation clusters. For a deployment that hosts
multiple teams or tenant boundaries, use one namespace per team.

Recommended namespace shape:

| Team | Namespace | Resources in that namespace |
|---|---|---|
| Acme | `mcp-team-acme` | Acme `MCPServer`, `MCPAccessGrant`, secrets, configmaps |
| Globex | `mcp-team-globex` | Globex `MCPServer`, `MCPAccessGrant`, secrets, configmaps |

Keep each server's grants in the same namespace as the server they govern.
Agent sessions are platform records for that server, not objects you edit in
the namespace. When `serverRef.namespace` is omitted, clients and renderers
should treat the current resource namespace as the intended boundary.

## Provisioning a team namespace

Admins create a managed team namespace through the platform API with
`team create`:

```bash
mcp-runtime auth login --api-url https://platform.example.com
mcp-runtime team create acme --name "Acme"
mcp-runtime team user create acme \
  --username acme-user@example.com \
  --role member
mcp-runtime team list
```

`team user create` prompts twice for a hidden password. For non-interactive
use, provide the password through stdin with `--password-stdin`; password
values are not accepted as command-line arguments.

`team create` records the team in the platform store and asks the API to ensure
the managed namespace, quota, limit range, default deny network policy, default
service account, bundled Traefik watch RBAC, and bundled Traefik namespace
watch entry. Platform API server writes into that namespace default
`spec.teamID` from the authenticated principal's team. Grant/session writes
default missing `subject.teamID` to the owning server team. The platform API
accepts an explicit foreign `subject.teamID` only after verifying the named
human or active agent belongs to that team and the grant has a capped expiry.

Use `team user create` as a platform admin when you need a new local
password-login user for a team. The command creates the user and team membership
in one transaction as `member` or `owner`, and then the user can sign in with:

```bash
mcp-runtime auth login \
  --api-url https://platform.example.com \
  --username acme-user@example.com \
  --password '...'
```

For an existing platform user, find their user ID in the platform UI and run
`mcp-runtime team user add <team-slug> <user-id> --role member` (or set the role
to `owner`). This changes only the team membership and does not reset the user's
password. In the UI, open Administration → Teams and choose **Add existing user**,
then enter that user ID and select a role. **Create account in this team** is for
new accounts and requires a temporary password of at least eight characters.
Validation failures appear in the form; an existing email offers the membership
form rather than resetting the account password. Platform admins and team owners can manage members in their teams.
Selecting a team shows its identifier, namespace, recorded creation date, member
and owner counts, and associated active and inactive agents. Member rows include
roles and recorded join dates. Agent rows link to details and show stable IDs,
creator attribution and recorded creation dates. **Load more agents** follows the
directory cursor; counts describe loaded agents until the directory is complete.
Directory errors appear independently and unavailable counts remain unknown.
The recorded creator is the registering user, not every human using an agent.
The APIs resolve the current account and membership after validating a saved
login token. Membership changes take effect on the next API request; directory
lookup failures deny access rather than trusting a stale token's roles.

Remove membership with `mcp-runtime team user remove <team-slug> <user-id>`;
removing the only owner requires `--confirm-last-owner`. This removes access to
that team, but does not delete the account or revoke its account-wide API keys
and sessions.

For a disposable test identity, first create a regular team member with the
hidden password prompt, record its user ID with `team user list`, sign in as
that user, and enroll the adapter through the normal session/certificate path.
The static administrator service key has no human identity and cannot replace
that user's login for enrollment. After the test, sign back in as an
administrator and run:

```bash
mcp-runtime user deactivate <user-id> --yes --json
```

`user deactivate` permanently disables a platform account, removes its team
memberships, and revokes its stored API keys, registry credentials, refresh
tokens and MCP sessions. Audit history and the identity tombstone remain;
there is no reactivation or hard-delete command. Repeating the command returns
`already_deactivated: true` with the cleanup counts, so interrupted cleanup can
be retried. The transaction rolls back if any revocation or audit write fails.
The signed-in administrator, the last active administrator and a team's last
active owner cannot be deactivated; transfer ownership or create another
administrator first. Server resources and team-owned agents remain in place
for the surviving administrators.

Bearer/API-key requests revalidate current account state. MCP session
revocations use the existing gateway sync contract (normally a 500ms poll;
disconnected caches deny after the documented maximum stale window), rather
than an acknowledgement from every gateway. Previously issued registry bearer
tokens retain their normal expiry; revoked stored credentials cannot mint new
tokens. Account cleanup counts report committed platform state.

`team init` is **deprecated** and rejects at runtime. Use `team create` above
for the normal platform-backed flow. The managed namespace shape that
`team create` provisions includes:

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: mcp-team-acme
  labels:
    mcpruntime.org/scope: team
    mcpruntime.org/team-slug: acme
    pod-security.kubernetes.io/enforce: restricted
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: mcp-runtime-team-admin
  namespace: mcp-team-acme
rules:
  - apiGroups: ["mcpruntime.org"]
    resources: ["mcpservers", "mcpaccessgrants", "mcpagentsessions"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: [""]
    resources: ["secrets", "configmaps"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: [""]
    resources: ["pods", "pods/log", "events", "services"]
    verbs: ["get", "list", "watch"]
  - apiGroups: ["networking.k8s.io"]
    resources: ["ingresses"]
    verbs: ["get", "list", "watch"]
```

`kubectl apply` uses create, update, and patch; Kubernetes has no
separate `apply` verb.

## Team Fields

Server ownership:

```yaml
apiVersion: mcpruntime.org/v1alpha1
kind: MCPServer
metadata:
  name: payments
  namespace: mcp-team-acme
spec:
  teamID: 7d0a0b8f-7c25-4761-a632-3cf0108e31d6
  image: registry.example.com/acme/payments:latest
```

Team-wide access:

```yaml
apiVersion: mcpruntime.org/v1alpha1
kind: MCPAccessGrant
metadata:
  name: payments-acme-read
  namespace: mcp-team-acme
spec:
  serverRef: {name: payments}
  subject: {teamID: 7d0a0b8f-7c25-4761-a632-3cf0108e31d6}
  maxTrust: low
  allowedSideEffects: [read]
```

Specific human or agent inside a team:

```yaml
subject:
  humanID: alice@example.com
  agentID: agt_01arz3ndektsv4rrffq69g5fav
  teamID: 7d0a0b8f-7c25-4761-a632-3cf0108e31d6
```

When more than one subject field is set, every field must match the request
identity, so a moved user stops matching the old team's grants as soon
as their trusted `teamID` claim or certificate-backed session changes.

## Gateway Identity

The gateway validates the OAuth token and reads team identity from `team_id`,
`tenant_id`, or `tid`, in that order. Adapter-certificate requests take
`humanID`, `agentID`, and `teamID`
from the rendered session that the ingress-verified SPIFFE identity resolves
to, inside the platform trust domain; their OAuth subject must match that
session's `humanID`. First-party OAuth tokens with multiple
memberships use `team_ids`; the gateway selects the policy server's team ID
when present, or the sole team ID when there is only one.

## Platform API Enforcement

The platform API blocks changes outside a user's allowed team namespaces.
When you install MCP Runtime, `--platform-mode` chooses which catalog users see
by default. In `public` mode, visitors can browse that catalog without signing
in. This is a setting for your own platform; the public reference deployment
at `platform.mcpruntime.org` is a separate installation.

| Mode | Default namespace behavior | Non-admin behavior |
|---|---|---|
| `org` (default) | `mcp-servers-org` | Authenticated users publish and browse the org catalog together with their authorized team namespaces. |
| `public` | `mcp-servers-public` | Anonymous users read the public catalog; signed-in users also publish and browse org and authorized team namespaces. |

- In `org` mode, anonymous callers cannot read the MCP server
  catalog.
- In `org` and `public` modes, non-admin listings cover the active catalog
  namespace (`mcp-servers-org` or `mcp-servers-public` by default, plus any
  `PLATFORM_CATALOG_NAMESPACES` entries) **and** the team namespaces authorized
  on the caller's principal. Anonymous public-mode reads are limited to the
  public catalog namespaces only; org and team namespaces remain authenticated.
- Server publish requests may pass `scope: tenant`, `scope: org`, or
  `scope: public` in place of an explicit catalog namespace. The API
  accepts `org` and `tenant` in both modes and `public` only in public mode;
  `tenant` resolves to the caller's team namespace unless an authorized team
  namespace is provided.
- Non-admin callers cannot write servers, grants, or sessions into the shared
  `mcp-servers` catalog namespace unless it is explicitly configured as an active catalog.
- Non-admin callers can only read or write resources in namespaces listed on
  their authenticated principal, except for the active `org`/`public` mode
  catalog namespace.
- Server apply defaults `spec.teamID` from the principal's team namespace and
  rejects mismatches.
- Grant/session apply defaults missing `subject.teamID` from the referenced
  server or namespace team. An explicit foreign `subject.teamID` is preserved,
  allowing the server-owning team to grant another team access to that server
  after the platform API verifies subject membership and bounded expiry.
- A grant or session must reference an `MCPServer` in the same namespace as the
  access resource. Cross-namespace `serverRef.namespace` values are rejected.
- Admin callers keep cluster-wide visibility, but same-namespace and team-ID
  ownership checks still apply to server resources.

Direct `kubectl apply` still depends on Kubernetes RBAC. Bind team admins only
inside their team namespace. As a defense-in-depth guard, the operator renders
only grants and sessions whose `serverRef` points at the target server. Missing
  `subject.teamID` values constrain the caller, not server ownership. Direct
  Kubernetes writes bypass platform identity and cross-team TTL checks, so use
  the platform API for cross-team grants. Explicit foreign subjects are
  rendered and enforced by the gateway, which matches every non-empty
  `humanID`, `agentID`, and `teamID` exactly.

## Ingress controller watch scope

The bundled Traefik manifests watch only `registry`, `mcp-platform`,
`mcp-observability`, `mcp-log-collector`, `mcp-servers`, `mcp-servers-org`, and
`mcp-servers-public` by default so Traefik does not need broad namespace access.
If MCP servers live in team namespaces,
update the ingress controller watch list, bind the Traefik watch role in each
team namespace, and allow ingress-controller traffic through the namespace
NetworkPolicy. The platform API `team create` flow performs those changes for
the repo-managed `traefik/traefik` Deployment when
`PLATFORM_TEAM_TRAEFIK_WATCH` is not `disabled`.

For the bundled Traefik overlay, extend the argument:

```text
--providers.kubernetesingress.namespaces=registry,mcp-platform,mcp-observability,mcp-log-collector,mcp-servers,mcp-servers-org,mcp-servers-public,mcp-team-acme,mcp-team-globex
```

External ingress controllers need equivalent namespace watch, RBAC, and
NetworkPolicy configuration. For platform API team creation, set
`PLATFORM_TEAM_TRAEFIK_WATCH=disabled` when another controller manages that
wiring, or override `PLATFORM_TRAEFIK_NAMESPACE`,
`PLATFORM_TRAEFIK_DEPLOYMENT`, and `PLATFORM_TRAEFIK_SERVICE_ACCOUNT` when the
repo-managed Traefik names differ.

Managed namespace policies allow outbound HTTPS to Traefik pods in the
configured ingress namespace on ports 443 and 8443. This lets standalone OAuth
servers fetch discovery and signing keys from the platform's public issuer.
Both the namespace and Traefik pod labels must match; the rule does not allow
general Internet egress or ingress-controller dashboard access. Namespace
provisioning through `team create` or `server deploy` refreshes this policy for
existing namespaces as well.

## Identifier Conventions

Keep identifiers stable:

| Field | Recommendation |
|---|---|
| `teamID` | Use the platform store team UUID or another immutable identity-provider tenant/team ID. Do not use a mutable display name. |
| `humanID` | Use the identity provider's stable subject claim, or email when that is stable in your environment. |
| `agentID` | Use the immutable `agt_<26-character lowercase ULID>` returned by the platform agent directory. Do not type or reuse a human-readable name as an ID. |

`mcp-runtime access grant init` and `access session init` scaffold local YAML on
the workstation only. `access grant apply` uses the platform API by default after
`mcp-runtime auth login --api-url <platform-url>`; `access session apply` is
admin-only on the platform API (agents use `adapter proxy --server …
--agent … --auto-refresh`). Add `--use-kube` only for admin/operator
direct Kubernetes writes. Before applying manifests, the apply commands warn
about obvious `humanID` shape problems, such as whitespace, malformed
email-like strings, case-sensitive uppercase email identifiers, or values that
appear to encode `mcp-team-*` namespace names. The warnings never block the
apply.

Grants default to enforcement. Add `--mode monitor` when initializing a grant,
or choose **Monitor** in the dashboard, to observe tool, trust and side-effect
denials before enforcing them. Audit events include `would_deny` and
`would_deny_reason`. Identity and required-session checks still block, and a
monitor grant cannot override a matching enforcing grant's denial. Disabled
and expired grants cannot shadow denials. After reviewing the observations,
switch the grant to **Enforce** in the dashboard or set `spec.mode: enforce`
in the manifest and apply it through the CLI. Deploy gateways supporting policy
schema v5 before enabling monitor mode.

## Audit And Reporting

Gateway analytics events carry target `team_id` from `MCPServer.spec.teamID`
and caller `subject_team_id` from the request identity, alongside server,
namespace, human ID, agent ID, session, tool, and decision. ClickHouse
materializes `team_id`, so team-scoped reporting can filter directly on the
server owner's team without joining through namespace names.

## Known Limits

- Team-wide grants require team identity from a verified adapter session or
  validated OAuth team claims. Arbitrary client-supplied identity headers are
  not trusted.
- The evaluator does exact string matching. It does not resolve live group
  membership from the platform database.
- Direct `kubectl apply` can bypass platform API defaulting; Kubernetes RBAC is
  the guardrail for direct writers.
- Cross-team server sharing is a privileged pattern. Prefer a shared namespace
  and explicit admin-owned grants when a server is intentionally shared.
- The bundled setup manifests create the legacy single-team namespace and the
  org/public catalog namespaces; per-team tenant namespaces are an explicit
  operational step. Personal user namespaces are not created for tenant
  publishing.

## Operational Checklist

1. Use `mcp-runtime team create` to provision a namespace per team or tenant
   boundary.
2. Bind team admins only to their namespace.
3. Put each team's `MCPServer`, `MCPAccessGrant`, analytics secrets, and image
   pull secrets in that namespace. Create sessions through the platform CLI or
   UI for those servers.
4. Set `spec.teamID` on every team-owned `MCPServer`.
5. Set `subject.teamID` on grants and sessions, or use the platform API so it
   defaults missing values from the owning server team. Use an explicit foreign
   `subject.teamID` for delegated cross-team access.
6. Configure verified adapter-certificate identity and OAuth when the server
   enables it.
7. Add team namespaces to the ingress controller watch list and RBAC.

## Related Docs

- [Getting Started](self-hosting.md) for self-hosting;
  [Local Kind](contributor/local-kind.md) for contributor setup.
- [CLI](cli-reference.md) for `team`, `access`, and namespace-scoped commands.
- [Runtime](runtime-operations.md) for CRD and reconciliation behavior.
- [API Reference](api-reference.md) for access resource fields.

**Next:** [Client Connections](connect-clients.md) connects an authorized agent
using its session certificate. Use [Server Publishing](publish-mcp-server.md)
first if the team's server is not deployed yet.
