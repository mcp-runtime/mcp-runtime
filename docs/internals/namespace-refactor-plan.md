# Namespace ownership refactor: implementation plan

Parent: [#548](https://github.com/mcp-runtime/mcp-runtime/issues/548).
Reviewed against the issue and current architecture on 2026-10-01.

## Scope and design decisions

Refactor the existing platform around clear namespace ownership. Keep the
current services, Traefik ingress, platform gateway, MCP gateway sidecars,
databases, and event pipeline. Improve configuration, access, lifecycle code,
and minor code inefficiencies within that architecture.

This is a clean-install design. Remove legacy layout resolution, shared-Secret
fallbacks, and compatibility branches. There is no migration guide or automatic
conversion of an old installation. Setup must detect conflicting old resources
before writes and report the fresh-install requirement. Re-running setup on a
partial **new** installation must remain safe and preserve generated credentials
and persistent data. The user will clean and reinstall the public demo
separately; implementation and validation use disposable environments.

| Namespace | Existing components placed here | Responsibility |
| --- | --- | --- |
| `mcp-runtime` | Operator, webhooks, leader election | Reconciliation and admission. Keep this established namespace. |
| `mcp-platform` | Platform API, runtime API, UI, platform gateway, Postgres, optional bundled auth, admin bootstrap | Product identity, management, sessions, and public control routes. |
| `mcp-analytics` | Analytics API, ingest, processor, Kafka, ClickHouse, their init jobs | Product events, audit/usage queries, backlog, and event storage. |
| `mcp-observability` | Prometheus, Grafana, OTel, Loki, Tempo, dashboard/datasource configuration | Platform metrics, logs, traces, and operational dashboards. |
| `mcp-log-collector` | Existing Promtail node collector | Host-log collection with its separate admission exception. |
| Existing tenant namespaces | MCPServer workloads and their gateway sidecars | Tenant isolation and per-server policy enforcement. |
| Existing infrastructure namespaces | Registry, shared/external Traefik, cert-manager | Their current installation and external ownership. |

The extra `mcp-analytics` namespace separates the existing event pipeline from
operational monitoring. It adds no service or proxy. Kafka/ClickHouse recovery
and event retention can be handled independently of Grafana/log/trace storage.
Each store still has its own backup and retention requirements.

Istio and other Kubernetes platforms provide useful principles: explicit
identity, narrow permissions, local failure handling, connection reuse, and
performance regression checks. This refactor introduces no service mesh, alternate
networking stack, autoscaling controller, or additional proxy layer.

## Implementation work

### 1. One component inventory and clear object ownership

Use `pkg/platforminventory` for component IDs, domains, namespaces, workload
names, and dependencies. Keep release image/container metadata and rollout
order in `internal/platformrelease`, referring to those component IDs. Setup,
update, status, logs, restart, port-forward, doctor, cleanup, and E2E must use
that placement instead of maintaining separate namespace lists.

Remove the versioned layout resolver. Move every workload together with its
Service, ServiceAccount, RoleBinding, ConfigMap, Secret, Job, PVC and policy.
Give each namespace one authoritative declaration and Pod Security policy.
Record owners for cluster-scoped resources; do not rely on cross-namespace
ownerReferences. Protect all management namespaces from tenant provisioning.
Retain useful existing component and command names; rename only where the new
meaning would otherwise be misleading, without adding compatibility aliases.

### 2. Configuration, credentials, and access cleanup

Split the shared configuration by consumer and owner. Use explicit Service
DNS for calls across namespaces. Finish the existing consumer-Secret work:
remove `mcp-sentinel-secrets`, its readers and RBAC, and supply only the keys a
service uses. Keep one authoritative value for a shared key and explicitly
listed derived copies; do not copy an entire domain's Secret elsewhere.

| Consumer | Credential and permission rule |
| --- | --- |
| Platform API | Identity/bootstrap/signing and its own database access; only required named Secret access. |
| Runtime API | Required tenant/server management permissions and API credentials. Give it a narrow local ingest-provisioning copy instead of reading an analytics namespace Secret at runtime. |
| UI | UI/API and shared-session credentials only; no platform signing key, unrestricted database role, or Kubernetes management authority. |
| Analytics API / ingest / processor | Only query/intake/storage credentials needed by each service. No platform database or unrelated Secret access. |
| Grafana | Its admin credential in `mcp-observability`; drift recovery reads that source and retains the existing backup-before-reset behavior. |
| Platform gateway | Static route configuration requires no Kubernetes discovery permissions or API token once Ingress watches are removed. |
| Operator / collectors / helpers | Preserve required narrow access; verify that workload creation, service-account selection, `pods/exec`, or namespace-label changes cannot bypass the intended boundary. |

Review exact API operations before removing RBAC. Disable automatic service
account token mounts for workloads with no Kubernetes API calls. Preserve
registry authentication and CA private-key restrictions from the merged
security work. Keep existing authentication mechanisms; this refactor does not
introduce a new token system.

Credential/config updates must restart only their actual consumers. Test a
Grafana-only and an analytics-only change and assert that unrelated platform
pod templates remain unchanged. Re-running setup must preserve authoritative
values and reconcile intended copies without rotating credentials accidentally.

### 3. Adapt current routing and dependency behavior

Keep the existing external Traefik and platform gateway. Use one platform-host
Ingress and TLS Secret in `mcp-platform`, targeting the platform gateway.
Consolidate that gateway's existing routes in its file-provider configuration,
with backend Service DNS in the appropriate namespace. Remove redundant
hostless Ingress objects and the gateway's Kubernetes discovery RBAC. This
supports the namespace split using the existing proxy, without another hop.
The MCP and registry hosts keep their existing routes and owners.

Keep one route table for targets, exact/prefix priority and middleware. Test
adapter sessions, registry push, auth/admin APIs, analytics, `/ingest`, UI and
admin-gated `/grafana`; preserve redaction and TLS renewal ownership. Update
cross-namespace NetworkPolicies using both namespace and pod selectors, required
ports, DNS and API-server access. Verify allowed and denied paths on a CNI that
enforces the policies. Test direct backend access and path handling for auth
bypasses.

Keep the current API division: platform API owns identity, runtime API owns
server/grant/session management, analytics API owns event and usage queries.
Runtime API's existing direct analytics reads are an explicit, limited
cross-domain dependency for this refactor. Keep their tenant checks, bounded
timeouts, and narrow database access. Optional event/dashboard queries must
report analytics unavailability without making unrelated control operations
unready. Do not add a new internal API hop solely to hide that dependency.

Telemetry export remains asynchronous and bounded. The gateway currently has
an in-memory event queue that can drop events; expose failures and drops and
verify existing recovery behavior. Namespace separation does not establish a
new audit durability guarantee. Authorization remains fail-closed.

### 4. Setup, management, recovery, and documentation

Apply namespace/admission declarations before namespaced objects, then
credentials/storage/init jobs, dependent services, and public routes. Keep
setup idempotent for the new layout. Update existing CLI/UI management and
`update --only` component selection to resolve the correct owner and namespace;
avoid introducing a second management command hierarchy. Adjust help and flags
only where their current semantics become misleading.

Doctor and status must distinguish disabled, missing, degraded, and failed
components. Backup/restore and cleanup must use exact owners and preserve
retained data and external infrastructure. Verify Postgres, Kafka/ClickHouse,
Grafana, Loki/Tempo and registry recovery through the existing supported paths.
Cleanup previews its actual targets and never treats a broad managed label as
permission to remove unrelated resources.

Update README, `AGENTS.md`, architecture/component-inventory docs, CLI help and
goldens, Sentinel/deployment/k3s runbooks, contributor and Staging E2E guides,
and relevant diagrams in the same implementation stack. Update canonical
skills `production-platform`, `contributor-cluster`, `cluster-ops`,
`dashboard-browser-qa`, `security-audit`, and affected access-governance
references. Keep `.claude/skills` linked to `.codex/skills`. Add a concise
changelog entry for the breaking clean-install layout; no migration guide.

### 5. Minor optimization and regression checks

Limit optimizations to clear source-level improvements: remove duplicated
work, reuse existing HTTP clients, preserve bounded timeouts and retries, and
remove unused permissions. Use the current performance checks to verify that
these changes and the namespace move do not regress existing behavior. Keep
the MCP call path through the existing per-server gateway; preserve session
revocation and tool-policy checks.

Request paths may be shortened when code inspection proves a forwarding step
or repeated read is unnecessary. The UI already calls runtime and analytics
services directly through its authenticated session proxy; preserve that
direct routing with the new Service DNS. Reuse results within a request only
for the same principal and tenant. Keep steps that perform authorization,
provisioning, policy enforcement or audit, and verify equivalent errors and
denials whenever a redundant step is removed.

One concrete cleanup is the runtime API's internal platform client:
`authorizedJSON` closes responses without consistently consuming the body on
no-result and error paths. Handle bounded response draining and deadlines so
existing HTTP connections can be reused reliably. Verify repeated calls reuse
connections and cancellation still works. Apply similar fixes only where the
existing code demonstrates the same problem; add no new service or proxy.

The event pipeline currently has three Kafka partitions and three processor
replicas. Keep the current worker and replica counts. Verify event delivery
and backlog recovery after the namespace move. The shared UI session store is already
present; preserve its behavior and existing replica settings.

Improve the existing performance check so required scenarios cannot silently
skip and candidate results cannot become their own baseline. Compare repeated
runs on the same disposable VM shape and representative load. Record p95/p99,
throughput, errors, CPU/memory, queue drops and backlog recovery. Use baseline
comparisons to detect regressions; record any improvement only when the
existing checks demonstrate it. A namespace change alone is not a performance
claim.

## Delivery and validation

| Stage | Reviewable result | Required evidence |
| --- | --- | --- |
| 1. Inventory and install ownership | Fixed placement, manifests, config/Secret consumers, idempotent setup | Inventory/render tests; fresh install and interrupted new-install rerun. |
| 2. Routing and security | Correct DNS/routes/TLS, removed unused permissions, bounded dependencies | Route/auth tests; Secret/RBAC denies; cross-namespace allow/deny probes; telemetry outage behavior. |
| 3. Operations and docs | Existing management flows work across namespaces; docs/skills match | Targeted-update isolation; status/doctor; backup/restore and cleanup preview; CLI goldens. |
| 4. Final proof | Working supported journeys and verified minor cleanups | Final-commit CI, Kind QA, Staging E2E, and performance comparison. |

Keep dependent changes in a reviewable PR stack; merge only when the final
head has a complete install and passes the required gates. An intermediate
manifest that moves only some consumers is not ready to merge.

The final validation record must include the tested commit and images,
environment, check results and limitations. Required journeys are login →
server init/validate/build/push/deploy → grant/session → allowed and denied MCP
calls → audit/query/UI evidence. Check Grafana access, registry pull/push,
certificate rotation, scrape/log/trace coverage, dependency recovery, and
unrelated workload generations after targeted updates. Run unit tests with an
isolated KUBECONFIG. Reuse unchanged images by their source inputs, while
running relevant manifest/security/E2E checks for every changed patchset.

## Coverage of #548

| Finding | Planned resolution |
| --- | --- |
| F1 admission | One restricted policy owner per application namespace; separate collector exception. |
| F2 credentials and authority | Exact consumers, narrow service accounts, unused access removed and denied operations tested. |
| F3 maintenance | Targeted credential/config rollouts with unrelated-generation checks. |
| F4 coupling | Explicit bounded dependencies; existing analytics reads accepted with tenant and failure checks. |
| F5 inventory | One placement catalog consumed by management and tests. |
| F6 lifecycle | Fresh-install layout and resumable setup; legacy migration scope superseded by user direction. |
| F7 resources | Preserve sizing and verify performance/recovery after the namespace move. |
| F8 data | Per-store ownership, retention and tested backup/restore. |
| F9 routing | Existing ingress/gateway adapted to namespaces; one platform certificate owner. |
| F10 operator | Keep `mcp-runtime`; verify existing webhook/Lease and cluster-scoped ownership. |
| F11 diagnostics | Accurate domain health and complete correlated telemetry. |
| F12 cleanup | Exact resource ownership, idempotent apply and retained-data cleanup preview. |

The inventory, narrower operator access, collector isolation, consumer Secrets,
and targeted-rollout prerequisites were merged through #569. Build on those
changes. This plan was checked against #548, `docs/architecture.md`, component
inventory, setup/update, manifests, API handlers and the existing performance
script. No live environment was changed and no runtime result is claimed by
this planning document.
