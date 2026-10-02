# Credential consumers

`pkg/platforminventory.CredentialSets` is the allowlist of generated consumer
Secrets. `CredentialPlacement` returns the Secret name and namespace for a key.
Setup reads and writes that owner object.

Each Secret is created in the namespace of its first consumer. The signing key
has no consumer and stays in `mcp-platform`. Shared API authentication keys
belong to `mcp-platform-api-credentials` and are copied only onto the consumer
sets that declare them. UI session keys (`UI_SESSION_DATABASE_URL`,
`UI_SESSION_ENCRYPTION_KEY`) and `UI_API_KEY` belong to `mcp-ui-credentials`.
`INGEST_API_KEYS` belongs to `mcp-ingest-credentials` in `mcp-observability`;
runtime-api receives its own copy in `mcp-runtime-ingest-credentials`. Postgres
role credentials stay on `mcp-postgres-credentials`. The platform API connection
string stays on `mcp-platform-api-credentials`.

A rerun snapshots each owner Secret and keeps the stored value, including an
intentional empty optional key. A required key that is present and empty fails
setup instead of generating a replacement. An undeclared key blocks rendering.

Grafana's persisted admin password and Postgres's database role still need
their existing reconciliation procedures. Copying a Secret does not rotate
those stores. The Grafana password is `GRAFANA_ADMIN_PASSWORD` in
`mcp-grafana-credentials`.
