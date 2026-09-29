# Module 1: Core concepts

MCP Runtime has six core building blocks: a managed agent directory, three
Kubernetes resources, and two runtime components.

!!! tip "Hold one picture in your head"
    Think of MCP Runtime as a secure office building: the `MCPServer` is a lease,
    the operator is facilities, the gateway is the guard at each suite's door, a
    grant is the rule in the security handbook, and a session is today's visitor
    badge. The agent directory is the building's register of approved service
    identities. The full mapping is in
    [Concepts: the whole thing, as a building](../concepts.md#the-whole-thing-as-a-building).

## What problem is MCP Runtime solving?

When you run MCP servers in production you need answers to three questions:

1. **Who deployed this server, and can they update it?** Kubernetes namespaces
   and RBAC provide isolation.
2. **Which agents are allowed to call which tools?** Grants define the policy.
3. **Did this agent have consent for this call, and has it expired?** Sessions
   carry time-bounded, revocable consent.

MCP Runtime configures all three from the CLI, and the gateway enforces them on
tool calls when allow-list policy is enabled.

## The six building blocks

### 1. Managed agent directory

The platform keeps a directory of logical agent identities owned by teams.
Each entry has an immutable ID such as `agt_01arz3ndektsv4rrffq69g5fav` and a
display name that can change. Use the ID in grants and sessions so audit records
and policy keep referring to the same agent if its name or client changes.

An agent ID is a directory reference, not a credential. It does not prove that
a request came from Claude, Cursor, or any particular program. The adapter's
session-bound certificate authenticates its Runtime session; OAuth is a separate
credential when the server enables it. An OAuth `client_id` also does not
replace the managed agent ID.

Team owners and platform admins create agents. Members can see active agents
covered by an applicable grant or their own active session. List them with
`mcp-runtime agent list <team-slug> --status active`; use the ID shown in the
output. See [Managed agents](../concepts.md#managed-agents) for deactivation
and access details. The ID is like a directory number: it gives policy and
audit records a stable name, while the certificate or OAuth token proves the
request's authentication.

### 2. MCPServer

A Kubernetes CRD that describes a running MCP server. You create it with
`mcp-runtime server deploy`. The operator reconciles it into a `Deployment`,
`Service`, and `Ingress`.

```
MCPServer
  name: payments
  image: registry.example.com/acme/payments:v1
  port: 8088
  gateway.enabled: true   ← enables the policy sidecar
  tools:
    - name: list_invoices
      requiredTrust: low
      sideEffect: read
```

The gateway uses the `tools` list for policy decisions. With the allow-list
policy scaffolded by `server init`, calls to undeclared tools are denied.

### 3. MCPAccessGrant

A policy that lets an agent, acting for a team, call specific tools on a
server up to a trust level.

For platform API and adapter flows, use an active managed agent ID from the
team directory. The ID in these examples shows the required format.

```
MCPAccessGrant
  serverRef: payments
  subject:
    agentID: agt_01arz3ndektsv4rrffq69g5fav
    teamID: <team-uuid>       ← which team this grant covers
  maxTrust: low               ← ceiling, even if session claims higher
  allowedSideEffects: [read]
  toolRules:
    - name: list_invoices
      decision: allow
      requiredTrust: low
    - name: delete_invoice
      decision: deny          ← explicitly blocked
```

The gateway denies by default: without a grant, the agent has no access.

### 4. MCPAgentSession

A time-bounded resource that records a human, agent, and team identity for one
server, along with the trust the user consented to, an expiry, and revocation
state. It is not an OAuth token. The adapter's certificate proves which session
is calling.

```
MCPAgentSession
  serverRef: payments
  subject:
    agentID: agt_01arz3ndektsv4rrffq69g5fav
    teamID: <team-uuid>
  consentedTrust: low    ← human approved this level
  expiresAt: ...         ← gateway rejects after this
  revoked: false         ← set true to block later calls after policy refresh
```

When `session.required: true`, the gateway checks for an active matching
session on each tool call. `server init` enables this by default. Servers with
optional sessions use the grant's `maxTrust` when no session matches.

The adapter creates or reuses a session at startup when you provide `--server`
and `--agent`. `--auto-refresh` renews the certificate before it expires; it
does not renew the OAuth token. Create sessions manually when an administrator
needs explicit control over expiry or revocation.

### 5. The gateway

A sidecar container injected next to your MCP server when `gateway.enabled: true`.
Every request passes through it. On each tool call it:

1. Validates the OAuth bearer when the server enables OAuth and resolves an adapter's session identity from its verified certificate
2. Looks up the matching grant and, when `session.required: true`, an active session
3. Checks trust level, side-effect class, and per-tool rules
4. Forwards or denies
5. Emits an analytics event

Your server code does not change.

```
MCP client → gateway sidecar (port 8091) → your server (port 8088)
```

### 6. The adapter

A local proxy that runs on your machine or inside an agent process. It:

- Calls the platform API to create or reuse an authorized session
- Presents a session-bound client certificate and, for OAuth-enabled targets, a bearer token to the gateway
- Refreshes the certificate before it expires (`--auto-refresh`)

```
MCP client → adapter proxy (localhost:8099) → gateway → server
```

Your MCP client does not need to manage platform sessions or certificates.

## How the policy engine helps

The operator combines server tool metadata, grants, and any required session
into a policy document for that server. The gateway reads that policy for each
`tools/call` and allows or denies the call before it reaches the MCP app. This
keeps authorization in one consistent place, gives server owners access rules
they can change without rebuilding the app, and records why a call was allowed
or denied. The policy uses the side effects and trust levels you declare; it
cannot inspect the server code to verify those declarations.

`server init` uses allow-list policy with a default deny decision. For a staged
rollout, `policy.mode: observe` records policy decisions but lets calls through.
See [Policy engine](../concepts.md#policy-engine) for the full evaluation flow.

## The decision table

| Scenario | Grant needed? | Session needed? |
|---|---|---|
| Agent calling tools with allow-list policy | Yes | Yes when `session.required: true` |
| Agent calling tools when sessions are optional | Yes | No; trust falls back to the grant's `maxTrust` |
| Block agent from a specific tool | Yes (deny rule) | — |
| Cap trust regardless of what agent claims | Yes (`maxTrust`) | — |
| Time-limit access | — | Yes (`expiresAt`) |
| Revoke later calls | — | Yes (`revoked: true`; gateways load the change after policy refresh) |
| Share one server between two teams | Yes (with `teamID`) | Yes (with `teamID`) |

## Trust levels

| Level | When to use |
|---|---|
| `low` | Read-only, reversible, low-risk |
| `medium` | Writes or operations with noticeable side effects |
| `high` | Destructive, irreversible, or high-impact |

With `maxTrust: low` on a grant, the gateway caps effective trust at `low`,
even when the session claims `high`.

## Side effects

| Value | What it means |
|---|---|
| `read` | Fetches or queries; no state change |
| `write` | Creates or modifies records |
| `destructive` | Deletes, wipes, or makes irreversible changes |

A grant with `allowedSideEffects: [read]` blocks any tool whose `.mcp/servers.yaml`
metadata declares `sideEffect: write`, even if that tool is in the allow list.
Tool names and side effects in your metadata must match the server's
implementation. A mismatch causes `tool_side_effect_unknown` at the gateway.

## Check your understanding

Before Module 2, make sure you can answer:

1. What does the gateway check on every tool call?
2. What is the difference between a Grant and a Session?
3. Why do tool names in `.mcp/servers.yaml` have to match the server's actual implementation?
4. What does `maxTrust: low` on a Grant mean when the Session has `consentedTrust: high`?

**Next:** [Module 2: Your first governed server](module-2-first-server.md)
