# Multi-Team Isolation

MCP Runtime's beta multi-team model combines team identity with Kubernetes
namespace boundaries. `MCPServer.spec.teamID` records the owning
platform team. `SubjectRef.teamID` constrains grants and sessions to callers
from that team. Namespaces and RBAC still isolate who can create resources.

The source-of-truth data plane is:

- `MCPServer`, `MCPAccessGrant`, and `MCPAgentSession` are namespaced resources.
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
- The gateway reads team identity from `spec.auth.teamIDHeader` in header mode,
  from OAuth `team_id`, `tenant_id`, or `tid` claims in OAuth mode, or from the
  session named by a verified adapter certificate.

## When to use this

The default `mcp-servers` namespace is fine for a single team, local
development, and simple evaluation clusters. For a deployment that hosts
multiple teams or tenant boundaries, use one namespace per team.

Recommended namespace shape:

| Team | Namespace | Resources in that namespace |
|---|---|---|
| Acme | `mcp-team-acme` | Acme `MCPServer`, `MCPAccessGrant`, `MCPAgentSession`, secrets, configmaps |
| Globex | `mcp-team-globex` | Globex `MCPServer`, `MCPAccessGrant`, `MCPAgentSession`, secrets, configmaps |

Keep each server's grants and sessions in the same namespace as the server they
govern. When `serverRef.namespace` is omitted, clients and renderers should
treat the current resource namespace as the intended boundary.

## Provisioning a team namespace

Admins create a managed team namespace through the platform API with
`team create`:

```bash
mcp-runtime auth login --api-url https://platform.example.com
mcp-runtime team create acme --name "Acme"
mcp-runtime team user create acme \
  --username acme-user@example.com \
  --password '...' \
  --role member
mcp-runtime team list
```

`team create` records the team in the platform store and asks the API to ensure
the managed namespace, quota, limit range, default deny network policy, default
service account, bundled Traefik watch RBAC, and bundled Traefik namespace
watch entry. Platform API server writes into that namespace default
`spec.teamID` from the authenticated principal's team. Grant/session writes
default missing `subject.teamID` to the owning server team. The platform API
accepts an explicit foreign `subject.teamID` only after verifying the named
human or active agent belongs to that team and the grant has a capped expiry.

Use `team user create` as a platform admin when you need a local password-login
user for a team. The command creates or updates the password identity, adds the
user to the team as `member` or `owner`, and then the user can sign in with:

```bash
mcp-runtime auth login \
  --api-url https://platform.example.com \
  --username acme-user@example.com \
  --password '...'
```

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
  agentID: acme-cron-bot
  teamID: 7d0a0b8f-7c25-4761-a632-3cf0108e31d6
