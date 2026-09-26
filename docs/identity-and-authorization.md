# Identity and authorization

MCP Runtime uses separate identities for platform administration, delegated
agent access, and Kubernetes workloads. Each identity defines **who is acting,
what they may do, and which component enforces the decision**.

## The three identity planes

| Identity plane | Identity shape | Used for | Enforced by |
|---|---|---|---|
| Platform | User or service principal with role, subject, teams, and namespaces | UI, CLI, and platform API operations | Split Sentinel API services (`platform-api`, `runtime-api`, `analytics-api`) |
| Agent governance | `humanID + agentID + teamID + sessionID` | MCP `tools/call` authorization | MCP gateway |
| Kubernetes workload | ServiceAccount plus RBAC bindings | Reading and changing cluster resources | Kubernetes API server |

Each plane is enforced independently. A platform administrator does not
automatically have an agent session, and an MCP agent identity is separate
from any Kubernetes ServiceAccount.

## Platform identity: who controls the platform

The split Sentinel API services authenticate a request using one of these credentials:

- A browser or CLI bearer token issued after local or OIDC login
- A user-owned API key sent as `x-api-key`
- A configured service or administrator API key sent as `x-api-key`

Authentication produces a platform principal containing fields such as:

```text
role
subject / user ID
email
primary namespace
allowed namespaces
team memberships and team roles
authentication type
```

The API uses this principal to authorize control-plane actions.

| Actor | Allowed actions | Why |
|---|---|---|
| Anonymous caller | Health, login, OIDC exchange, and explicitly public catalog reads | These routes are public by design |
| Authenticated user | Read visible teams, namespaces, servers, and deployments, plus team-scoped analytics on `GET /api/v1/user/analytics/usage` | Visibility is scoped by the principal's namespaces and teams |
| Server owner | Change their server and administer its grants and sessions | The server carries the owner's platform user label |
| Team owner | Manage team members and administer servers and access resources in the team's namespace | Team-owner membership grants administrative authority within that team |
| Platform administrator | Manage teams, users, namespaces, platform operations, and resources across tenants, and read the unscoped analytics routes (`/api/v1/events`, `/stats`, `/sources`, `/event-types`, `/analytics/usage`) | The `admin` role is the platform-wide control-plane authority |
| Service API key | Only the API operations permitted by its assigned role | Service authentication does not automatically create a human or agent identity |

Creating or changing an access grant requires authority over the referenced
server. In normal platform flows, that means the caller is the server owner, a
team owner for the server namespace, or a platform administrator.

Direct creation of an `MCPAgentSession` through `/api/v1/runtime/sessions` is
administrator/internal-only. Normal users obtain a session through the adapter
session endpoint.

See the complete endpoint matrix in
[Sentinel API authn/authz matrix](security/authz-matrix.md).

### Trusted ingress and client IP

The API derives the caller's client IP from the left-most `X-Forwarded-For`
hop, falling back to `RemoteAddr`. This IP is used both for audit `ActorIP`
labelling and for **login-lockout bucketing** (brute-force throttling on
`/api/v1/auth/login`). Because clients can set `X-Forwarded-For` freely, the
ingress in front of platform-api **must** set/overwrite this header
authoritatively and strip any client-supplied value. Otherwise a caller can
rotate the header to evade lockout or poison another address's bucket.

Configure Traefik (and any upstream load balancer) so platform-api sees the
real client address, and so inbound `X-Forwarded-For` from untrusted clients is
discarded, not appended. The ingress is the only trusted boundary for client
IP; platform-api uses the header value it receives.

## Agent identity: who is using an MCP tool

An agent call carries a delegated governance identity:

```text
humanID   the platform user responsible for the call
agentID   the agent or client acting for that user
teamID    the stable team whose server and policy are in use
sessionID the active MCPAgentSession resource
```

The adapter obtains this identity from the platform and writes it to the
configured governance headers on every request. It removes caller-supplied
identity headers before applying the issued values.

The platform agent directory gives each managed `AgentID` a stable record
owned by one team. Its display name and active status help administrators
select and govern agents in grants and sessions. The directory is governance
metadata; the ID alone does not authenticate a runtime or prove which software
made a request. Authentication continues to come from the configured OAuth
flow or, for enrolled adapters, the session-bound certificate.

Agent IDs are platform-generated immutable `agt_<26-character lowercase
ULID>` values. Agent subjects must resolve to an active directory record owned
by the selected subject team. Unknown, malformed, inactive, or wrong-team IDs
are rejected, and agent IDs cannot be entered as free text in the admin access
forms. The runtime API checks grants, sessions, adapter session issuance, and
certificate enrollment; use the runtime API for access changes so it can
validate agent ownership against the identity store. Direct Kubernetes CRD
writes do not consult the identity store and therefore bypass these directory
checks; adapter session issuance and certificate enrollment still verify the
agent before they issue credentials.
Deactivation first marks the agent inactive,
then revokes its active sessions across namespaces and audits each revocation.
If revocation fails, the agent remains inactive; retry deactivation to finish.

