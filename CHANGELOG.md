# Changelog

Notable changes are curated here for users and operators. The format follows
[Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/); versions use
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Earlier release notes remain available in [GitHub Releases](https://github.com/mcp-runtime/mcp-runtime/releases).

## [Unreleased]

### Added

- Ingest operations docs now include PromQL for Kafka readiness DNS/connect/total latency and bounded failure reasons, plus guidance for correlating slow checks with broker, node and DNS signals. This exposes investigation paths; reference-platform root-cause and under-one-second acceptance remain outstanding ([#624](https://github.com/mcp-runtime/mcp-runtime/issues/624)).
- macOS adapter service commands can keep separately authorized, session-bound MCP profiles behind a per-user LaunchAgent, protected local client tokens and redacted status. This initial CLI daemon slice does not include the planned Dock app or signed/notarized distribution yet ([#656](https://github.com/mcp-runtime/mcp-runtime/issues/656)).
