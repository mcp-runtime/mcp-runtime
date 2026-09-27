# example-rust-2025-11-25

Rust server using `rmcp` 2.0.0. This release implements MCP `2025-11-25` and
uses a local session manager for Streamable HTTP.
Its checked-in Runtime server identity ends in `-gateway` because the metadata
enables the Runtime gateway for policy, sessions, and audit.

Build and run locally:

```bash
cargo run
```