```

When more than one subject field is set, every field must match the request
identity, so a moved user stops matching the old team's grants as soon
as their trusted `teamID` claim/header changes.

## Gateway Identity

Header mode defaults:

| Identity | Header |
|---|---|
| `humanID` | `X-MCP-Human-ID` |
| `agentID` | `X-MCP-Agent-ID` |
| `teamID` | `X-MCP-Team-ID` |
| `sessionID` | `X-MCP-Agent-Session` |

Override the team header per server with `spec.auth.teamIDHeader`. In OAuth
mode, the proxy validates the token and reads team identity from `team_id`,
`tenant_id`, or `tid`, in that order. Adapter-certificate requests ignore
caller-supplied identity headers and take `humanID`, `agentID`, and `teamID`
from the rendered session that the ingress-verified SPIFFE identity resolves
to, inside the platform trust domain. First-party OAuth tokens with multiple
memberships use `team_ids`; the gateway selects the policy server's team ID
when present, or the sole team ID when there is only one.

## Platform API Enforcement

The platform API fails closed for team-scoped writes. The setup-time
`--platform-mode` decides which catalog namespace non-admin users see by
default:

| Mode | Default namespace behavior | Non-admin behavior |
|---|---|---|
| `tenant` | Principal team namespace | Authenticated users read and write only team namespaces for teams they belong to. |
| `org` | `mcp-servers-org` | Authenticated users publish and browse the org catalog together with their authorized team namespaces. |
| `public` | `mcp-servers-public` | Anonymous users can read the public preview catalog; signed-in users publish to the public catalog namespace and browse it together with their authorized team namespaces. |

- In `tenant` and `org` modes, anonymous callers cannot read the MCP server
  catalog.
- In `tenant` mode, non-admin callers listing MCP servers without a `namespace`
  query see MCPs in their team namespaces.
- In `org` and `public` modes, non-admin listings cover the active catalog
  namespace (`mcp-servers-org` or `mcp-servers-public` by default, plus any
  `PLATFORM_CATALOG_NAMESPACES` entries) **and** the team namespaces authorized
  on the caller's principal. Anonymous public-mode reads are limited to the
  catalog namespaces.
- Server publish requests may pass `scope: tenant`, `scope: org`, or
  `scope: public` in place of an explicit catalog namespace. The API
  resolves `org` and `public` only when the matching platform mode is enabled;
  `tenant` resolves to the caller's team namespace unless an authorized team
  namespace is provided.
- Non-admin callers cannot write servers, grants, or sessions into the shared
  `mcp-servers` catalog namespace in `tenant` mode.
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

The bundled Traefik manifests watch only `registry`, `mcp-sentinel`,
`mcp-servers`, `mcp-servers-org`, and `mcp-servers-public` by default so Traefik
does not need broad namespace access. If MCP servers live in team namespaces,
update the ingress controller watch list, bind the Traefik watch role in each
team namespace, and allow ingress-controller traffic through the namespace
NetworkPolicy. The platform API `team create` flow performs those changes for
the repo-managed `traefik/traefik` Deployment when
`PLATFORM_TEAM_TRAEFIK_WATCH` is not `disabled`.

For the bundled Traefik overlay, extend the argument:

```text
--providers.kubernetesingress.namespaces=registry,mcp-sentinel,mcp-servers,mcp-servers-org,mcp-servers-public,mcp-team-acme,mcp-team-globex
```

External ingress controllers need equivalent namespace watch, RBAC, and
NetworkPolicy configuration. For platform API team creation, set
`PLATFORM_TEAM_TRAEFIK_WATCH=disabled` when another controller manages that
wiring, or override `PLATFORM_TRAEFIK_NAMESPACE`,
`PLATFORM_TRAEFIK_DEPLOYMENT`, and `PLATFORM_TRAEFIK_SERVICE_ACCOUNT` when the
repo-managed Traefik names differ.

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
admin-only on the platform API (agents use `adapter stdio|proxy --server …
--agent … --auto-refresh`). Add `--use-kube` only for admin/operator
direct Kubernetes writes. Before applying manifests, the apply commands warn
about obvious `humanID` shape problems, such as whitespace, malformed
email-like strings, case-sensitive uppercase email identifiers, or values that
appear to encode `mcp-team-*` namespace names. The warnings never block the
apply.

## Audit And Reporting

Gateway analytics events carry target `team_id` from `MCPServer.spec.teamID`
and caller `subject_team_id` from the request identity, alongside server,
namespace, human ID, agent ID, session, tool, and decision. ClickHouse
materializes `team_id`, so team-scoped reporting can filter directly on the
server owner's team without joining through namespace names.

## Known Limits

- Team-wide grants require a trusted `teamID` header or OAuth team claim.
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

1. Create one namespace per team or tenant boundary.
2. Bind team admins only to their namespace.
3. Put each team's `MCPServer`, `MCPAccessGrant`, `MCPAgentSession`, analytics
   secrets, and image pull secrets in that namespace.
4. Set `spec.teamID` on every team-owned `MCPServer`.
5. Set `subject.teamID` on grants and sessions, or use the platform API so it
   defaults missing values from the owning server team. Use an explicit foreign
   `subject.teamID` for delegated cross-team access.
6. Configure trusted header or OAuth team identity extraction.
7. Add team namespaces to the ingress controller watch list and RBAC.

## Related Docs

- [Getting Started](getting-started.md) for the single-namespace local flow.
- [CLI](cli.md) for `team`, `access`, and namespace-scoped commands.
- [Runtime](runtime.md) for CRD and reconciliation behavior.
- [API Reference](api.md) for access resource fields.
