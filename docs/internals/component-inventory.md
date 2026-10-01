# Component identity and namespace placement

`pkg/platforminventory` is the owner-to-namespace map. `Lookup` returns a
component whose namespace comes from that owner. There is no layout version.

| Owner | Namespace |
|---|---|
| Operator | `mcp-runtime` |
| Platform | `mcp-platform` |
| Observability | `mcp-observability` |
| Log collector | `mcp-log-collector` |
| Certificates | `cert-manager` |
| Registry | `registry` |
| Ingress | `traefik` |

`CredentialPlacement` returns the Secret name and namespace for a key.
`ServiceHost` builds `resource.namespace.svc.cluster.local:port`.

The inventory covers managed long-running components, including Postgres,
optional auth, registry, ingress, and cert-manager. Tenant MCP servers and
transient setup jobs keep their own identities. The inventory does not grant
authority over those namespaces.

CLI inspection and platform status read this inventory. Adding an inventory
entry does not add it to `sentinel restart --all` or release updates. Release
image and container metadata stay in `internal/platformrelease`.
