# MCP Runtime Design Principles

Apply these when designing or reviewing **anything with a contract or a trust boundary** in MCP Runtime: REST/JSON-RPC APIs, CRD shapes (`api/v1alpha1`), CLI flags, operator reconciliation, the gateway pipeline, platform services, and MCP tool surfaces. Prefer these over ad-hoc choices; when two are in tension, make the tradeoff explicit (see *Make tradeoffs explicit*).

One-line north star:

> Good design makes the right thing easy, the wrong thing hard, and future change less painful.

## API design (the 12)

1. **RESTful resource naming** — nouns, not verbs. `GET /users/{id}`, not `GET /getUser`.
2. **Simple, predictable URLs** — guessable paths. `/users/{id}/orders`, not `/fetchAllOrdersForUser`.
3. **Correct HTTP methods** — `GET` read, `POST` create, `PUT` full update, `PATCH` partial, `DELETE` delete.
4. **Proper status codes** — 200/201, 400/401/403/404/409, 500. Don't return 200 with an error body.
5. **Consistent request/response shape** — predictable JSON envelope (`{ "data": …, "error": … }`).
6. **Clear, coded errors** — `{ "code": "INVALID_EMAIL", "message": "…", "field": "email" }`, never just "failed".
7. **Filtering, sorting, pagination** on list APIs — `?status=active&sort=created_at&page=1&limit=20`. Never return everything.
8. **Don't abuse query params for identity** — prefer nested paths (`/users/123/orders`) over `?user_id=123` for core relationships.
9. **Version your APIs** — `/api/v1/…` so breaking changes don't break old clients.
10. **Clear actions for non-CRUD** — `POST /orders/{id}/cancel`, `POST /payments/{id}/retry` as explicit sub-resources.
11. **Model meaningful state transitions** — `CREATED → PAID → SHIPPED → DELIVERED`; reject invalid jumps.
12. **Security from the start** — authN (who are you), authZ (what may you do), rate limiting, input validation, no sensitive-data leaks.

> API design is about resources, behavior, state, errors, security, and future compatibility — not just endpoints.

## System / generic design principles

**Security & trust**
- **Least privilege** — grant the minimum access needed; every extra permission is future blast radius. (See the operator/runtime RBAC and `pkg/access`.)
- **Secure by default / deny by default** — unsafe behavior requires explicit opt-in. (Gateway default decision is `deny`.)
- **Fail closed, not open** — on auth/policy/validation error or timeout, deny. When confused, don't allow.
- **Validate at boundaries** — validate input where it enters (API, webhook, queue consumer, MCP tool input). Boundaries are where bad data enters.
- **Don't trust client input** — frontend validation is UX; backend validation is security. A client-sent `"role": "admin"` means nothing.
- **Minimize blast radius** — scoped tokens, per-team/namespace isolation, rate limits; assume failure and limit how far it spreads.
- **Audit important actions** — record grants, tool executions, policy/secret access, approvals. Audit is accountability, not just logging.

**Contracts & change**
- **Hide implementation details / avoid leaky abstractions** — expose what the caller needs, not DB tables, vendor names, or internal status codes. The contract should survive internal changes.
- **Stable contracts + backward/forward compatibility** — internal code can move fast; public contracts move carefully. Add optional fields/endpoints; don't remove fields, change types, or change the meaning of a status.
- **Explicit over implicit** — make important behavior visible (`deploy(env="staging", dry_run=true)`), not guessed. Hidden assumptions become bugs.
- **Good defaults** — the default must be safe and useful (deny access, private visibility, bounded retries, sane timeouts/pagination). Most users follow defaults; don't make them type magic strings for the common case.
- **Least surprise** — behave how a reasonable developer expects, even if other behavior is documented.
- **Make tradeoffs explicit** — state them ("async improves latency but status is eventually consistent"); don't hide them.

**Structure**
- **Separation of concerns / single responsibility** — auth checks identity, policy decides permission, business logic acts, audit records. One component, one job; if it has many reasons to change, split it.
- **Loose coupling, high cohesion / least knowledge** — related things together, unrelated apart; each component talks through a clear interface and knows only what it needs.
- **Encapsulation** — callers depend on behavior, not implementation (`payment.charge(...)` hides retries/locks/vendor).
- **Clear ownership** — every datum/behavior has one owner; shared ownership becomes no ownership.
- **Prefer composition over big magic** — small explicit pieces (auth middleware + policy engine + audit logger + handler) over one invisible mega-framework.
- **Make invalid states impossible or hard** — prevent bad state structurally (can't SHIP before PAID), don't just detect it later.

**Operability & evolution**
- **Idempotency where needed** — retries must not duplicate charges/orders/deploys; use idempotency keys. Distributed systems retry.
- **Observability first** — design logs/metrics/traces/audit up front: who called, what, was it allowed, how long, why it failed. If you can't observe it, you can't debug it.
- **Evolution-friendly** — leave room for tomorrow (hide the payment provider behind an interface); don't hardcode one vendor everywhere.
- **Design for testability** — inject dependencies; if testing is painful, the design is too coupled.
- **Feature flags / gradual rollout / kill switch** — risky features should be reversible and rollable per-team/namespace.
- **Progressive disclosure** — simple common case, advanced case possible (`create_user(name, email)` vs full options).
- **Consistency** — similar things look/behave similarly, so learning one part lets you guess the rest.
- **Clear naming** — names explain intent (`refresh_access_token`), reducing the need for comments.
- **Human-friendly errors** — a good error message is part of the product; tell the user how to fix it.

> Good design is least privilege, clear contracts, hidden internals, explicit behavior, safe defaults, strong boundaries, observable decisions, and limited blast radius.

## How this shows up in MCP Runtime

- **Gateway** already embodies *fail-closed*, *deny by default*, *separation of concerns* (inspect → policy → auth → authz → upstream filters), and *don't trust client input* (adapter identity comes only from the verified certificate assertion).
- **CRDs / CLI flags** should follow *good defaults* and *explicit over implicit*: a feature's common path needs no magic strings (e.g. enabling the mTLS path defaults to the managed CA), and prod-affecting side effects are an explicit opt-in.
- **Policy contract** (`pkg/policy`) is *stable-contract* / *backward-compatible* by design — bump `SchemaVersion` only on a real shape change, add optional fields rather than breaking old consumers.
- **Operator** reconciliation favors *idempotency* (Server-Side Apply over read-diff-write) and *minimized blast radius* (NetworkPolicies, scoped certs, per-namespace resources).
