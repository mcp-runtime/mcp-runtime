# Changelog

Notable changes are curated here for users and operators. The format follows
[Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/); versions use
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Earlier release notes remain available in [GitHub Releases](https://github.com/mcp-runtime/mcp-runtime/releases).

## [Unreleased]

### Changed

- Pull-request QA E2E runs on the disposable VM. Each component keeps one local Docker image, tagged `latest` and labeled with the checkout content hash. The next run reuses that image when the hash matches and replaces it when the hash changes. GitHub Container Registry is not used for this job, and `timing.txt` records elapsed seconds so later runs can be compared.
- QA E2E and Staging E2E fail a clean install when a workload, owner Secret, shared ConfigMap, or in-cluster service URL is still aimed at the old combined namespace.
- A clean `mcp-runtime setup` places the operator in `mcp-runtime`, control-plane services (platform-api, runtime-api, UI, gateway, Postgres) in `mcp-platform`, and the event pipeline plus telemetry in `mcp-observability`. Promtail stays in `mcp-log-collector`. MCP servers stay in `mcp-servers`, `mcp-servers-org`, `mcp-servers-public`, or `mcp-team-{slug}`. An existing `mcp-sentinel` install is wiped and reinstalled. Contributor E2E, smoke, and demo deploy scripts read owner Secrets in these namespaces. The UI Deployment runs two replicas with Postgres-backed sessions (`UI_SESSION_STORE=postgres` on `mcp-ui-credentials`).
- QA E2E now reclaims preinstalled runner tools only when less than 60 GiB of disk space is free, reports remaining space after the run, and skips installing tools already present or optional on the runner. Documentation-only PRs skip the Kind job; changelog edits no longer force all scenarios. The selected `multitenancy` scenario checks cross-team API and registry isolation, and targeted HTTP flows run once per job ([#569](https://github.com/mcp-runtime/mcp-runtime/pull/569)).
- Setup rolls workloads when their consumed Secret or ConfigMap values change, replacing blanket Sentinel deployment restarts ([#548](https://github.com/mcp-runtime/mcp-runtime/issues/548)).

- Setup gives API, UI, ingest, Grafana and Postgres workloads consumer-specific credential Secrets in the owner's namespace. A rerun keeps values already stored on those Secrets ([#548](https://github.com/mcp-runtime/mcp-runtime/issues/548)).
### Security

- Operator tenant Secret permissions are scoped to MCPServer namespaces, and Secret reads bypass the controller cache. Runtime-managed namespaces receive a scoped binding automatically; setup backfills existing managed namespaces. The configured TLS namespace grants named read/update access only to the public adapter trust bundle ([#540](https://github.com/mcp-runtime/mcp-runtime/issues/540)).
### Security

- Platform application workloads now use restricted Pod Security admission; node log collection runs in a dedicated namespace with its hostPath exception ([#548](https://github.com/mcp-runtime/mcp-runtime/issues/548)).
### Security

- The internal registry NetworkPolicy no longer admits tenant workload namespaces (`mcp-servers*` and platform-managed team namespaces), closing the unauthenticated in-cluster registry read/write path from tenant pods on the base manifests. Node pulls and platform publish paths are unchanged. Registry-native authentication and the k3s compatibility overlay's pod-CIDR allowance remain open follow-ups ([#531](https://github.com/mcp-runtime/mcp-runtime/issues/531)).
### Fixed

- Setup replaces a gateway trace endpoint that still names the removed combined namespace with `otel-collector.mcp-observability.svc`. A collector address outside that namespace is kept. Gateway sidecars and server defaulting do the same for an ingest URL aimed at that namespace.
- Docs now say a saved login does not see a team membership added after that login, the example server tool list includes `whoami`, and `MCP_ADAPTER_CERTIFICATES` only turns on ingress verification of an adapter certificate. Enrollment still follows the workload issuer, and an allow-list `tools/call` stays `missing_identity` until verification is on.
- Setup builds `POSTGRES_DSN` for `mcp-postgres.mcp-platform.svc` when that key is absent from `mcp-platform-api-credentials`. A present empty value is still rejected, so a wiped database URL is not replaced from another Secret.
- UI replicas retry session-table creation when Postgres reports a concurrent catalog collision, so a two-replica start no longer crash-loops before `ui_sessions` exists.
- The public platform certificate stays a single `mcp-platform-tls` Secret in `mcp-platform`. Grafana and analytics-api Ingresses are created beside those Services in `mcp-observability` and do not open a second ACME order. The platform gateway Traefik watch list follows those namespaces, and registry host fallback reads `mcp-shared-config` from `mcp-platform`. The shared ConfigMap and PriorityClasses use the `mcp-shared-` prefix (`mcp-shared-config`, `mcp-shared-data`, `mcp-shared-services`) because those objects are used from more than one namespace.
- `cluster doctor` now retries transient connection errors from its fresh in-cluster registry probe before reporting registry reachability as failed; persistent failures still fail the check ([#569](https://github.com/mcp-runtime/mcp-runtime/pull/569)).
- Staging E2E now accepts the expected denied CertificateRequest authorization result during adapter enrollment. Registry doctor and in-cluster image push failures include helper-container diagnostics to make failed probes actionable ([#569](https://github.com/mcp-runtime/mcp-runtime/pull/569)).
- Promtail now parses CRI log envelopes, discovers only node-local pods across all namespaces, labels streams with `namespace`, `pod`, `container`, `app`, and `node`, redacts bearer tokens and secret-looking JSON fields, and the platform-namespace fallback job labels streams from the log path. Intended to restore Loki coverage for MCP server and team namespaces; root cause of the production gap is not yet confirmed on a live cluster ([#496](https://github.com/mcp-runtime/mcp-runtime/issues/496)).
- Sentinel recovery hardening for node resource pressure and eviction: stateful stores (Kafka, ClickHouse, Postgres) run under a new `mcp-sentinel-data` PriorityClass and request-serving services under `mcp-sentinel-services` (evicted last/after data stores; placement unchanged), StatefulSet PVCs are retained on delete or scale-down, ClickHouse gains resource requests/limits and probes, Kafka and ClickHouse gain startup probes and longer termination grace, and ingest/processor gain startup probes and use `imagePullPolicy: IfNotPresent` for pinned images so restarts survive transient registry outages ([#71](https://github.com/mcp-runtime/mcp-runtime/issues/71), [#72](https://github.com/mcp-runtime/mcp-runtime/issues/72), [#548](https://github.com/mcp-runtime/mcp-runtime/issues/548)). Cluster-scoped PriorityClasses are applied by `setup`; upgrades need no migration.
- `mcp-runtime setup` prunes Failed/Evicted and orphaned Completed pods in `mcp-platform` and `mcp-observability` before re-applying the stack, and `cluster doctor` reports them as `sentinel stale pods`.

### Added

- Platform regression coverage index (`docs/contributor/regression-index.yaml`) mapping incidents to regression checks, with a CI check that fails on unmapped incidents or expired waivers ([#546](https://github.com/mcp-runtime/mcp-runtime/issues/546)).
- The UI Deployment ships two replicas with `UI_SESSION_STORE=postgres`. Session rows live in Postgres, encrypted with `UI_SESSION_ENCRYPTION_KEY` from `mcp-ui-credentials`, so login and admin-check work across pods. The gateway does not store the session cookie ([#257](https://github.com/mcp-runtime/mcp-runtime/issues/257)).
- `mcp-runtime sentinel grafana check` (read-only) detects when Grafana's persisted admin account rejects the password in `mcp-grafana-credentials`, distinguishing Grafana login from the platform ingress gate. `sentinel grafana reset-admin-password --yes` recovers deliberately: it runs only on detected drift, backs up the Grafana database first, passes the password over stdin inside the pod, and verifies access. Related #500.
- Gateway and Sentinel service logs include `trace_id`/`span_id` for request and failure lines. Auth rejections (401/403), policy denials, and upstream or 5xx failures mark their spans as errors with a safe reason, and OTLP export failures are logged and counted in `mcp_otel_internal_errors_total`. Probe requests (`/health`, `/ready`, `/metrics`) are no longer traced ([#498](https://github.com/mcp-runtime/mcp-runtime/issues/498)).
- `cluster doctor` reports `sentinel Grafana provisioning` drift when the `mcp-server` dashboard uid, the `prometheus` datasource uid, or the Grafana provisioning mounts are missing, instead of surfacing only as "Dashboard not found" / "Data source not found" from server-card Grafana links ([#543](https://github.com/mcp-runtime/mcp-runtime/issues/543)).
- Setup ends with a short operational smoke gate (nodes Ready, Bound PVCs, no Pending blockers, Postgres, platform-api `/health`+`/ready`, Sentinel rollout health, and an authenticated API probe). Failures fail setup; use `mcp-runtime cluster diagnostics` for deeper follow-up.
- One-command macOS, Linux, and Windows CLI installers from the Getting Started and Quickstart pages; installers detect the platform and install the matching release binary to a user-local directory. The Windows installer also adds its directory to the user's `PATH`.

### Security

- Adapter certificates are gated on a workload-issuer approval policy: outside `--test-mode`, setup refuses `MCP_ADAPTER_CERTIFICATES=true` unless cert-manager approver-policy is installed or `MCP_WORKLOAD_ISSUER_APPROVAL_ACK=true` acknowledges another approver. The runtime API now rejects issued certificates whose SPIFFE URI, key, usage, or lifetime differ from the submitted CSR, and its ClusterRole no longer grants `list`/`watch` on `CertificateRequests`. Operators enabling adapter certificates on a cluster without approver-policy must set the acknowledgement; adapter issuance is off by default ([#538](https://github.com/mcp-runtime/mcp-runtime/issues/538)).
- Setup validates the bundled `mcp-runtime-ca` workload CA before use (key/cert match, CA constraints, validity). Production setup now fails on a missing, invalid, expired, or under-180-day CA instead of silently generating or accepting it; test mode still generates a missing CA and warns near expiry. Rotation and backup guidance is in `docs/cli.md`. Splitting the registry and workload roots is not yet done ([#535](https://github.com/mcp-runtime/mcp-runtime/issues/535)). Migration: production installs using `--mtls-cluster-issuer mcp-runtime-ca` must have the CA Secret present beforehand.
- Prometheus now scrapes the OTel collector, Loki, Tempo, Promtail, itself, and opt-in annotated Services in `mcp-platform` and `mcp-observability`, and ships coverage rules (required target absent or down, intentionally uninstrumented workloads), OAuth-failure, gateway analytics-drop, and collector export-failure alerts. Gateways export a bounded `mcp_gateway_oauth_outcomes_total{outcome}` counter, and Grafana has a new "Scrape Coverage" dashboard ([#497](https://github.com/mcp-runtime/mcp-runtime/issues/497)). The bundled `mcp-auth-server` image is listed as not instrumented; its OAuth failures are visible through the gateway counter.

## [0.4.1] - 2026-09-29

### Fixed

- Setup enables the Kind/port-forward OAuth issuer backchannel (`OAUTH_INTERNAL_ISSUER_URL`) only under `--test-mode` (or an explicit env). Production setups leave it unset and clear leftovers so gateways keep using public HTTPS JWKS ([#528](https://github.com/mcp-runtime/mcp-runtime/issues/528), [#529](https://github.com/mcp-runtime/mcp-runtime/pull/529)).

### Changed

- Agent skills are consolidated into seven mode-based skills, with deterministic live checks under `hack/cluster-ops/` and ship guidance via CI + Staging E2E instead of a release-orchestrator skill ([#526](https://github.com/mcp-runtime/mcp-runtime/issues/526), [#527](https://github.com/mcp-runtime/mcp-runtime/pull/527)).

## [0.4.0] - 2026-09-29

### Added

- Role-aware agent management in the dashboard, including team filtering ([#507](https://github.com/mcp-runtime/mcp-runtime/pull/507)).
- CLI support for adding an existing platform user to a team ([#508](https://github.com/mcp-runtime/mcp-runtime/pull/508)).

### Changed

- Adapter identity comes from issued SPIFFE client certificates. OAuth is optional for certificate-authenticated MCP servers ([#494](https://github.com/mcp-runtime/mcp-runtime/pull/494)).
- Gateway observability is enabled by default, and an unset gateway policy defaults to observe ([#511](https://github.com/mcp-runtime/mcp-runtime/pull/511)).

### Removed

- The obsolete `adapter stdio` command and its cache/configuration are removed. Migrate existing adapter commands to `adapter proxy`; use `adapter enroll` for issued client certificates ([#523](https://github.com/mcp-runtime/mcp-runtime/pull/523)).

### Fixed

- Enrollment preserves HTTPS server roots while configuring mTLS client identity ([#523](https://github.com/mcp-runtime/mcp-runtime/pull/523)).
- The operator waits for TLS Secrets before creating Traefik mTLS routes ([#513](https://github.com/mcp-runtime/mcp-runtime/pull/513)).
- Public registry certificate issuance and renewals allow only Traefik to reach ACME HTTP-01 solver pods through the registry default-deny policy ([#523](https://github.com/mcp-runtime/mcp-runtime/pull/523)).
- Setup pulls republished `latest` service images instead of reusing stale local images ([#509](https://github.com/mcp-runtime/mcp-runtime/pull/509)).
- Standalone MCP servers no longer display gateway-only metrics ([#493](https://github.com/mcp-runtime/mcp-runtime/pull/493)).
- Staging validation waits for newly enrolled session policy and recovers from k3s installer endpoint failures using a pinned official fallback ([#523](https://github.com/mcp-runtime/mcp-runtime/pull/523)).

### Security

- CLI login prompts securely for passwords ([#495](https://github.com/mcp-runtime/mcp-runtime/pull/495)).

## [0.3.2] - 2026-09-27

### Added

- Targeted platform updates through `mcp-runtime update`, including embedded CRD updates and `--build` to build/push missing component images before rollout ([#491](https://github.com/mcp-runtime/mcp-runtime/pull/491)).

[Unreleased]: https://github.com/mcp-runtime/mcp-runtime/compare/v0.4.1...HEAD
[0.4.1]: https://github.com/mcp-runtime/mcp-runtime/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/mcp-runtime/mcp-runtime/compare/v0.3.2...v0.4.0
[0.3.2]: https://github.com/mcp-runtime/mcp-runtime/compare/v0.3.0...v0.3.2
