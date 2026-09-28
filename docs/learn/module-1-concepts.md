# Module 1: Core concepts

MCP Runtime has five core abstractions. Every term in the CLI and docs maps to
one of them.

!!! tip "Hold one picture in your head"
    Think of MCP Runtime as a secure office building: the `MCPServer` is a lease,
    the operator is facilities, the gateway is the guard at each suite's door, a
    grant is the rule in the security handbook, and a session is today's visitor
    badge. The full mapping is in
    [Concepts: the whole thing, as a building](../concepts.md#the-whole-thing-as-a-building).

## What problem is MCP Runtime solving?

When you run MCP servers in production you need answers to three questions:

1. **Who deployed this server, and can they update it?** Kubernetes namespaces
   and RBAC provide isolation.
2. **Which agents are allowed to call which tools?** Grants define the policy.
3. **Did this agent have consent for this call, and has it expired?** Sessions
   carry time-bounded, revocable consent.

MCP Runtime configures all three from the CLI, and the gateway enforces them on
every call.

## The five abstractions

### 1. MCPServer

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

The gateway uses the `tools` list for every policy decision. Calls to a tool
that is not listed are denied.

### 2. MCPAccessGrant

A policy that lets an agent, acting for a team, call specific tools on a
server up to a trust level.

```
MCPAccessGrant
  serverRef: payments
  subject:
    agentID: cursor
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

### 3. MCPAgentSession

A time-bounded, revocable token that ties an agent identity to a grant.
It carries the trust level the user consented to for this interaction.

```
MCPAgentSession
  serverRef: payments
  subject:
    agentID: cursor
    teamID: <team-uuid>
  consentedTrust: low    ← human approved this level
  expiresAt: ...         ← gateway rejects after this
  revoked: false         ← set true to block immediately
```

The gateway checks the session on every tool call and denies calls without a
valid session.

The adapter creates sessions for you (`--auto-refresh`). Create them manually
when you need explicit control over expiry or revocation.

### 4. The gateway

A sidecar container injected next to your MCP server when `gateway.enabled: true`.
Every request passes through it. On each tool call it:

1. Authenticates every OAuth client and verifies an adapter certificate when present
2. Looks up the active grant and session
3. Checks trust level, side-effect class, and per-tool rules
4. Forwards or denies
5. Emits an analytics event

Your server code does not change.

```
MCP client → gateway sidecar (port 8091) → your server (port 8088)
```

### 5. The adapter

A local proxy that runs on your machine or inside an agent process. It:

- Calls the platform API to create or reuse an authorized session
- Presents a session-bound client certificate and, for OAuth-enabled targets, a bearer token to the gateway
- Refreshes the certificate before it expires (`--auto-refresh`)

```
MCP client → adapter proxy (localhost:8099) → gateway → server
```

Your MCP client does not need to manage platform sessions or certificates.

## The decision table

| Scenario | Grant needed? | Session needed? |
|---|---|---|
| Agent calling any tools on a server | Yes | Yes |
| Block agent from a specific tool | Yes (deny rule) | — |
| Cap trust regardless of what agent claims | Yes (`maxTrust`) | — |
| Time-limit access | — | Yes (`expiresAt`) |
| Instantly revoke mid-flight | — | Yes (`revoked: true`) |
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
