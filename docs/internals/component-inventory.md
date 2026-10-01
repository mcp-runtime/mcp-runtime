# Component identity and namespace placement

`pkg/platforminventory` owns stable component IDs, CLI aliases, workload names,
legacy namespaces, ownership domains, capabilities and service dependencies.
`pkg/sentinel`, CLI Sentinel management, platform status and release updates
project their existing management surfaces from this inventory. Adding an
inventory entry does not add it to `sentinel restart --all` or release updates.
Release image/container metadata and rollout order remain in
`internal/platformrelease`; release manifests cannot choose arbitrary workloads.

The inventory covers managed long-running components and release image targets,
including Postgres, optional auth, registry, ingress and cert-manager. Tenant
MCPServers and transient setup/maintenance jobs retain their own resource
identities; the inventory does not grant authority over their namespaces.
Dependencies describe service relationships, including optional telemetry
relationships, rather than a topological startup order.

A version 1 `Layout` maps ownership domains to namespaces. `legacy-v1` describes
the current installation. The `separated-v1` template places platform workloads
in `mcp-platform`, observability in `mcp-observability`, and the node log collector
in `mcp-log-collector`. The operator, registry, ingress and upstream certificate
services keep their namespaces. Templates and resolution perform no writes.
Existing commands continue to use the legacy placement until a later explicit
migration integrates a persisted record and discovery through supported setup.

`ResolveLayout` accepts observations of exact inventory workload identities.
Without a record, it validates the legacy layout; an absent workload is allowed.
A workload in another namespace, two placements or a record/observation mismatch
returns an error requiring migration. Callers must query the possible namespaces
and must surface authorization/discovery errors rather than treating them as
absence. Namespace selection does not authorize access: provisioning must still
validate reserved namespaces, tenant ownership and least-privilege RBAC.

This is the inventory/resolver foundation for P01 in
[the namespace refactor plan PR](https://github.com/mcp-runtime/mcp-runtime/pull/552). Persisted layout
handling and setup/update migration gates remain follow-up work; changing the
inventory defaults alone is not a supported migration.