For audit, keep the **actor** (the agent named by the session) separate from
the **authority** (the human or team that delegated access). A session and its
gateway decisions should preserve both identities, along with the grant,
server, MCP method/tool, decision, and request trace ID. MCP's OAuth roles and
emerging workload identity guidance provide useful context; an OAuth client
registration or `client_id` does not automatically create a managed agent.

In the default header mode, the gateway reads these headers. Therefore, the
adapter, ingress path, and gateway form a trust boundary: untrusted clients
should not be able to bypass the adapter and inject governance headers directly.
On OAuth-configured servers, clients without an adapter certificate
authenticate with a bearer token at the gateway.

Clients without an adapter certificate use OAuth. An adapter can instead
authenticate with its session-bound client certificate; Traefik verifies it and
the gateway resolves its session identity for grant/session authorization.
Adapter certificates are opt-in (`MCP_ADAPTER_CERTIFICATES=true`) and
require the platform-wide `MCP_MTLS_CLUSTER_ISSUER` and `MCP_TRUST_DOMAIN`
settings. Persisted `auth.mode: mtls` resources must be
migrated to OAuth; that per-server mode was removed.

Further reading: [MCP authorization](https://modelcontextprotocol.io/specification/2025-11-25/basic/authorization),
[WIMSE Agent Identity Management Services draft](https://datatracker.ietf.org/doc/draft-ietf-wimse-aims/00/)
(work in progress), [AUDIT delegation and interaction traceability proposal](https://datatracker.ietf.org/doc/bofreq-kuhlewind-audit-agent-use-of-delegation-and-interaction-traceability/),
and [SPIFFE SVIDs](https://spiffe.io/docs/latest/deploying/svids/).

## Grant: administrator-approved authority

An `MCPAccessGrant` answers:

> Which human, agent, or team may use which MCP server, with which tools,
> side effects, and maximum trust?

Its important fields are:

```yaml
spec:
  serverRef:
    name: payments
    namespace: mcp-team-finance
  subject:
    humanID: user-123
    agentID: agt_01arz3ndektsv4rrffq69g5fav
    teamID: team-finance-id
  maxTrust: medium
  allowedSideEffects: [read, write]
  toolRules:
    - name: create_invoice
      decision: allow
      requiredTrust: medium
```

Every populated subject field must match the request identity. A grant bound to
all three subject fields does not match a different human, agent, or team.
For cross-team access, `subject.teamID` names the grantee team while the
referenced `MCPServer.spec.teamID` remains the resource-owning authority team.
The grant does not transfer or rewrite server ownership. Cross-team subjects
must be active members of the named team, and their grant must expire within
`MCP_CROSS_TEAM_GRANT_MAX_TTL` (default seven days). Adapter sessions stop at
the grant expiry. Authorization and audit records preserve both dimensions:
the caller's subject team and the server's resource team.
For administrator-created sessions, pass `grantName` to link the session to an
active grant; the API verifies its server and every populated subject field,
then caps session trust and expiry to the grant. Grant-wide session revocation
targets sessions with this explicit link, including adapter-issued sessions.
Manually created sessions without `grantName` are not part of a grant's
revoke-all operation.

The grant controls:

- Whether a specific tool is allowed or denied
- Which side-effect classes are allowed
- The administrator-approved maximum trust
- Whether the grant is disabled

The grant does not prove that the agent has a current session.

Managed agent IDs are created with `mcp-runtime agent create <team-slug>
--name <name>` and selected from the team's agent directory when creating a
grant or session. The directory page and `mcp-runtime agent list|get|rename|
deactivate|reactivate` expose the same records. IDs are generated once and
never reused; do not invent an agent ID for a new managed subject.

## Session: active delegated consent

An `MCPAgentSession` answers:

> Is this human-agent-team identity currently active for this server, and how
> much trust is consented for this session?

Its important fields are:

```yaml
spec:
  serverRef:
    name: payments
    namespace: mcp-team-finance
  subject:
    humanID: user-123
    agentID: coding-agent
    teamID: team-finance-id
  consentedTrust: low
  expiresAt: 2026-06-12T12:00:00Z
  revoked: false
```

For the normal adapter flow:

1. An authenticated platform user asks for a session for a server and agent.
2. The platform verifies the user's team and namespace access.
3. The platform selects a matching enabled grant.
4. Requested trust is capped by the grant's `maxTrust`.
5. The platform creates or reuses an `MCPAgentSession`.
6. The adapter injects the returned identity and session ID into MCP requests.

The session controls expiry, revocation, and consented trust. It does **not**
contain tool rules or `allowedSideEffects`; those remain grant policy.

## Tool metadata: what the server declares

When the MCP server is defined, its owner or deployer classifies each governed
tool:

```yaml
tools:
  - name: list_invoices
    requiredTrust: low
    sideEffect: read
  - name: delete_invoice
    requiredTrust: high
    sideEffect: destructive
```

The gateway uses the declared metadata as its policy input; it does not
inspect tool implementations. A tool that the server never declared, or whose
side-effect metadata is missing or unknown, is denied with
`tool_side_effect_unknown`.

## Gateway decision for every `tools/call`

For a tool call, the gateway authorizes the intersection of identity, policy,
consent, and tool metadata:

```text
valid identity
AND valid matching session
AND matching enabled grant
AND tool allowed by the grant
AND tool side effect allowed by the grant
AND effective trust >= required trust
```

### The decision ladder

The gateway extracts the tool name from the JSON-RPC request, loads the rendered
policy for the target server, and walks these checks in order. **The first
failing rung wins**, and its reason code is what lands in the audit event and the
denial response. A denied call never reaches the MCP server.

| # | Check | Fails with |
|---|---|---|
| 0 | Is `policy.mode: observe`? If so, allow now; nothing below runs. | *(allowed, still audited)* |
| 1 | Is there any human, agent, or team identity? | `missing_identity` (401) |
| 2 | With `session.required: true`: is there a session ID, a matching session, not revoked, not expired? | `missing_session`, `session_not_found`, `session_revoked`, `session_expired` (401) |
| 3 | Does any grant's subject match? Every populated subject field must match exactly. | `no_matching_grant` |
| 4 | Does a tool rule deny this tool, or does no enabled grant allow it? Disabled and expired grants are skipped; a grant with no `toolRules` allows every tool name. | `tool_denied` (403), `tool_not_granted`, `grant_expired` (every matching grant that is not disabled has passed its `expiresAt`) |
| 5 | Did the server declare this tool's side effect, and does the grant's `allowedSideEffects` include it? | `tool_side_effect_unknown`, `side_effect_not_allowed` (403) |
| 6 | Does the grant carry a `maxTrust`? | `grant_without_trust` |
| 7 | Is effective trust at least the required trust? | `trust_too_low` (403) |
| ✓ | Forward to the MCP server and emit the audit event. | `allowed` |

The trust comparison on rung 7 is:

```text
effectiveTrust = min(grant.maxTrust, session.consentedTrust)
requiredTrust  = max(tool.requiredTrust, matchingToolRule.requiredTrust)
```

When no session is required or none matches, `consentedTrust` falls back to the
grant's `maxTrust`.

Reasons without a status code (`no_matching_grant`, `tool_not_granted`, `grant_expired`,
`grant_without_trust`) follow `policy.defaultDecision`: `403` under the shipped
`deny` default, allowed only if a server explicitly sets `defaultDecision: allow`.

Every decision, allowed or denied, emits an audit event containing the identity,
tool, decision, reason, trust values, server, namespace, and policy version.

!!! warning "Observe mode is reporting, not enforcement"
    `policy.mode: observe` allows the call without any identity, session, grant,
    side-effect, or trust check; only the audit trail keeps visibility. Use it to
    preview what enforcement would deny on a new server, then switch back to
    `allow-list`.

Example:

```text
Tool:               delete_invoice
Declared effect:    destructive
Declared trust:     high
Grant tool rule:    allow
Grant side effects: [read, write]
Grant max trust:    high
Session consent:    high
Result:             deny, side_effect_not_allowed
```

Authorization is an intersection, so the allow rule alone is not enough. The
grant must also permit `destructive`.

## Kubernetes workload identity

The operator and runtime-api use Kubernetes ServiceAccounts and RBAC to perform
cluster operations. This identity plane controls actions such as:

- Reading and reconciling `MCPServer`, `MCPAccessGrant`, and
  `MCPAgentSession` resources
- Creating workloads, Services, Ingresses, Secrets, and namespace-scoped access
  resources
- Reading platform configuration required by the service

Most workloads that do not call the Kubernetes API disable automatic
ServiceAccount token mounting. Kubernetes RBAC is a separate enforcement layer
from platform roles and gateway policy.

## Responsibility summary

| Question | Source of truth |
|---|---|
| Who is operating the platform? | Authenticated platform principal |
| Which tenant resources can they manage? | Role, team membership, namespace scope, and ownership |
| Which human and agent are making the tool call? | Grant/session subject and governance identity headers |
| Is that delegated access active now? | Session expiry and revocation |
| Which tools can be called? | Grant tool rules |
| Which classes of effects are permitted? | Grant `allowedSideEffects` |
| How risky is the tool? | MCPServer tool metadata |
| How much authority is effective? | Minimum of grant maximum and session consent |
| Who enforces the tool call? | MCP gateway |
| Who can change cluster resources? | Kubernetes ServiceAccount and RBAC |

**Next:** [Concepts](concepts.md) for the individual resource model, or
[Multi-Team Isolation](multi-team.md) for namespace and team boundaries.
