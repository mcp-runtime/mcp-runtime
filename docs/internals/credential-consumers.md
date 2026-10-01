# Credential consumers

`pkg/platforminventory.CredentialSets` owns the key allowlists for generated
consumer Secrets. Setup renders the API, UI, ingest, Grafana, Postgres and
bootstrap references in the existing namespace; it does not relocate data.
Only declared keys are synchronized, and an undeclared key blocks rendering.

On the first upgrade, absent owner objects import installed legacy values.
Afterwards setup snapshots owner objects once and uses their values, including
intentional empty optional keys. Required credentials missing from an existing
owner fail setup instead of generating replacements. Shared API authentication
keys belong to the platform API set and are copied to runtime/analytics/UI only
where declared. UI and ingest keys have separate owners. Postgres initialization
credentials remain separate from the platform API's connection string.

The legacy `mcp-sentinel-secrets` remains a synchronized compatibility mirror
for existing pods, doctor/preflight paths and recovery. This phase does not remove
its credentials or narrow broad Secret/workload API permissions. P03 must deny
unrelated identities direct/indirect access. Remove the mirror only after every
supported management/recovery reader uses canonical objects and upgrade/rollback
checks demonstrate no remaining legacy references. Nonsecret configuration
separation remains follow-up P02 scope.

Rotation must keep shared copies consistent before consumers restart; P04
supplies targeted rollout planning. Setup currently retains its existing rollout
behavior. Grafana's persisted password and Postgres's database role still require
their existing reconciliation procedures; Secret copies alone do not rotate stores.
