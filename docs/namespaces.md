# Namespaces

`pkg/platforminventory` is the only owner-to-namespace map. `mcp-runtime setup`
installs into these namespaces.

| Namespace | What runs there |
|---|---|
| `mcp-runtime` | Operator, admission webhooks, and leader election. |
| `mcp-platform` | platform-api, runtime-api, UI, the platform gateway Deployment, Postgres, the optional mcp-auth server, and the admin bootstrap job. |
| `mcp-observability` | analytics-api, ingest, processor, Kafka, ClickHouse, Prometheus, Grafana, the OTel collector, Loki, and Tempo. |
| `mcp-log-collector` | Promtail. This namespace is the privileged Pod Security exception for node log collection. |
| `registry`, `traefik`, `cert-manager` | Unchanged infrastructure. A k3s Traefik install stays in `kube-system`. |
| `mcp-servers`, `mcp-servers-org`, `mcp-servers-public`, `mcp-team-{slug}` | MCP server workloads. They do not run in the platform or observability namespaces. Direct apply with no team uses `mcp-servers`. |

An Ingress is applied in the same namespace as the Service it targets. Dev
routes for Grafana, ingest, and analytics-api live in `mcp-observability`. UI,
platform-api, and runtime-api routes live in `mcp-platform`. The host-based
dashboard Ingress `mcp-platform-ui` is created in `mcp-platform` and is the
only owner of the `mcp-platform-tls` certificate. The public Grafana and
analytics-api routes live in `mcp-observability` and do not request a second
certificate for the same hostname.
Setup writes the shared ConfigMap `mcp-shared-config` in both `mcp-platform` and `mcp-observability`. The name is the same because the document is the same; the namespace is the location.

Inside the cluster, callers use Service DNS such as
`mcp-runtime-api.mcp-platform.svc.cluster.local` and
`mcp-analytics-api.mcp-observability.svc.cluster.local`. The browser
enters through Traefik. The UI then calls runtime-api and analytics-api on
that Service DNS.

## Credential Secrets

Each key lives on the Secret that owns it, in that owner's namespace.

| Secret | Namespace | Keys |
|---|---|---|
| `mcp-platform-api-credentials` | `mcp-platform` | API keys, admin keys, JWT, Postgres DSN, bootstrap users |
| `mcp-runtime-api-credentials` | `mcp-platform` | API auth copies used by runtime-api |
| `mcp-ui-credentials` | `mcp-platform` | `UI_API_KEY`, UI session database URL and encryption key |
| `mcp-postgres-credentials` | `mcp-platform` | Postgres user, password, and database name |
| `mcp-platform-signing-credentials` | `mcp-platform` | OAuth signing key |
| `mcp-analytics-api-credentials` | `mcp-observability` | API auth copies used by analytics-api |
| `mcp-ingest-credentials` | `mcp-observability` | `INGEST_API_KEYS` |
| `mcp-grafana-credentials` | `mcp-observability` | Grafana admin user and password |
| `mcp-runtime-ingest-credentials` | `mcp-platform` | Ingest key copy read by runtime-api |

The bundled Traefik allowlist is `registry`, `mcp-platform`,
`mcp-observability`, `mcp-log-collector`, `mcp-servers`, `mcp-servers-org`, and
`mcp-servers-public`. Team create appends `mcp-team-{slug}` when it patches
the repo-managed Traefik Deployment.

Service behavior is in [Platform services](platform-services.md).
