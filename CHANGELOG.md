# Changelog

Notable changes are curated here for users and operators. The format follows
[Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/); versions use
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Earlier release notes remain available in [GitHub Releases](https://github.com/mcp-runtime/mcp-runtime/releases).

## [Unreleased]

### Added

- Setup ends with a short operational smoke gate (nodes Ready, Bound PVCs, no Pending blockers, Postgres, platform-api `/health`+`/ready`, Sentinel rollout health, and an authenticated API probe). Failures fail setup; use `mcp-runtime cluster diagnostics` for deeper follow-up.
- One-command macOS, Linux, and Windows CLI installers from the Getting Started and Quickstart pages; installers detect the platform and install the matching release binary to a user-local directory. The Windows installer also adds its directory to the user's `PATH`.

- Prometheus now scrapes the OTel collector, Loki, Tempo, Promtail, itself, and opt-in annotated Services in `mcp-sentinel`, and ships coverage rules (required target absent or down, intentionally uninstrumented workloads), OAuth-failure, gateway analytics-drop, and collector export-failure alerts. Gateways export a bounded `mcp_gateway_oauth_outcomes_total{outcome}` counter, and Grafana has a new "Scrape Coverage" dashboard ([#497](https://github.com/mcp-runtime/mcp-runtime/issues/497)). The bundled `mcp-auth-server` image is listed as not instrumented; its OAuth failures are visible through the gateway counter.

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
