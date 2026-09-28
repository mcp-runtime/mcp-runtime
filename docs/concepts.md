# Concepts

MCP Runtime has three resources (`MCPServer`, `MCPAccessGrant`,
`MCPAgentSession`) and two runtime components (the gateway and the adapter).
For the full authorization model, see
[Identity and authorization](identity-and-authorization.md).

## The whole thing, as a building

If it helps, think of the platform as an office building that rents suites to
tools and admits visiting agents:

| In the building | In MCP Runtime | What that means |
|---|---|---|
| A floor rented to one company | **Namespace** (`mcp-team-acme`) | Hard boundary: one team's servers, secrets, and grants. |
| The signed lease describing the suite | **`MCPServer`** | Declarative description: image, port, route, tools, policy, gateway on/off. |
| Facilities, who reads the lease and builds the room | **Operator** | Turns one `MCPServer` into a Deployment, Service, Ingress, and policy ConfigMap. |
| Lobby reception, pointing visitors to the right floor | **Traefik ingress** | Routes `/<server>/mcp` to the right Service. It routes; it does not authorize. |
| The guard at the suite's own door | **`mcp-gateway` sidecar** | Makes the allow/deny decision on every tool call, inside the pod. |
| The standing rule in the security handbook | **`MCPAccessGrant`** | Long-lived policy: which tools, up to what trust, with which side effects. |
| Today's visitor badge, expiring at 6pm | **`MCPAgentSession`** | Time-boxed, human-consented, instantly revocable. |
| Clearance printed on the badge | **Trust level** (`low` / `medium` / `high`) | A ceiling, never a grant of power on its own. |
| "May look" vs "may edit" vs "may shred" | **Side effect** (`read` / `write` / `destructive`) | What the tool does to data, authorized separately from trust. |
| The escort who carries your badge and renews it | **Adapter** | Local proxy that obtains a session and presents a session-bound SPIFFE client certificate (plus Bearer for OAuth servers). |
| Cameras and the logbook | **Sentinel** | Audit events, analytics, dashboards. |

## MCPServer

An `MCPServer` is a Kubernetes CRD that describes a running MCP server: what image
to run, which port it listens on, how it routes traffic, and whether the governance
gateway is enabled.

When you run `mcp-runtime server deploy`, the operator reconciles an `MCPServer` into
a Kubernetes `Deployment`, `Service`, and `Ingress`. If you delete one of them, the
operator recreates it.

```yaml
# What the operator creates from one MCPServer
Deployment  → runs your server image
Service     → exposes it inside the cluster
Ingress     → routes /<server-name>/mcp to the Service
```

The `MCPServer` also carries policy settings: which tools are governed, what trust
levels they require, and whether a gateway sidecar enforces policy on every call.

## MCPAccessGrant

A grant is a policy document that says: **"Agent X, acting for Team Y, is allowed
to call these tools on server Z, up to this trust level, with these side effects."**

Grants are per-agent and per-server. An agent that has no grant for a server cannot
call any tools; the gateway denies by default.

```
MCPAccessGrant
  serverRef: payments          ← which server
  subject:
    agentID: cursor            ← which agent
    teamID: <globex-uuid>      ← which team (optional)
  maxTrust: low
  allowedSideEffects: [read]
  toolRules:
    - name: list_invoices      ← allowed
      decision: allow
      requiredTrust: low
    - name: delete_invoice     ← blocked
      decision: deny
```

