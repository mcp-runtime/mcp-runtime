# Namespace ownership refactor: delivery plan

Parent: [#548](https://github.com/mcp-runtime/mcp-runtime/issues/548).
Prepared 2026-10-01 against `main` at `14ebdb29`.

Status: proposed delivery plan. Namespace migration is **not ready to ship**.
The implementation PRs below are drafts with known gaps, not completed findings.
This document defines the work and evidence required to close F1–F12.

## Current work

| Work | PR / branch | State |
| --- | --- | --- |
| Operator Secret scope, related to #540 and F2 | [#550](https://github.com/mcp-runtime/mcp-runtime/pull/550), `security/operator_secret_rbac`, `31148165` | Draft; generator, provisioning, and authorization checks pending. |
| Collector admission boundary, F1 | [#551](https://github.com/mcp-runtime/mcp-runtime/pull/551), `refactor/namespace-admission-policy`, `050f5ef5` | Draft; bootstrap, cutover, admission, and log-delivery checks pending. |
| Credential worktree | `refactor/namespace-credential-boundaries` | Reserved locally; no implementation commit or PR yet. |
| Remaining findings | PR sequence below | Planned; no completion is implied by this plan. |

The two implementation branches were based on `76553f38`. Refresh them against
current main and the relevant prerequisites before marking them ready. Main now
includes the post-setup operational smoke gate from #545; namespace changes must
preserve that check as well as standalone diagnostics.

## Ownership and compatibility decisions

The following is the target ownership map. P01 turns it into a complete,
machine-readable inventory before the main platform/observability migration.

| Domain | Current placement | Proposed placement and responsibility |
| --- | --- | --- |
| Controller/admission | `mcp-runtime` | Keep this namespace for the first migration. Own the operator, webhooks, coordination, and an inventory of cluster-scoped objects. `mcp-system` is a separate, optional later rename. |
| Product management/identity | `mcp-sentinel` | `mcp-platform`: platform API, runtime API, UI, Postgres identity/platform state, and explicitly inventoried gateway/auth consumers. Runtime API co-location requires the P03 privilege review; a namespace alone provides no isolation from UI. |
| Telemetry | `mcp-sentinel` | `mcp-observability`: analytics API, ingest, processor, Kafka, ClickHouse, Prometheus, Grafana, OTel, Loki, and Tempo. Own telemetry schemas, retention, and recovery. |
| Node log collection | `mcp-sentinel` | Proposed `mcp-log-collector`, as in #551, exclusively for the host-access collector. Application and telemetry namespaces must not inherit its hostPath admission exception. |
| Tenant runtime | Existing managed namespaces | Preserve namespace names, team ownership, MCPServer/grant/session identities, and per-server gateways. No tenant resource relocation. |
| Image distribution | `registry` or external | Keep placement and storage ownership; explicitly scope publication, pull credentials, and helper workloads. |
| Ingress | `traefik` or external | Keep external installations where they are. Own shared routing/watch scope; preserve existing public URLs and auth gates. |
| Optional identity and infrastructure | Installation-dependent | Inventory bundled mcp-auth, Keycloak, cert-manager, bootstrap jobs, DNS/CNI/CSI and external stores/providers individually. Preserve externally managed resources; choose bundled auth placement from its actual consumers and lifecycle. |

Compatibility rules:

- Preserve stable component IDs and existing Sentinel CLI/config names. Resolve
  placement through the layout inventory rather than changing command names.
- Preserve public API/CRD shapes, hostnames, session behavior, certificate trust,
  tenant authorization, and existing data. Publish version compatibility for
  CLI, APIs, operator, gateway and stored layout metadata.
- Keep legacy installations on the legacy layout during ordinary updates.
  Migration must be an explicit supported CLI/UI operation with a reviewable
  plan. Command/API syntax is designed in P11; this document does not introduce
  working flags or endpoints.
- Proposed layout state: one non-secret record owned by setup/update in the
  stable operator namespace, recording schema/layout version, capability
  enablement, component placement, ownership, and migration checkpoints. Missing
  metadata requires legacy discovery and validation, not silent adoption of a
  new layout. Reject ambiguous/unsupported layouts before writes.
- Keep controller/API/public interfaces stable while removing internal duplicate
  inventories. No new service mesh, deployment product, or namespace per binary
  is required.

## PR sequence and dependencies

Each row is a focused PR in its own worktree. P00 is this plan. Existing drafts
are reused for their rows; create the other PRs once they contain reviewable
implementation. PR numbers are assigned only after creation.

Create future worktrees from current `origin/main` with one branch per row;
use the existing credential worktree for P02 after refreshing its base. When a
PR requires unmerged code, name its base PR explicitly and retarget/revalidate
after that dependency merges. Keep unrelated rows out of each other's diffs.

| ID | Scope and findings | Depends on | Completion evidence |
| --- | --- | --- | --- |
| P01 | Canonical component, owner, namespace, capability and dependency inventory; versioned legacy/target resolver (F5, F12) | P00 | Setup/update, CLI and API status/logs/restart/port-forward consume one source; legacy, target, partial and external-install fixtures resolve consistently; conflicting ownership is rejected. |
| P02 | Split shared credentials/config by owner and consumer while retaining current placement (F2) | P01 | Preserve existing values on upgrade; migrate only required keys; tested synchronization and rotation; unrelated consumers cannot read the resulting Secret objects; legacy fallback has an explicit removal gate. |
| P03 | Narrow service/helper/tenant RBAC and namespace authority, incorporating #550 (F2) | P01, P02; #550 can land independently if its own gates pass | Allowed/denied capability matrix includes direct Secret reads and indirect access via workloads, service accounts, impersonation, bind and namespace labels. Generator output stays narrow. |
| P04 | Credential-consumer rollout and maintenance plans (F3) | P01, P02 | A telemetry-only update leaves unrelated platform pod-template generations unchanged; dry-run, execution, retry and rollback target the same affected consumers. |
| P05 | Platform-to-telemetry query and failure contracts (F4) | P01 | Bounded authenticated queries, preserved tenant scope and explicit degraded responses; telemetry outages do not block unrelated login/deploy/session operations; audit-loss handling is tested and visible. |
| P06 | Data ownership, backup, restore, retention and rollback procedures (F8) | P01, P02 | Store-specific backup/restore and consistency checks for Postgres, Kafka, ClickHouse, Grafana, Loki, Tempo and registry data; tested recovery in a disposable environment. |
| P07 | Cross-domain service discovery, routing, certificates and network policies (F9) | P01, P03, P05 | Public auth/paths preserved; internal endpoints, DNS, API-server access, scrape/ingest flows, registry pulls and certificate renewal tested for legacy and target layouts. |
| P08 | Application admission ownership and collector isolation; finish #551 (F1) | P01 and the collector-relevant parts of P03/P07 | Fresh setup succeeds; migration/resume/rollback preserves collection; application namespaces reject privileged/hostPath pods; supported application/collector workloads pass their intended policies. |
| P09 | Domain capacity and availability profiles (F7) | P01, P06 | Resource/rollout/restore headroom is explicit for each supported deployment shape; drain and rollout checks pass; UI/gateway session locality is respected. |
| P10 | Capability-aware diagnostics, telemetry coverage and safe cleanup (F11, F12) | P01, P06, P07; include P08 placement when enabled | Distinguish disabled, missing, degraded and failed components; verify logs/metrics/traces; dry-run cleanup excludes retained data, tenant namespaces and external/shared resources by default. |
| P11 | Explicit installation capabilities and orchestrated layout migration (F6, F12) | P01–P10 and all relevant branch gates | Fresh install, legacy upgrade, retry/resume, rollback, restore and supported CLI/UI journeys pass in Kind/staging; migration is idempotent and blocks on failed prerequisites. |
| P12 | Optional operator namespace rename (F10) | Separate decision after P11 | One leader-election group, correct webhook Service/SAN/CA references and cluster-scoped ownership throughout cutover; admission/reconciliation remain available. |

F10 does not block the initial namespace split: keeping `mcp-runtime` retains its
existing isolation and avoids an unnecessary webhook/leader migration. P12 is
explicitly deferred unless the naming change is still useful after P11.

P01 is the next implementation PR. P02/P03/P04 can be reviewed while P05/P06 are
developed, but their merge dependencies remain as listed. Do not treat the early
existence of #551 as evidence that collector lifecycle prerequisites are done.

## Implementation contracts

### Inventory and management (P01, P04, P10)

Start from `pkg/sentinel/components.go`, `internal/cli/sentinel/manager.go`,
`internal/cli/platformstatus/workloads.go` and
`internal/platformrelease/catalog.go`. A record must include stable component
ID, owner domain, workload/service names, current placement, installation mode,
required/optional dependencies, credential/config consumers, storage, and
declarative owner. Include helpers, jobs and cluster-scoped resources rather
than only Deployments. Render the human-readable inventory from that source.

Use it in setup/update, runtime API/UI management, diagnostics, port-forward,
backup/restore and cleanup. An absent namespace is not evidence that an enabled
capability is intentionally disabled. Explicitly discovered external services
remain external; their owner is not implicitly changed by setup.

### Credentials, privilege and admission (P02, P03, P08)

Inventory keys currently rendered by
`internal/cli/setup/platform/analytics.go` and every consumer in `k8s/` before
splitting `mcp-sentinel-secrets`. Separate platform signing/database/bootstrap,
API auth, UI, ingest and Grafana credentials as their consumers require. Shared
auth material must have one owner, defined synchronization and a rotation order.
Do not clone the entire legacy Secret into multiple namespaces.

Secret splitting alone is insufficient when an identity can create workloads
that mount other Secrets or select a more privileged ServiceAccount. Review
`k8s/08-runtime-api-rbac.yaml`, `k8s/08-platform-api-rbac.yaml`, registry-push
helpers, user-key roles and namespace-label writers together. Protect all
management namespaces, including the collector, from tenant provisioning.

Create destination namespaces through one authoritative policy owner before
applying namespaced RBAC or workloads. For collector migration, retain source
RBAC and admission needed to restart the old collector until the replacement
is healthy. Define positions/cursor handling, overlap deduplication and the
rollback point before removing source resources or tightening source admission.

### Cross-domain APIs and failure behavior (P05, P07)

For each API/event/database/Secret dependency, document caller identity,
authorization, endpoint discovery, compatibility, timeout/retry budget,
readiness impact and outage behavior. Review runtime dashboard/event reads in
`services/runtime-api/internal/runtimeapi/components.go` and `server_events.go`.
Use either a bounded analytics API contract or an explicitly transitional direct
read contract; choose based on scope and failure behavior, not namespace naming.

Optional telemetry query failures must be represented as degraded data rather
than fabricated zero values or unnecessary loss of core management. Required
identity, authorization and admission failures remain fail-closed. Audit
delivery needs explicit buffering, retry, loss detection and retention behavior;
query degradation is not permission to silently discard security audit events.

Preserve public URLs/admin gates. Keep one certificate owner per endpoint,
including renewal and restore; validate cross-namespace Secret copies if any
are necessary. Network-policy tests must exercise both allowed and denied flows
on a CNI that enforces those policies.

### Stateful migration and recovery (P06, P11)

Use a persisted, versioned migration journal with these proposed phases:

1. **Preflight:** discover the installed layout and capabilities; verify version
   compatibility, ownership, privileges, data volumes, capacity, CNI, trust
   material, destination names and a restorable backup. Produce a resource and
   dependency diff without modifying the cluster.
2. **Prepare:** create destination namespaces/policies and narrowly selected
   credentials; validate destination access and dependencies. Preserve source
   resources and trust material.
3. **Transfer:** execute the store-specific procedure with checkpoints, writer
   fencing/quiescence where necessary, consistency checks and explicit downtime
   expectations. PVCs cannot simply be renamed into another namespace.
4. **Cut over:** switch routes/discovery in dependency order and run health,
   auth, tenant-boundary, audit and data checks. Record which dataset became
   writable in the destination and when.
5. **Verify:** exercise the supported login → publish/deploy → grant/session →
   allowed/denied MCP call → audit/query/UI journey and dependency-failure cases.
6. **Finalize:** commit the layout version only after required checks pass;
   remove source workloads through an explicit ownership inventory. Retain
   backups/source volumes according to the recovery policy. Never delete a
   namespace merely because its name matches a broad prefix.

Every phase must support retry without duplicate work and reject concurrent
migrations using a single coordination owner. Interrupted migration remains
visible as incomplete; normal setup/update must not silently advance it.

Before destination writes, rollback can return routing to verified source
services. After destination writes, rollback must fence writers and reconcile
or restore state using the store's recovery procedure. Repointing traffic to a
stale source or deleting destination namespaces is not a rollback strategy.
Specify backup identifiers, checksums/consistency markers, retention and
operator-visible recovery instructions; keep credential values out of the
journal and logs.

P06 must define recovery-point and recovery-time objectives per dataset and
measure restore drills against them before P11 can migrate that dataset.

## Existing draft merge gates

### #550 — operator Secret access

- The annotation in `internal/operator/controller.go` still grants broad Secret
  verbs. Update the generator source and check regenerated RBAC so regeneration
  cannot undo the manifest restriction.
- Test migration ordering: install scoped access before removing legacy access;
  cover existing empty managed namespaces, team/catalog provisioning and direct
  manifests. Verify all added assets are available in packaged CLI installs.
- Audit all named Secret reads and protected namespaces. Prove access to the CA
  private key and unrelated Secrets is denied, including indirect workload
  access, while reconciliation, TLS and registry pulls still succeed.
- Current evidence: the default Kustomize bundle renders with the scoped binding
  in `mcp-servers`. This is not an authorization or reconciliation test.

### #551 — collector namespace

- The ingress bundle retains `traefik-watch` resources in `mcp-sentinel` after
  removing that bundle's Namespace declaration. Verify and fix namespace
  creation order on a clean install, including analytics-disabled paths.
- The current patch changes the existing collector ClusterRoleBinding and
  source Pod Security labels before replacement health is known. Complete the
  cutover/resume/rollback design before relying on its late resource cleanup.
- Verify all application/helper/optional workloads satisfy restricted admission,
  reserve the collector namespace, and include it in lifecycle inventories.
- Prove log delivery and cursor/duplication behavior during migration and
  rollback. A ready DaemonSet alone does not prove end-to-end collection.
- Current evidence: YAML parsing and whitespace checks passed in the original
  implementation; a static contract test exists but has not run.

These are source-review follow-ups, not results from a live cluster audit. Neither
draft should automatically close #548 or claim its full security acceptance gate.

## Related issues and scope control

The parent issue's related-work list is not a blanket requirement to finish
every older ticket before any preparatory refactor can merge.

| Existing work | Relationship to this plan |
| --- | --- |
| #268 oversized-module refactors | Refactor touched code where necessary; wholesale module cleanup remains independent. |
| #257 shared sessions | Required before increasing UI/gateway replicas; retain current supported scaling until resolved. |
| #71, #72 durability/resource-pressure recovery | P06/P09 must exercise affected recovery paths; unresolved failure of a moved store blocks its migration. |
| #500 Grafana credentials | P02/P06 must preserve persisted credential consistency and prove recovery. |
| #496, #497, #498, #543 observability coverage | P10 must demonstrate coverage/correlation and correct links for each moved component; namespace movement alone does not close these tickets. |
| #531 registry authentication | Preserve publish/pull authorization through P03/P07; require demonstrated internal-registry isolation before making that security claim. |
| #535, #538, #540 PKI and CA access | Keep dedicated security work linked; P03/P07 must preserve issuer/trust/approval behavior and verify CA-key restrictions. #550 is only the #540 candidate. |
| #546 regression tracking | Record migration/admission/RBAC/failure regressions and CI coverage under its existing workflow. |

## Validation and release gates

Each implementation PR records actual checks and open gaps, then passes the
checks applicable to its surface before readiness. Required aggregate gates:

- Render/contract checks: unique declarative policy owners, valid namespace and
  Secret/RBAC/DNS references, generator consistency, packaged assets, stable
  component IDs, legacy/target/transition layout resolution, safe cleanup plans.
- Root and changed service-module Go build, tests, formatting and vet; contract
  and fake-client tests cannot substitute for Kubernetes RBAC/admission tests.
- Disposable Kind/staging: fresh install; legacy upgrade; stopped/resumed
  migration at every checkpoint; rollback before and after writes; independent
  restore; cleanup with retained data; optional/external component combinations.
- Security: allowed/denied RBAC, indirect pod/ServiceAccount access, protected
  labels/namespaces, Pod Security admission, CNI-enforced network boundaries,
  issuer approval, certificate renewal and credential rotation.
- Product and failure paths: supported CLI/UI journey, scoped grants/sessions,
  telemetry outages, audit durability, scrape/log/trace coverage, external
  Traefik and custom cluster-DNS handling, capacity pressure and drain/rollout.
- CI and applicable Staging E2E green on the final integration commit; record
  measured privilege/Secret-consumer scope, unrelated rollout counts and restore
  outcomes. No performance or availability claims without measurements.

Use an empty temporary kubeconfig for unit tests and commit hooks, per
`AGENTS.md`. Use explicit disposable cluster kubeconfigs for integration/E2E.
Do not use ambient production credentials during validation.

Plan preparation ran read-only source review and a local Kustomize render. Go
and gofmt were unavailable; no Go suite, live cluster validation, or security
scanner result is claimed. Documentation-only P00 needs link/format checks;
it does not require a running cluster or change an installation.
