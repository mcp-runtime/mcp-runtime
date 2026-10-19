# ADR: Optional Idempotency-Key for governed tool calls

Status: accepted (alpha). Tracks historical audit item H6 of issue #110.

## Context

A client that times out or loses its connection after sending a mutating
`tools/call` cannot tell whether the tool ran. Retrying blindly can run it twice.
The JSON-RPC `id` is chosen by the client, is not unique across sessions or
principals, and is not an authorization-bound identity, so it must never be the
deduplication identity by itself.

## Decision

The gateway offers an **opt-in** `Idempotency-Key` request header. Without the
header nothing changes: no hashing, no storage, no extra headers.

### Contract

- Applies only to a governed `tools/call` (`POST`, JSON body, tool-call method)
  that was **allowed by the current authorization decision** for this request.
  The key is evaluated after authentication, session/revocation checks and grant
  evaluation, so a replay is never served from stale authorization: a revoked
  session, removed grant or changed policy denies the retry exactly as it would
  a first call.
- The header on any other request (non-tool method, GET, non-JSON) returns
  `400 idempotency_unsupported_request`. Silently ignoring it would suggest a
  guarantee that does not exist.
- A key is 1 to 255 visible ASCII characters in a single header value;
  otherwise `400 idempotency_key_invalid`.
- **Scope.** A key is scoped to the verified principal (human, agent and team
  from OAuth or the verified adapter certificate), the session ID, and the
  current MCPServer UID from the live policy. The same key from another
  principal, session or server is an unrelated key. Delegated-header servers
  and any request without a verified principal return
  `400 idempotency_requires_verified_identity`; the gateway cannot scope a key
  to an identity it did not verify. A server without a known UID returns
  `503 idempotency_unavailable`.
- **Request hash.** SHA-256 over the key, JSON-RPC method, tool name, JSON-RPC
  `id` and canonical (key-sorted) `params`. A retransmission must therefore
  reuse the same JSON-RPC `id`; that keeps the replayed response's `id` correct.
  The same key with a different hash returns `409 idempotency_key_conflict`.
- **Outcomes** for the first request with a key:
  - upstream returns 2xx with a non-streaming body within the size cap: stored
    and replayed (`200`/original status, original `Content-Type` and body, plus
    `Idempotency-Replayed: true`);
  - upstream returns 4xx, or the gateway refused before dispatch (circuit open,
    backpressure): the request was not executed or definitively rejected, so the
    key is released and a retry runs normally;
  - upstream returns 5xx, times out, disconnects or the body read fails: the
    outcome is ambiguous. A `failed-unknown` tombstone is stored and a retry
    returns `409 idempotency_outcome_unknown`. The gateway **never re-executes**
    the tool; the client must reconcile through the tool's own state;
  - the response carries a `Content-Encoding` (other than identity), is a stream
    (`text/event-stream`) or exceeds the reply cap. Content-encoded replies are
    not stored because replay would need `Accept-Encoding`/`Vary` negotiation,
    and the simpler safe choice is to deliver once and refuse replay. The reply
    is delivered to the first caller unbuffered, a tombstone is stored, and a
    retry returns `409 idempotency_response_not_replayable`. Streaming
    responses are therefore not deduplicated; they are only protected against
    duplicate execution.
- A request that arrives while the first is still in flight returns
  `409 idempotency_in_flight` with `Retry-After: 1`.
- The gateway never retries upstream calls itself.

### Limits (per gateway sidecar, in memory)

| Limit | Value |
|---|---|
| Entry TTL (completed replies and tombstones; starts at completion) | 5 minutes |
| Max entries (global) | 256 |
| Max in-flight entries (global) | 32 |
| Max entries / in-flight per scope (principal + session) | 32 / 4 |
| Max entries / in-flight per principal across sessions | 64 / 8 |
| Max tombstones per scope | 8 |
| Max stored reply body | 32 KiB |

Quotas nest: one scope or principal filling its share does not block others.
When a quota or the global entry cap is reached, the store evicts the oldest
**completed replayable** entry (within the scope, principal or globally,
respectively) instead of refusing. It never evicts an in-flight entry or a
tombstone, because evicting a tombstone would allow re-execution after an
ambiguous outcome, and evicting an in-flight entry would let a duplicate start.
If nothing is evictable, or an in-flight cap is reached, or the scope's
tombstone budget is spent or reserved by in-flight keys (each could end in a
tombstone the store must keep), keyed requests of that scope fail closed with
`429 idempotency_capacity`; unkeyed traffic is unaffected. Eviction of a
completed entry loses only the replay (a later retry may run the tool again), so
an attacker can shorten a victim's replay window only by filling the global cap,
which requires several principals' worth of quota.
In-flight entries are exempt from TTL expiry; they leave only by completion,
tombstone or release, so a slowly streaming body cannot let a duplicate start.
Completed entries and tombstones expire by TTL.
Each in-flight key reserves one tombstone slot until its outcome is known.
A replayable completion or release frees that reservation; an ambiguous or
non-replayable outcome consumes the slot without evicting another tombstone.

A 2xx reply that carries a JSON-RPC error or an `isError` tool result is stored
and replayed like any other 2xx. The tool ran and produced a definitive answer;
re-running it on a retry could repeat a side effect. Clients that want to
re-run after a tool-level error must use a new key.

### Observability

Audit events and metrics never carry the raw key or payload. Audit events add
`idempotency` (outcome enum) and `idempotency_key_hash` (first 16 hex characters
of the scoped SHA-256 over key, principal, session and server UID). That hash is
truncated and unsalted: someone who already holds the audit log could brute-force
it only for low-entropy keys, which is acceptable here because the log already
names the principal and session. Use high-entropy keys (for example UUIDs). Metric `mcp_gateway_idempotency_total{namespace,server,outcome}`
uses a bounded outcome enum: `first`, `replay`, `conflict`, `in_flight`,
`unknown_outcome`, `not_replayable`, `capacity`, `rejected`.

## Consequences and failure modes

- State is per sidecar process. A gateway restart or a retry that lands on
  another replica of the same server is not deduplicated (the first call may
  execute again). The key narrows duplicate execution; it is not a distributed
  exactly-once guarantee. Clients needing that must make the tool itself
  idempotent.
- The 5-minute window bounds how long a retry is protected.
- Memory is bounded (about 8 MiB of stored replies worst case).
- Policy file reload (5 s polling) and session sync (500 ms polling) bound how
  quickly revocation takes effect, as for every other request; the idempotency
  layer adds no additional staleness because it runs after authorization.
- Replayed responses carry only status, `Content-Type` and body; upstream
  response headers are not stored.
- Rejected alternatives: deduplicating by JSON-RPC `id`; a shared (Redis or
  database) store, which adds a stateful dependency and a new trust boundary for
  an alpha opt-in feature; automatic gateway-side retries.

## Store interface and planned work

The gateway talks to an `idempotencyStore` interface
(`services/mcp-gateway/idempotency_store.go`: `begin`, `complete`, `tombstone`,
`release`). Keys given to a store are already scoped hashes. Only the bounded
in-memory implementation ships and is unit-tested.

Planned for later, not implemented and not tested here:

- A store shared across gateway replicas (for example backed by the platform
  API or a database). It must implement the same interface with atomic `begin`
  and the same "never re-execute after an ambiguous outcome" rule. Until then,
  deduplication is per sidecar process (see Consequences).
- Live-cluster proof of the end-to-end path. Everything here is verified with
  in-process HTTP tests against the real gateway pipeline and a fake upstream;
  no cluster, Kind or E2E run covers it.