Grants are created with `mcp-runtime access grant init` and applied with
`mcp-runtime access grant apply`. See [CLI reference: access](cli.md#access).

## MCPAgentSession

A session ties an agent identity to a grant for a fixed period of time. It carries:

- `consentedTrust`: the trust ceiling the user consented to
- `expiresAt`: when the session ends (the agent must renew)
- `revoked`: set to `true` to block the agent immediately

The gateway checks the session on every tool call. If there is no valid session, or
the session is revoked, the call is denied regardless of the grant.

```
MCPAgentSession
  serverRef: payments
  subject:
    agentID: cursor
    teamID: <globex-uuid>
  consentedTrust: low          ← human approved this level
  expiresAt: 2026-06-02T12:00Z
  revoked: false
```

In normal use, sessions are created automatically by the adapter when you run
`adapter proxy --server ... --auto-refresh`. You only create them manually when you
need explicit control over expiry, trust ceiling, or revocation.

!!! tip "Why there are two resources"
    A grant is standing policy, like a rule in a security handbook. A session is
    today's visitor badge. A call needs both. Revoking a badge is instant and
    local; changing the handbook is a deliberate policy change.

## Grant or session: which one do I need?

| Scenario | Grant needed? | Session needed? |
|---|---|---|
| Agent calling tools on a server | Yes | Yes |
| Block an agent from a specific tool | Yes (deny rule) | — |
| Limit trust to `low` regardless of what the agent requests | Yes (`maxTrust`) | — |
| Time-limit an agent's access | — | Yes (`expiresAt`) |
| Instantly revoke an agent mid-flight | — | Yes (`revoked: true`) |
| Share one server between two teams | Yes (with `teamID`) | Yes (with `teamID`) |

## Trust levels

Trust is a ceiling, never a grant. The gateway computes two values and compares
them:

```text
effective = min(grant.maxTrust, session.consentedTrust)
required  = max(tool.requiredTrust, matchingToolRule.requiredTrust)
allowed   when effective >= required
```

Effective trust takes the **lower** of what policy permits and what a human
consented to, so either one can hold the line alone. Required trust takes the
**higher** of the tool's own bar and any tighter bar in the grant's tool rule, so
a grant can raise the price of a tool but never discount it.

| Level | Meaning |
|---|---|
| `low` | Read-only, reversible, low-risk operations |
| `medium` | Writes or operations with noticeable side effects |
| `high` | Destructive, irreversible, or high-impact operations |

Setting `maxTrust: low` on a grant means even if the agent claims `high` trust in
its session, the gateway caps it at `low`.

## Side effects

Side effects classify what a tool does to data. The grant must explicitly
allow the side-effect class before a call reaches the server.

| Value | When to use |
|---|---|
| `read` | Fetches or queries only; no state change |
| `write` | Creates, updates, or modifies records |
| `destructive` | Deletes, wipes, or makes irreversible changes |

A grant with `allowedSideEffects: [read]` blocks any tool whose metadata declares
`sideEffect: write` or `sideEffect: destructive`, even if that tool is in the
allow list. Trust and side effect are checked independently; both must pass.

!!! warning "Empty is not a wildcard"
    An omitted or empty `allowedSideEffects` authorizes **nothing**. A tool the
    server never declared is denied too (`tool_side_effect_unknown`): with no
    declared side effect, there is nothing for a grant to authorize.

## The gateway

The gateway, `mcp-gateway`, is a sidecar container in each MCP server pod (when
`gateway.enabled: true` on the MCPServer). Traefik routes the public path to the
pod; `mcp-gateway` makes the authorization decision there before the request
reaches your server.

Governance identity comes from a **verified session-bound SPIFFE client
certificate** (Traefik validates the cert and the gateway maps the SPIFFE URI
to an `MCPAgentSession`). On OAuth servers the caller also presents a **Bearer
JWT** for resource authentication; human/agent/team may come from token claims,
but the platform session binding for grants is the certificate.

On each tool call the gateway:

1. Authenticates the caller (Bearer JWT on OAuth routes; SPIFFE cert for the
   adapter session)
2. Looks up the active `MCPAgentSession` and `MCPAccessGrant` for that agent+server pair
3. Checks trust level, side-effect class, and per-tool allow/deny rules
4. Either forwards the call to your server or returns a denial with a reason code
5. Emits an analytics event with the decision

Your server code needs no changes to support the gateway.

!!! note "Two things are called \"gateway\""
    `mcp-gateway` is the per-server enforcement sidecar inside each MCP server pod.
    The Sentinel `gateway` Deployment is the Traefik ingress in front of the
    platform APIs, ingest, and UI. It routes traffic and makes no tool-call
    decisions.

## Adapter

The adapter is a local proxy that runs on the developer's machine (or inside an
agent process). It:

- Calls the platform API to create or refresh an `MCPAgentSession`
- Presents that session with a **session-bound SPIFFE client certificate**
  (`--auth mtls` / `adapter enroll`)
- Forwards the **Bearer** token when the MCP server uses OAuth
- Refreshes the session (and re-enrolls the cert) before expiry (`--auto-refresh`)

The adapter does not make authorization decisions; the gateway does.

```
Your MCP client → adapter proxy (localhost:8099) → gateway → MCP server
```

## Platform mode

Platform mode controls which namespace servers are published into and who can
browse the catalog without logging in.

| Mode | Who sees servers | Catalog namespace |
|---|---|---|
| `tenant` (default) | Only team members | Per-team namespaces |
| `org` | All signed-in users | `mcp-servers-org` |
| `public` | Anyone, no login | `mcp-servers-public` |

In `org` and `public` modes the catalog namespace is added to what a signed-in
user already sees, so their listings cover the shared catalog plus every team
namespace they are authorized for.

Set with `--platform-mode` on `setup` or `MCP_SETUP_PLATFORM_MODE` in your env file.

## Scopes

When publishing a server image (`server push`) or deploying a server
(`server deploy`), `--scope` controls which catalog namespace the server lands in:

| Scope | Namespace | Who can use it |
|---|---|---|
| `tenant` | `mcp-team-<slug>` | Members of that team |
| `org` | `mcp-servers-org` | All signed-in users in the org |
| `public` | `mcp-servers-public` | Anyone |

## What's in a `.mcp/servers.yaml`

The `.mcp/servers.yaml` file is the metadata file that `server init` creates.
It is the source of truth for your server's tool policy. The gateway enforces
what is declared here, and calls to unlisted tools are denied.

```yaml
servers:
  - name: payments
    image: registry.example.com/acme/payments
    imageTag: v1
    scope: tenant
    tools:
      - name: list_invoices      # must match the real tool name in your server
        requiredTrust: low
        sideEffect: read
      - name: create_invoice
        requiredTrust: medium
        sideEffect: write
    policy:
      mode: allow-list
      defaultDecision: deny      # deny everything not in the list
    session:
      required: true
    gateway:
      enabled: true
```

Use `server init --from-server http://localhost:8088` to generate this file
automatically from a running server's `tools/list` response rather than writing
it by hand. Then run `server validate` before deploying to catch mismatches.

!!! warning "`policy.mode: observe` is a migration tool, not a setting to leave on"
    Observe mode allows every tool call before identity, session, grant,
    side-effect, and trust checks run. Traffic is still proxied and audited, so use
    it to see what enforcement *would* deny on a new server, fix the tool
    inventory, then switch back to `allow-list`.

**Next:** [Publish an MCP Server](publish-mcp-server.md): build, push, and deploy your first governed server.
