# Changelog

Notable changes are curated here for users and operators. The format follows
[Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/); versions use
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Earlier release notes remain available in [GitHub Releases](https://github.com/mcp-runtime/mcp-runtime/releases).

## [Unreleased]

### Added

- Ingest operations docs now include PromQL for Kafka readiness DNS/connect/total latency and bounded failure reasons, plus guidance for correlating slow checks with broker, node and DNS signals. This exposes investigation paths; reference-platform root-cause and under-one-second acceptance remain outstanding ([#624](https://github.com/mcp-runtime/mcp-runtime/issues/624)).
- Kafka ingress is limited to the platform ingest, processor, topic-init job, and broker peers, reducing which pods can reach the bundled PLAINTEXT broker ([#110](https://github.com/mcp-runtime/mcp-runtime/issues/110)).
