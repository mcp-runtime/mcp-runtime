# Gateway and Platform Inspiration Audit

Date: 2026-06-13

## Goal

Identify gateway and platform capabilities that MCP Runtime can adopt from
current MCP-native and AI gateway projects without turning the runtime into a
generic API or model gateway.

This document is background research: an inspiration and gap analysis, not an
implementation spec, roadmap commitment, or proposal to copy third-party
source. Nothing here describes shipped behavior beyond the "What MCP Runtime
already has" table, and the numbered items, milestones, and example YAML are
sketches. Any implementation must be designed against MCP Runtime's existing
CRDs, policy contract, sidecar model, and Sentinel services, and reviewed with
the shared `.codex/skills/_shared/design-principles.md` guide before it becomes a contract.

## Sources reviewed

Primary project documentation and repositories:

| Project | Useful product ideas | Source |
|---|---|---|
| agentgateway | Virtual MCP federation, dynamic backend selection, MCP rate limits, stateful routing, conditional policies, retries/timeouts, external processing, policy inheritance, request tracing | <https://agentgateway.dev/docs/llms.txt> |
| Kong AI Gateway | Plugin-oriented policy stages, traffic maps, metering, developer portal, protocol-level MCP and A2A governance | <https://developer.konghq.com/ai-gateway/> |
| Portkey | Conditional routing, retries, fallbacks, circuit breakers, budgets, quotas, load balancing, canary traffic | <https://portkey.ai/docs/product/ai-gateway> |
| Microsoft MCP Gateway | Session-aware routing, direct and unified tool routes, lifecycle APIs, logs/status, browser JSON-RPC console | <https://github.com/microsoft/mcp-gateway> |
| Docker MCP Gateway | Isolated local execution, secrets, OAuth flows, dynamic discovery, multiple catalogs, call tracing | <https://github.com/docker/mcp-gateway> |
| IBM ContextForge | MCP/REST/gRPC federation, protocol-version selection, plugins, guardrails, unified discovery | <https://github.com/IBM/mcp-context-forge> |
| ToolHive | Curated registries, one-click installation, local-to-cluster workflow, optimizer, OIDC token exchange, portal experience | <https://github.com/stacklok/toolhive> |
| MCP specification 2025-11-25 (superseded by [2026-07-28](https://modelcontextprotocol.io/specification/2026-07-28/changelog), which removes the `initialize` handshake and deprecates roots and sampling) | Capability negotiation, roots, sampling, elicitation, progress, cancellation, consent requirements | <https://modelcontextprotocol.io/specification/2025-11-25> |

## What MCP Runtime already has

Do not create duplicate roadmap items for these capabilities.

| Capability | Current implementation |
|---|---|
| Kubernetes-native server lifecycle | `MCPServer` reconciles Deployments, Services, Ingress, policy ConfigMaps, readiness, and rollout state. |
| Per-server gateway | Optional `mcp-gateway` sidecar with inspect, policy, auth, authz, upstream, and audit stages. |
| Tool governance | Grants and sessions enforce tool allowlists, trust ceilings, consent, side-effect classes, expiry, and revocation. |
| OAuth upstream exchange | OAuth policy mode, token validation/exchange, and upstream token injection exist. |
| Catalog and discovery | Scoped server and tool catalogs expose declared and live inventory, connect config, and drift state. |
| Protocol inventory | Live probes discover tools, prompts, and resources and cache results for 30 seconds. |
| Observability | Gateway metrics, audit events, ClickHouse analytics, OpenTelemetry traces, logs, Grafana, and scoped dashboards exist. |
| Multi-tenancy | Platform roles, teams, namespaces, catalog modes, ownership, user keys, and registry authorization exist. |
| Rollout primitives | Rolling, recreate, and canary deployment strategies exist. |
| Adapter reliability | Idempotent reads retry selected transient failures through the HTTP proxy. |
| Operations UI | Server catalog, governance, activity, platform operations, components, analytics, and observability views exist. |

## Highest-value missing capabilities

### P0: Protocol coverage and governance

#### 1. Full protocol capability inventory

The live inventory only models tools, prompts, and resources. Add negotiated
capabilities and protocol metadata:

- negotiated protocol version;
- server capabilities and instructions;
- resource subscriptions;
- completions;
- logging;
- task support when standardized and adopted;
- client-advertised roots, sampling, and elicitation capabilities.

Store declared versus live capability drift in the catalog. Do not silently
assume a server supports a method because it appears in static metadata.

Likely ownership:

- `services/runtime-api/internal/runtimeapi/live_inventory.go`
- `services/runtime-api/internal/runtimeapi/tools.go`
- `api/v1alpha1/mcpserver_types.go`
- `services/ui/frontend/` and the generated `services/ui/static/` bundle

#### 2. Method-level policy beyond `tools/call`

The gateway inspects all JSON-RPC methods but authorization is primarily
tool-call oriented. Extend policy to cover:

- `resources/read` and resource URI patterns;
- `prompts/get`;
- subscriptions;
- server-initiated `sampling/createMessage`;
- server-initiated elicitation;
- roots exposure;
- logging level changes.

Sampling and elicitation need explicit human consent and bounded data
disclosure. Default-deny server-initiated capabilities until a grant and active
session explicitly allow them.

Likely ownership:

- `pkg/policy/`
- `api/v1alpha1/access_types.go`
- `internal/operator/policy.go`
- `services/mcp-gateway/rpc.go`
- `services/mcp-gateway/filter_authz.go`

#### 3. MCP conformance status

Expose the result of automated protocol checks per server:

- initialize/version negotiation;
- required response headers;
- JSON-RPC envelope validity;
- session behavior;
- pagination and notifications;
- advertised capability correctness;
- malformed request and cancellation behavior.

Use the existing `mcp-protocol-compliance` checks as the implementation seed.
Display last checked time, tested protocol version, failures, and warnings in
the catalog.

### P0: Traffic safety and resilience

#### 4. Gateway-native timeouts

Add explicit, bounded defaults for:

- downstream request timeout;
- upstream response-header timeout;
- stream idle timeout;
- maximum request body size by method;
- maximum response size for non-streaming responses.

These belong in `GatewayConfig`, not only process environment variables.
Streaming MCP responses must use idle timeouts rather than a short absolute
deadline.

#### 5. Safe retry policy

Move retry behavior into the gateway for protocol-safe methods:

- default retryable: `ping`, list operations, and other read-only requests;
- default non-retryable: `tools/call`, writes, elicitation, sampling, and
  unknown methods;
- retry only before response headers are committed;
- exponential backoff with jitter and per-try timeout;
- expose attempt count in metrics and audit events.

Never infer that a tool is idempotent from its name. An explicit tool annotation
or policy field is required before retrying `tools/call`.

#### 6. Concurrency and rate limits

Support limits by server, tool, team, human, agent, and session:

- concurrent in-flight calls;
- requests per interval;
- expensive or destructive tool quotas;
- optional distributed counters for multi-replica enforcement;
- `Retry-After` and stable JSON-RPC/HTTP denial details;
- limit decisions in audit and Prometheus metrics.

Start with local per-sidecar concurrency limits because they are deterministic
and do not require a new datastore. Add distributed quotas only after the
policy and failure semantics are stable.

#### 7. Circuit breaking and outlier status

Track upstream transport failures and latency to:

- stop sending calls to an unhealthy server instance;
- distinguish readiness failure from policy denial;
- expose open/half-open/closed state;
- avoid retry storms;
- surface outliers in operations UI.

Because the current gateway is a same-pod sidecar, the first implementation can
be a per-server breaker. Cross-instance ejection only becomes useful with a
shared or federated gateway.

### P1: Federation and routing

#### 8. Virtual MCP endpoint

Provide one governed endpoint that can expose approved tools from multiple
servers. Requirements:

- deterministic namespacing and collision handling;
- filtered `tools/list`, `prompts/list`, and `resources/list`;
- route `tools/call` by catalog identity, not free-form semantic guesses;
- preserve per-server grants, sessions, audit identity, and policy revisions;
- return partial availability metadata when one backend is unhealthy;
- prevent a virtual endpoint from widening access beyond each source server.

This is the clearest product gap relative to agentgateway, Microsoft MCP
Gateway, ContextForge, and ToolHive.

#### 9. Session affinity and resumability

The current sidecar topology naturally reaches one server pod, but a shared or
virtual gateway needs:

- `Mcp-Session-Id` affinity;
- stateless and stateful backend declarations;
- shared session routing state or deterministic consistent hashing;
- explicit behavior when a backend disappears;
- session termination forwarding;
- no cross-tenant session-key collisions.

#### 10. Multi-cluster catalog federation

Federate metadata before federating traffic:

1. read-only remote catalog import;
2. health and capability synchronization;
3. trust-domain and ownership mapping;
4. explicit route publication;
5. cross-cluster traffic with identity propagation.

Do not start with transparent cross-cluster proxying. Catalog federation gives
useful discovery without immediately creating a large security boundary.

### P1: Extensibility and guardrails

#### 11. External policy/processing hooks

Add a narrow extension contract around existing pipeline stages:

- pre-authorization context enrichment;
- request validation or redaction;
- post-response inspection;
- asynchronous audit enrichment.

Use versioned HTTP or gRPC contracts with deadlines, payload limits, failure
mode (`fail-closed` or `observe`), and explicit allowed mutations. Do not load
untrusted in-process plugins into the gateway.

Useful integrations include DLP/PII scanning, malware scanning for resource
content, organization-specific approval systems, and OPA/Cedar-style decision
services.

#### 12. Tool integrity and approval workflow

Build on existing declared/live drift:

- snapshot tool schemas and descriptions;
- require approval for new or changed capabilities;
- flag tool poisoning patterns and suspicious description changes;
- attach image digest, signature, SBOM, and provenance status;
- quarantine unapproved drift without taking the entire server offline;
- retain approval history and reviewer identity.

#### 13. Human approval for high-risk calls

For destructive or high-risk tools, support:

- pending approval state;
- approver role and team scope;
- expiry and single-use approval tokens;
- request argument digest binding;
- deny on argument mutation after approval;
- UI and API queue;
- complete decision audit trail.

### P1: Developer and operator experience

#### 14. Browser MCP inspector

Add an authenticated test console inspired by Microsoft MCP Gateway and the
official MCP Inspector:

- initialize and capability display;
- tools/prompts/resources browsing;
- schema-driven call forms;
- raw JSON-RPC view;
- streaming event view;
- identity/session selector;
- policy denial explanation;
- request trace link.

The console must use the same gateway route and authorization as real clients.
Do not add an admin bypass.

#### 15. Policy simulator and explain API

Given principal, agent, session, method, tool/resource/prompt, and arguments:

- show the policy revision evaluated;
- show each allow/deny condition;
- identify the grant/session that contributed;
- indicate trust and side-effect calculations;
- make simulation side-effect free;
- permit regular users to simulate only identities and resources they control.

#### 16. Curated installation workflow

Evolve the catalog from discovery to controlled installation:

- organization collections and tags;
- compatibility and trust badges;
- required secret/OAuth setup;
- one-click or generated deployment;
- preflight checks;
- version upgrade and rollback;
- approval before public or organization-wide publication.

### P2: FinOps and product analytics

#### 17. Usage quotas and chargeback

MCP tools do not have a standard token cost, so use explicit units:

- calls;
- execution milliseconds;
- bytes in/out;
- configured per-tool cost units;
- infrastructure CPU/memory attribution where available.

Support budgets and alerts by team, server, tool, agent, and environment.
Keep enforcement separate from reporting until accounting is trustworthy.

#### 18. SLOs and error budgets

Define server/tool SLOs for availability and latency, then expose:

- burn rate;
- policy-denial rate separately from service errors;
- upstream error rate;
- saturation and concurrency;
- release/canary comparison;
- alert links and recent traces.

#### 19. Traffic replay and shadow testing

For upgrades and policy changes:

- record redacted request envelopes with strict retention;
- replay only methods explicitly classified as safe;
- shadow list/read traffic to canaries;
- compare schemas, status, latency, and response shape;
- never replay destructive calls or secrets by default.

## Capabilities to defer or reject

| Inspiration | Decision | Reason |
|---|---|---|
| Universal LLM provider API | Reject for now | MCP Runtime governs MCP servers and agents; model normalization is a separate product boundary. |
| Semantic model routing | Reject for now | It introduces model gateway concerns and nondeterministic routing unrelated to MCP tool governance. |
| Prompt rewriting/RAG injection | Reject for now | Mutating model prompts is outside the current MCP server delivery and policy contract. |
| Semantic tool routing | Defer | Route by stable catalog identity first. Semantic selection can create surprising or unsafe tool execution. |
| Response caching for `tools/call` | Reject by default | Tool calls can have side effects and responses may be identity-sensitive. |
| Arbitrary in-process plugins | Reject | They weaken gateway isolation and make the trusted computing base unbounded. |
| Bundled agent execution runtime | Defer | Running model loops and shell/file tools is a distinct sandboxing problem. |

## Recommended delivery order

### Milestone 1: Protocol-safe gateway

1. Add gateway timeouts, body limits, and stream idle handling.
2. Add method classification and method-level policy.
3. Add local concurrency limits and structured limit denials.
4. Expand capability inventory and conformance status.
5. Add retry metrics and audit fields for protocol-safe reads.

### Milestone 2: Trustworthy developer experience

1. Add policy explain/simulation API.
2. Add browser inspector using real governed sessions.
3. Add tool schema snapshots, approval, and drift quarantine.
4. Add SLO and breaker state to operations UI.

### Milestone 3: Virtual MCP

1. Define a `VirtualMCPServer` API or equivalent platform resource.
2. Implement deterministic federation of list operations.
3. Implement stable namespaced routing for calls and reads.
4. Add session affinity and partial-backend health.
5. Add multi-cluster catalog import after single-cluster federation is stable.

## First implementation slice

The smallest high-value code change is gateway-native request limits:

```yaml
spec:
  gateway:
    enabled: true
    limits:
      maxRequestBodyBytes: 1048576
      maxConcurrentRequests: 32
      requestTimeout: 30s
      streamIdleTimeout: 5m
```

Expected behavior:

- reject oversized requests before JSON parsing;
- acquire concurrency capacity before policy evaluation;
- return `429` with `Retry-After` when saturated;
- enforce a bounded request deadline for non-streaming calls;
- preserve streaming responses with an idle timeout;
- emit limit reason, configured threshold, and observed value in audit events;
- publish bounded-cardinality limit metrics;
- default conservatively while preserving current behavior when fields are
  omitted during the alpha API period.

This slice improves production safety without requiring federation, a new
datastore, or a new service.
