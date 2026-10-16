# Changelog

Notable changes are curated here for users and operators. The format follows
[Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/); versions use
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Earlier release notes remain available in [GitHub Releases](https://github.com/mcp-runtime/mcp-runtime/releases).

## [Unreleased]

### Added

- Ingest operations docs now include PromQL for Kafka readiness DNS/connect/total latency and bounded failure reasons, plus guidance for correlating slow checks with broker, node and DNS signals. This exposes investigation paths; reference-platform root-cause and under-one-second acceptance remain outstanding ([#624](https://github.com/mcp-runtime/mcp-runtime/issues/624)).
- `MCPAccessGrant.spec.mode: monitor` shadows matched grant tool, trust, and side-effect denials with gateway audit fields and a bounded `mcp_gateway_policy_would_deny_total` counter. Identity/session checks, disabled or expired grants, and matching enforcing-grant denials remain blocking; deploy schema-v5-capable gateways before enabling monitor grants ([#307](https://github.com/mcp-runtime/mcp-runtime/issues/307)).

