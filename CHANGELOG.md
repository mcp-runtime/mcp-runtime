# Changelog

Notable changes are curated here for users and operators. The format follows
[Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/); versions use
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Earlier release notes remain available in [GitHub Releases](https://github.com/mcp-runtime/mcp-runtime/releases).

## [Unreleased]

### Changed

- The operator manages per-server Traefik egress policies for actual serving ports and reports TraefikEgressReady conditions. Fresh setup is required for the v0.7.0 reference deployment; the static policy no longer grants fixed ports into server namespaces ([#630](https://github.com/mcp-runtime/mcp-runtime/pull/630)).

- Server cards show an OAuth badge only when the server reports OAuth support, and omit the badge when authentication support is unknown. Header credential labels remain available.

- Organization and Platform pages appear directly in the main console sidebar and compact navigation, eliminating the second administration sidebar. Navigation filtering and active states include each section.

### Fixed

- First-install setup applies the requested registry PVC size before creation, checks Docker buildx before image builds, rejects partial password-admin credentials while preserving saved pairs, and propagates external TLS proxy UI settings. Rollout and registry-auth restoration tolerate bounded transient Kubernetes API failures; image uploads reject unsafe redirects with an HTTPS-origin diagnostic, and tenant setup prints membership steps ([#666](https://github.com/mcp-runtime/mcp-runtime/issues/666)).

- The package-docs image uses the root module's Go toolchain version in its build and runtime stages, preventing startup failures after a Go requirement bump. Module requirement changes now run the package-docs PR smoke test.

- Setup reports the cert-manager version it actually installs, and repair guidance follows the installed release rather than a retired manifest; drift checks keep the pin, logs, and docs aligned ([#628](https://github.com/mcp-runtime/mcp-runtime/pull/628)).

- Promtail uses the Kubernetes node name for pod discovery and streams labeled logs across platform and tenant namespaces instead of relying on a static-path fallback ([#629](https://github.com/mcp-runtime/mcp-runtime/pull/629)).

- Ingest readiness serves a bounded, cached Kafka check with DNS/connect timing and failure metrics, and resolves cluster FQDNs directly using ndots:3 ([#626](https://github.com/mcp-runtime/mcp-runtime/pull/626)).

- Grafana loads every bundled dashboard with stable datasource UIDs; cluster doctor detects persisted admin credential drift without revealing or resetting credentials ([#631](https://github.com/mcp-runtime/mcp-runtime/pull/631)).

- `update --build` publishes bundled-registry images through an in-cluster helper using the selected kubeconfig, context and registry-auth client, avoiding host DNS failures and ambient-cluster credential selection ([#679](https://github.com/mcp-runtime/mcp-runtime/issues/679)).

### Security

- Production-shaped TLS setup enables scoped backend registry authentication and narrows registry access to authorized ingress, publication helpers, and probes. Fresh setup is required; lab/test HTTP installs retain explicit warnings about anonymous backends ([#632](https://github.com/mcp-runtime/mcp-runtime/pull/632)).

- Platform images and modules build with Go 1.26.9 and `golang.org/x/net` v0.60.0. Those releases fix HTTP/2 CPU exhaustion from repeated window updates (CVE-2026-78669), excessive CPU from large Range headers (CVE-2026-78667), and memory exhaustion from malformed TLS ECH references (CVE-2026-97031).

## [0.6.6] - 2026-10-09

### Changed

- Administration usage analytics shows the server, tool, and RPC method on recent policy decisions, and refreshes servers, tools, and decisions every 15 seconds ([#671](https://github.com/mcp-runtime/mcp-runtime/issues/671), [#672](https://github.com/mcp-runtime/mcp-runtime/pull/672)).
- Client connect snippets for header-auth servers include the configured credential header names with empty values. The console and `server connect-config` copy those names from the MCPServer spec. Credential values are not stored or returned ([#669](https://github.com/mcp-runtime/mcp-runtime/issues/669), [#670](https://github.com/mcp-runtime/mcp-runtime/pull/670)).

## [0.6.5] - 2026-10-09

### Added

- Setup can allow team MCP servers to reach configured destination networks on TCP 443. `--pod-egress-cidrs` lists those destinations and `--pod-egress-except-cidrs` keeps the cluster pod and service ranges out of a private destination. An empty list adds nothing, and `0.0.0.0/0` is rejected. Runtime API writes the allow into each team default-deny NetworkPolicy ([#666](https://github.com/mcp-runtime/mcp-runtime/issues/666), [#667](https://github.com/mcp-runtime/mcp-runtime/pull/667)).

### Changed

- Install docs cover registry PVC size, Docker buildx, an external TLS terminator in front of HTTP Traefik, password-admin seeding, the HTTPS API base URL, registry upload body size, and tenant publish membership ([#666](https://github.com/mcp-runtime/mcp-runtime/issues/666)).
- Customers can configure dashboard, registry catalog entry, documentation, and admin Grafana paths alongside existing hostname overrides, allowing a single public hostname and enterprise certificate. Bundled MCP Auth ingress and discovery paths follow its configured issuer URL; setup rejects conflicting paths before cluster writes ([#658](https://github.com/mcp-runtime/mcp-runtime/issues/658), [#663](https://github.com/mcp-runtime/mcp-runtime/pull/663)).
- Servers can opt in to upstream-owned header authentication with `auth.mode: header` and a customer-defined list of credential header names. The gateway forwards those headers, enforces allow-list tool rules with a default deny, and audits the caller as unverified. It does not accept grants, sessions, or observation mode on this path, and it does not fall back to header mode when OAuth fails. Set `auth.credentialPresence` to `all` when every configured header must be present; the default `any` accepts one or more without choosing among them ([#660](https://github.com/mcp-runtime/mcp-runtime/issues/660), [#664](https://github.com/mcp-runtime/mcp-runtime/pull/664)).
- `adapter proxy --auth-mode header` can inject customer-defined credential headers from local environment variables or files. The adapter remains optional: direct clients send those headers themselves. Header mode does not enroll a certificate or create a Runtime session. Credential values stay on the client and are pinned to the configured HTTPS server URL ([#661](https://github.com/mcp-runtime/mcp-runtime/issues/661), [#665](https://github.com/mcp-runtime/mcp-runtime/pull/665)).
- The bundled registry ingress matches `/v2` rather than `/`, so it can share a hostname with the platform. The configurable `/registry` browser entry opens the platform server catalog; Docker and Kubernetes clients continue using the registry authority's `/v2/` API. Apply the new ingress and route configuration during a fresh setup; image-only updates do not reconcile public routes ([#658](https://github.com/mcp-runtime/mcp-runtime/issues/658)).

## [0.6.4] - 2026-10-09

### Changed

- The platform console uses grouped sidebar navigation with page filtering, blue active states, and a charcoal dark theme. Account controls sit at the bottom of the desktop sidebar; compact screens retain a navigation menu and light mode remains available ([#648](https://github.com/mcp-runtime/mcp-runtime/pull/648)).

### Fixed

- The bundled Traefik NetworkPolicy permits the Kubernetes API endpoint port 6443 as well as Service port 443. This restores ingress watches and prevents UI/registry 404s on CNIs that evaluate egress after Service address translation. Existing repo-managed installs can reconcile the policy with `cluster config --force-ingress-install --ingress-manifest config/ingress/base/networkpolicy.yaml` ([#653](https://github.com/mcp-runtime/mcp-runtime/issues/653)).
- UI replicas retain bounded schema-race retries after waiting for Postgres, preventing a fresh two-replica install from exiting on concurrent session-table creation ([#651](https://github.com/mcp-runtime/mcp-runtime/issues/651)).
- Platform Dockerfiles compile service binaries for the requested image architecture. ARM64 builds previously contained AMD64 executables, causing startup failures or unintended emulation on ARM64 nodes. Rebuild affected images to correct existing builds ([#649](https://github.com/mcp-runtime/mcp-runtime/issues/649)).
- Release manifests give Runtime images a tag tied to the full source commit. `update --build` rebuilds those images from a clean checkout of that commit even when the tag exists, preventing a stale registry tag from silently supplying an older build. Same-release image tags compare as the same release version during update ([#644](https://github.com/mcp-runtime/mcp-runtime/issues/644)).
- `server deploy` reports an operator rejection on the next poll with its status message and false condition reason instead of waiting for the five-minute timeout. Pending timeouts include the last observed condition ([#642](https://github.com/mcp-runtime/mcp-runtime/issues/642)).

ARM64 installations must rebuild the affected platform images from this release.
For the hosted reference platform, follow the documented backup, fresh setup,
restore, and verification procedure in [Reference Deployment](docs/reference-deployment.md);
retain the external identity provider data and TLS state.

## [0.6.3] - 2026-10-06

### Fixed

- The operator receives a named, namespace-scoped permission to patch the bundled MCP Auth Deployment when server OAuth resources or scopes change. `cluster operator-rbac apply` repairs existing installations without changing images, Secrets, or other workloads. The hosted release workflow runs this repair before updating and checks the effective EndpointSlice, Secret, and MCP Auth permissions. `cluster doctor` now detects missing EndpointSlice informer access ([#634](https://github.com/mcp-runtime/mcp-runtime/pull/634)).
- Fresh setup attaches the platform registry pull Secret to a bundled MCP Auth Deployment that uses a private platform image. `cluster doctor` reports platform Deployments that pull from the platform registry without a pull Secret ([#627](https://github.com/mcp-runtime/mcp-runtime/pull/627)).
- `server build image` keeps an explicit team repository from tenant metadata and rejects ambiguous unscoped repositories for accounts in multiple teams. It prints the chosen repository before Docker runs, preventing an image built for one team from silently being deployed to another ([#640](https://github.com/mcp-runtime/mcp-runtime/pull/640)).
- Published servers receive the configured Analytics ingest URL when the runtime API creates their credentials, including the legacy configuration key on existing installations. An explicit server URL is preserved ([#641](https://github.com/mcp-runtime/mcp-runtime/pull/641)).

Existing installations can repair operator permissions with `cluster operator-rbac apply` from this release's source checkout. The bundled MCP Auth pull Secret is attached by a fresh setup; `mcp-runtime update` changes images only.

## [0.6.2] - 2026-10-06

### Fixed

- The hosted platform deploy workflow rolls out MCP Auth from its public image. It previously re-tagged the image into the platform registry, which the `mcp-auth-server` Deployment cannot pull without a pull secret. The rollout then hit `ImagePullBackOff` and rolled back ([#621](https://github.com/mcp-runtime/mcp-runtime/pull/621)).
- The `mcp-ingest` readiness probe allows 3s, which is longer than the 2s Kafka check behind `/ready`. Before, slow Kafka connections showed up as probe timeouts instead of a clean not-ready response ([#621](https://github.com/mcp-runtime/mcp-runtime/pull/621)).
- Setup creates the platform registry pull Secret in `mcp-log-collector`, where the promtail DaemonSet already referenced it. This removes `FailedToRetrieveImagePullSecret` warnings and lets a mirrored promtail image be pulled ([#621](https://github.com/mcp-runtime/mcp-runtime/pull/621)).

`mcp-runtime update` preserves probes and Secrets, so the ingest and promtail fixes take effect on a fresh `mcp-runtime setup`. No configuration changes are required.

## [0.6.1] - 2026-10-06

### Added

- MCP server manifests can set `auth.scopes` per resource. The operator passes those scopes to MCP Auth, while resources without an explicit list retain MCP Auth's read-only default. `mcp-runtime cluster mcp-auth-connector apply` updates the selected identity-provider connector without replacing its credential Secret ([#620](https://github.com/mcp-runtime/mcp-runtime/pull/620)).

### Changed

- The bundled authorization server is MCP Auth 0.4.4. Reference connector configuration no longer carries `mcp_scopes`; move any required write scope into each server's `auth.scopes` before updating the platform ([#620](https://github.com/mcp-runtime/mcp-runtime/pull/620)).

## [0.6.0] - 2026-10-06

This release removes the "Sentinel" name, renames `mcp-runtime sentinel` to
`mcp-runtime ops`, makes `mcp-runtime status` an authenticated platform API
check, adds opt-in native registry authentication, and fixes team account
creation, access tables, image uploads, and port-changing rollouts. Several
changes are breaking. There is no in-place upgrade from 0.5.x: back up,
run a fresh `mcp-runtime setup`, restore required data, and verify; see
**Upgrading from 0.5.x** below.

### Changed

- **Breaking:** the "Sentinel" name is removed from the product. `setup --without-sentinel` and `MCP_WITHOUT_SENTINEL` become `--without-platform-stack` and `MCP_WITHOUT_PLATFORM_STACK`, and the hidden `--without-analytics` alias is gone. The operator and CLI read only `MCP_ANALYTICS_INGEST_URL` (`MCP_SENTINEL_INGEST_URL` is no longer read). The Traefik middleware guarding `/grafana` is renamed `platform-admin-auth`. `pkg/sentinel` becomes `pkg/platformstack`, `cluster doctor` checks are named "platform …", and the UI is titled "MCP Runtime Control Plane". The operator no longer rewrites analytics or OTLP URLs that point at the removed combined namespace, and setup no longer guards against it. Apply with a fresh setup on the reference platform; setup writes the new env var and middleware name.
- `mcp-runtime status` makes one authenticated platform API readiness check with a five-second timeout and returns a failure exit code for missing or rejected credentials and unavailable APIs. Workload and server inventories remain available through `cluster status`, `ops status`, and `server list`. QA checks status using its isolated saved login after authentication.
- Release development follows a pre-customer policy: breaking changes may use a backed-up fresh setup and tested recovery of the hosted reference platform, without legacy compatibility layers or general upgrade infrastructure. Release instructions must cover required data, identity-provider backups, recovery, and verification; customer migration commitments will be defined when the first external customer is onboarded.
- Documentation sections use descriptive names and start with audience and reading-path overviews. Getting Started separates hosted, self-hosted, and contributor paths; Server and Client Guides connects publishing, access, and client setup. Deployment and Operations groups installation, the public reference, and maintenance. Development and Testing and Implementation Details use shorter grouped menus. The home page consolidates repeated guide lists; existing page URLs and heading anchors remain available. Platform Installation owns reusable setup and enterprise certificate instructions, with Cluster Provisioning handing off once infrastructure is ready.
- Deployment documentation uses distribution-neutral names: `reference-deployment.md` covers Runtime and its external identity provider, while `cluster-provisioning.md` describes the reference cluster with K3s as the worked distribution choice. Navigation, indexes, and operational links follow the new names; published URLs redirect and existing section anchors remain available. The reference guide separates configuration, installation and updates, identity-provider setup, backups and recovery, and verification, and clarifies that Keycloak DNS and backups belong to its separate VM.

### Removed

- The checked-in Go Package Reference page and its generator are removed; contributors use the hosted [pkgsite](https://docs.pkg.mcpruntime.org/github.com/mcp-runtime/mcp-runtime) or `go doc`, and the generated-file drift checks now cover only CRDs and manifests. The `Staging E2E (Remote Cluster)` workflow and `test/e2e/staging-remote.sh` are removed: Staging E2E runs only on the disposable VM through `Staging E2E (Disposable VM)`.
- **Breaking:** `mcp-runtime sentinel` is renamed to `mcp-runtime ops` (`ops status`, `ops logs`, `ops events`, `ops port-forward`, `ops restart`, `ops grafana`). The old name has no alias; update scripts and runbooks that call `mcp-runtime sentinel`.

### Fixed

- Registry image pushes can extend upload deadlines through request logging and metrics middleware, allowing uploads beyond the server's default timeout; unsupported deadline extensions are logged ([#615](https://github.com/mcp-runtime/mcp-runtime/issues/615)).
- Port-transition promotion reads candidate pods directly from the API instead of starting a Pod informer the operator RBAC cannot watch, so promotion no longer acts on a stale pod view ([#617](https://github.com/mcp-runtime/mcp-runtime/issues/617)).
- MCPServer port-changing rollouts retain the existing Service route until a Ready candidate declares the new listener, use a zero-unavailable rollout during the transition, and select only compatible pods after switching. Gateway network policies resolve the named listener per pod so retained ports stay reachable. Readiness requires current Deployment replicas and ready EndpointSlices on the requested port ([#533](https://github.com/mcp-runtime/mcp-runtime/issues/533)). The bundled Traefik egress policy still permits only its configured backend ports; allow a custom listener there before rolling it out ([#618](https://github.com/mcp-runtime/mcp-runtime/issues/618)).
- Access control tables use consistent column widths, wrap long subject identifiers within their cells, and keep status actions usable without squeezing grant and server names ([#606](https://github.com/mcp-runtime/mcp-runtime/issues/606)).
- Team account creation preserves validation and conflict messages, creates accounts and memberships atomically, and offers an existing-user membership form without changing their password. Duplicate emails no longer expose database errors; temporary passwords require at least eight characters ([#605](https://github.com/mcp-runtime/mcp-runtime/issues/605)).
- `mcp-runtime cluster doctor` reports the underlying kubectl/API error and stops dependent checks when the cluster cannot be queried ([#591](https://github.com/mcp-runtime/mcp-runtime/issues/591)).
- Gateway-enabled OAuth apps receive the derived issuer and public resource audience, and the TypeScript example listens on the reconciled upstream path while retaining bearer validation. Apps that validate tokens themselves now need network access to the issuer's JWKS endpoint even when the gateway is enabled; the Go example exits at startup if it cannot reach it ([#532](https://github.com/mcp-runtime/mcp-runtime/issues/532)).

### Security

- Add opt-in native Distribution authentication with `registry enable-auth`: repository-scoped five-minute tokens, namespace-bound read-only node credentials, trusted publication helpers, legacy copied-admin-key rotation with resumable recovery, and ClusterIP-only exposure. Activation requires updated API images and an external/public platform-api bootstrap image ([#531](https://github.com/mcp-runtime/mcp-runtime/issues/531)).
- Fresh cert-manager installs use v1.21.2 instead of the retired v1.16.2; TLS doctor checks flag unsupported Kubernetes/version pairs and inconsistent controller/webhook/cainjector versions. Existing installations remain unchanged and require staged minor upgrades with certificate/Secret backups ([#534](https://github.com/mcp-runtime/mcp-runtime/issues/534)).
- HTTPS registry overlays no longer expose the unauthenticated backend through NodePort 32000. Before upgrading, migrate any node mirrors using that port to the supported HTTPS pull endpoint and verify CA trust and fresh pulls. HTTP lab overlays retain their NodePort. Native authentication is available through `registry enable-auth` ([#531](https://github.com/mcp-runtime/mcp-runtime/issues/531)).

### Upgrading from 0.5.x

Follow the release deployment model (backup → fresh setup → restore → verify):

1. Back up the platform Postgres database, registry images, required Secrets
   and certificates, and the separately hosted identity provider's realm data
   and TLS state.
2. Update automation and runbooks: `mcp-runtime sentinel` → `mcp-runtime ops`;
   `setup --without-sentinel` / `MCP_WITHOUT_SENTINEL` →
   `--without-platform-stack` / `MCP_WITHOUT_PLATFORM_STACK`;
   `MCP_SENTINEL_INGEST_URL` → `MCP_ANALYTICS_INGEST_URL`; Traefik references to
   `sentinel-admin-auth@file` → `platform-admin-auth@file`. Run
   `mcp-runtime auth login` before `mcp-runtime status`, which now needs a
   saved login.
3. Move any node registry mirror that used NodePort 32000 on an HTTPS install to
   the registry ingress host. Make sure OAuth apps behind the gateway can reach
   their issuer's JWKS endpoint.
4. Run a fresh `mcp-runtime setup` from this release, restore the backed-up
   data, then verify login, image push and pull, server deployment, agent
   enrollment, grants and sessions, allowed and denied tool calls, and
   audit/observability.

## [0.5.2] - 2026-10-05

### Changed

- The reference deployment's demo Keycloak identity provider runs as a separate Docker/Caddy service with its own Let's Encrypt certificate, so a Runtime namespace reset does not remove it. K3s backups no longer include its old Kubernetes Secrets; back up Keycloak's realm data and Caddy state on the Buddy VM separately before recovery ([#589](https://github.com/mcp-runtime/mcp-runtime/pull/589)).
- Pre-release Regression uses Staging E2E as its only cluster suite, avoiding repeated Kind tenant/org/public runs and cache replay. Static, unit, integration, service, benchmark, and security checks remain; Kind QA stays in PR CI ([#588](https://github.com/mcp-runtime/mcp-runtime/pull/588)).

### Fixed

- The reference deployment includes a tracked, non-secret Keycloak connector configuration with public HTTPS token and JWKS endpoints. Setup reruns using that configuration preserve the working login endpoints after the identity provider moves to its separate VM; the client secret remains supplied through the deployment environment ([#589](https://github.com/mcp-runtime/mcp-runtime/pull/589)).

## [0.5.1] - 2026-10-04

### Changed

- CLI installation on macOS/Linux reports each step with terminal colors and download progress, bounds stalled transfers, and explains download or destination failures. Set `NO_COLOR` to disable colors ([#586](https://github.com/mcp-runtime/mcp-runtime/pull/586)).
- Documentation starts with hosted and self-hosted paths, followed by task guides, operations, concepts, and reference. Self-hosting instructions distinguish release installs from source builds and avoid an extra unconfigured setup run. Pages use descriptive filenames, with redirects preserving published URLs ([#586](https://github.com/mcp-runtime/mcp-runtime/pull/586)).

## [0.5.0] - 2026-10-02

This release moves the platform into owner namespaces. There is no in-place
upgrade from 0.4.x; see the migration note under **Changed**.

### Added

- Setup ends with an operational smoke gate: nodes Ready, Bound PVCs, no Pending blockers, Postgres, platform-api `/health` and `/ready`, platform rollout health, and an authenticated API probe. A failure fails setup; use `mcp-runtime cluster diagnostics` for deeper follow-up.
- `mcp-runtime sentinel grafana check` (read-only) detects when Grafana's persisted admin account rejects the password in `mcp-grafana-credentials`, and tells Grafana login apart from the platform ingress gate. `sentinel grafana reset-admin-password --yes` runs only on detected drift, backs up the Grafana database first, passes the password over stdin inside the pod, and verifies access ([#500](https://github.com/mcp-runtime/mcp-runtime/issues/500)).
- `cluster doctor` reports `sentinel Grafana provisioning` drift when the `mcp-server` dashboard uid, the `prometheus` datasource uid, or the Grafana provisioning mounts are missing, instead of surfacing only as "Dashboard not found" or "Data source not found" from server-card links ([#543](https://github.com/mcp-runtime/mcp-runtime/issues/543)).
- Gateway and platform service logs include `trace_id` and `span_id` on request and failure lines. Auth rejections (401/403), policy denials, and upstream or 5xx failures mark their spans as errors with a safe reason. OTLP export failures are logged and counted in `mcp_otel_internal_errors_total`, and `/health`, `/ready`, and `/metrics` probes are no longer traced ([#498](https://github.com/mcp-runtime/mcp-runtime/issues/498)).
- Prometheus scrapes the OTel collector, Loki, Tempo, Promtail, itself, and opt-in annotated Services in `mcp-platform` and `mcp-observability`. It ships coverage rules (required target absent or down, intentionally uninstrumented workloads) and OAuth-failure, gateway analytics-drop, and collector export-failure alerts. Gateways export a bounded `mcp_gateway_oauth_outcomes_total{outcome}` counter, and Grafana has a "Scrape Coverage" dashboard. The bundled `mcp-auth-server` is listed as not instrumented; its OAuth failures show through the gateway counter ([#497](https://github.com/mcp-runtime/mcp-runtime/issues/497)).
- The UI Deployment runs two replicas with `UI_SESSION_STORE=postgres`. Session rows live in Postgres, encrypted with `UI_SESSION_ENCRYPTION_KEY` from `mcp-ui-credentials`, so login and admin-check work across pods ([#257](https://github.com/mcp-runtime/mcp-runtime/issues/257)).
- One-command macOS, Linux, and Windows CLI installers (`install.sh`, `install.ps1`), linked from Getting Started and Quickstart. They detect the platform and install the matching release binary to a user-local directory; the Windows installer also adds that directory to the user's `PATH`.
- Self-hosted Go package browsing for the root, service, and example modules at `docs.pkg.mcpruntime.org`, linked from the docs site ([#574](https://github.com/mcp-runtime/mcp-runtime/pull/574), [#577](https://github.com/mcp-runtime/mcp-runtime/pull/577)).
- A platform regression coverage index (`docs/contributor/regression-index.yaml`) maps incidents to regression checks. CI fails on an unmapped incident or an expired waiver ([#546](https://github.com/mcp-runtime/mcp-runtime/issues/546)).

### Changed

- **Breaking: owner namespaces.** A clean `mcp-runtime setup` installs the operator in `mcp-runtime`; platform-api, runtime-api, UI, the platform gateway, and Postgres in `mcp-platform`; the event pipeline and telemetry in `mcp-observability`; and Promtail in `mcp-log-collector`. MCP servers stay in `mcp-servers`, `mcp-servers-org`, `mcp-servers-public`, or `mcp-team-{slug}`. See `docs/namespaces.md` ([#548](https://github.com/mcp-runtime/mcp-runtime/issues/548), [#573](https://github.com/mcp-runtime/mcp-runtime/pull/573)).
  **Migration from 0.4.x:** setup does not move or remove the old `mcp-sentinel` namespace. Back up the public TLS Secret and any OIDC or admin bootstrap Secrets, delete `mcp-sentinel` (this deletes the Postgres, Kafka, and ClickHouse data stored there: users, teams, API keys, and analytics), run `mcp-runtime setup`, and restore the certificate as `mcp-platform-tls` in `mcp-platform`. `hack/deploy/mcpruntime-org/clean.sh --yes` and `setup.sh` automate the backup and restore for the reference deployment.
- Each workload reads its own credential Secret in its owner namespace (for example `mcp-platform-api-credentials`, `mcp-ui-credentials`, `mcp-grafana-credentials`). A setup rerun keeps values already stored on those Secrets, and builds `POSTGRES_DSN` for `mcp-postgres.mcp-platform.svc` only when the key is absent ([#548](https://github.com/mcp-runtime/mcp-runtime/issues/548)).
- The shared ConfigMap and PriorityClasses use the `mcp-shared-` prefix (`mcp-shared-config`, `mcp-shared-data`, `mcp-shared-services`) because more than one namespace uses them. The public platform certificate is a single `mcp-platform-tls` Secret in `mcp-platform`; Grafana and analytics-api Ingresses live beside their Services in `mcp-observability` and do not open a second ACME order.
- Setup rolls a workload only when a Secret or ConfigMap value it consumes changes, replacing blanket platform restarts ([#548](https://github.com/mcp-runtime/mcp-runtime/issues/548)).
- Recovery hardening for node pressure and eviction: Kafka, ClickHouse, and Postgres run under the `mcp-shared-data` PriorityClass and request-serving services under `mcp-shared-services`; StatefulSet PVCs are retained on delete or scale-down; ClickHouse gains requests, limits, and probes; Kafka, ClickHouse, ingest, and processor gain startup probes; and ingest and processor use `imagePullPolicy: IfNotPresent` for pinned images. Setup prunes Failed, Evicted, and orphaned Completed pods in `mcp-platform` and `mcp-observability`, and `cluster doctor` reports them as `sentinel stale pods` ([#71](https://github.com/mcp-runtime/mcp-runtime/issues/71), [#72](https://github.com/mcp-runtime/mcp-runtime/issues/72)).
- Promtail parses CRI log envelopes, discovers node-local pods across all namespaces, labels streams with `namespace`, `pod`, `container`, `app`, and `node`, and redacts bearer tokens and secret-looking JSON fields. This is intended to restore Loki coverage for MCP server and team namespaces; the original production gap is not yet confirmed fixed on a live cluster ([#496](https://github.com/mcp-runtime/mcp-runtime/issues/496)).
- Contributors: pull-request QA E2E runs Kind on a fresh GitHub runner with unique cluster names, so PRs run it in parallel, and pulls unchanged platform images from the content-hash GHCR cache. Each PR runs `smoke-auth` plus the scenarios that cover its changed paths, including service manifests, the gateway, shared API packages, and each scenario's own harness. Staging E2E on the disposable VM runs from Pre-release Regression or by hand, one run at a time, instead of on every merge. Pre-release deep mode runs the UI auth and platform API flows again. Both suites fail a clean install that still points at the old combined namespace ([#569](https://github.com/mcp-runtime/mcp-runtime/pull/569), [#576](https://github.com/mcp-runtime/mcp-runtime/pull/576)).

### Fixed

- UI replicas retry session-table creation on a concurrent Postgres catalog collision, wait for the Postgres Service name to resolve, and use a startup probe while they wait, so a two-replica start no longer crash-loops and fails setup smoke.
- Setup replaces a gateway trace endpoint or ingest URL that still names the removed combined namespace with the `mcp-observability` Service. Addresses outside that namespace are kept.
- `cluster doctor` retries transient connection errors from its in-cluster registry probe before reporting the registry unreachable. Registry doctor and in-cluster image push failures include helper-container diagnostics ([#569](https://github.com/mcp-runtime/mcp-runtime/pull/569)).
- Docs say a saved login does not see a team membership added after that login, list the example server's `whoami` tool, and state that `MCP_ADAPTER_CERTIFICATES` only turns on ingress verification of an adapter certificate; an allow-list `tools/call` stays `missing_identity` until verification is on.

### Security

- Adapter certificates require a workload-issuer approval policy. Outside `--test-mode`, setup refuses `MCP_ADAPTER_CERTIFICATES=true` unless cert-manager approver-policy is installed or `MCP_WORKLOAD_ISSUER_APPROVAL_ACK=true` acknowledges another approver. The runtime API rejects an issued certificate whose SPIFFE URI, key, usage, or lifetime differs from the submitted CSR, and its ClusterRole no longer grants `list` or `watch` on CertificateRequests. Adapter issuance stays off by default ([#538](https://github.com/mcp-runtime/mcp-runtime/issues/538)).
- Setup validates the bundled `mcp-runtime-ca` workload CA before use (key and certificate match, CA constraints, validity). Production setup fails on a missing, invalid, expired, or under-180-day CA instead of generating or accepting it; test mode still generates a missing CA and warns near expiry. Migration: production installs using `--mtls-cluster-issuer mcp-runtime-ca` must have the CA Secret in place before setup. Rotation and backup guidance is in `docs/cli-reference.md` ([#535](https://github.com/mcp-runtime/mcp-runtime/issues/535)).
- Operator Secret permissions are scoped to MCPServer namespaces, and Secret reads bypass the controller cache. Runtime-managed namespaces get a scoped binding automatically, and setup backfills existing ones. The TLS namespace grants named read and update access only to the public adapter trust bundle ([#540](https://github.com/mcp-runtime/mcp-runtime/issues/540)).
- Platform application workloads run under restricted Pod Security admission. Node log collection runs in `mcp-log-collector`, the only namespace with the hostPath exception ([#548](https://github.com/mcp-runtime/mcp-runtime/issues/548)).
- The internal registry NetworkPolicy no longer admits tenant workload namespaces (`mcp-servers*` and team namespaces), closing the unauthenticated in-cluster registry read and write path from tenant pods. Node pulls and platform publish paths are unchanged. Registry-native authentication remains a follow-up ([#531](https://github.com/mcp-runtime/mcp-runtime/issues/531)).

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

[Unreleased]: https://github.com/mcp-runtime/mcp-runtime/compare/v0.6.6...HEAD
[0.6.6]: https://github.com/mcp-runtime/mcp-runtime/compare/v0.6.5...v0.6.6
[0.6.5]: https://github.com/mcp-runtime/mcp-runtime/compare/v0.6.4...v0.6.5
[0.6.4]: https://github.com/mcp-runtime/mcp-runtime/compare/v0.6.3...v0.6.4
[0.6.3]: https://github.com/mcp-runtime/mcp-runtime/compare/v0.6.2...v0.6.3
[0.6.2]: https://github.com/mcp-runtime/mcp-runtime/compare/v0.6.1...v0.6.2
[0.6.1]: https://github.com/mcp-runtime/mcp-runtime/compare/v0.6.0...v0.6.1
[0.6.0]: https://github.com/mcp-runtime/mcp-runtime/compare/v0.5.2...v0.6.0
[0.5.2]: https://github.com/mcp-runtime/mcp-runtime/compare/v0.5.1...v0.5.2
[0.5.1]: https://github.com/mcp-runtime/mcp-runtime/compare/v0.5.0...v0.5.1
[0.5.0]: https://github.com/mcp-runtime/mcp-runtime/compare/v0.4.1...v0.5.0
[0.4.1]: https://github.com/mcp-runtime/mcp-runtime/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/mcp-runtime/mcp-runtime/compare/v0.3.2...v0.4.0
[0.3.2]: https://github.com/mcp-runtime/mcp-runtime/compare/v0.3.0...v0.3.2
