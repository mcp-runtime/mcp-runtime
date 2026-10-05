# API authn/authz matrix

This is the **source of truth** for which roles can call which endpoint on
`mcp-platform-api`, `mcp-runtime-api`, and `mcp-analytics-api`. The `security-audit` skill mode `platform` (see
`.codex/skills/security-audit/references/platform.md`) compares the live
services against this table. A divergence in either direction is a finding:

- A route in `services/platform-api/routes.go`, `services/runtime-api/routes.go`,
  or `services/analytics-api/routes.go` that is missing from this table → add a
  row, then verify the expected status.
- A row whose expected status differs from the live response → fix the
  route or fix the matrix, but do not silently change one to match the
  other.

Source of routes: per-service `routes.go` files (public surface is `/api/v1/*`
only). Path items ending with `/` route to a handler that parses sub-paths
internally; include both the prefix and the meaningful sub-paths in the table.

Status expectations assume valid inputs, healthy dependencies, and access to the
requested resource. A static service key has no user identity; it cannot perform
user-owned credential or activity operations even when its role is admin.

## Roles

| Role             | Credential                                                   | Source secret key                                             |
|------------------|--------------------------------------------------------------|---------------------------------------------------------------|
| `anon`           | none                                                         | n/a                                                           |
| `user-cookie`    | logged-in browser session (platform identity)                | seeded via `PLATFORM_DEV_*` in test mode, OIDC otherwise      |
| `user-key`       | `x-api-key` matching a user-scoped key                       | Created for a platform user through the CLI or UI |
| `admin-key`      | `x-api-key` matching `ADMIN_API_KEYS` entry                  | `mcp-platform-api-credentials` `ADMIN_API_KEYS` in `mcp-platform` |
| `ingest-key`     | `x-api-key` matching `INGEST_API_KEYS` entry                 | `mcp-ingest-credentials` `INGEST_API_KEYS` in `mcp-observability` |

`admin-role` is enforced by `platformauth.Authenticator.RequireRole` wraps in
each split service's `routes.go`. `user-cookie` and `user-key` distinguish which
calls require browser identity vs accept a bearer-style key.
When `ADMIN_API_KEYS` is unset, static `API_KEYS` authenticate as `user`
unless the explicit legacy dev/test fallback is enabled.

Expected codes:

- **200/201/202/204**: allowed; handler returns successfully.
- **401**: auth missing/invalid.
- **403**: auth valid but role insufficient.
- **404**: handler returns not-found for valid auth (path-item endpoints).
- **405**: method not allowed (handler-side check).

## Public endpoints

| Path                | Methods | anon | user-cookie | user-key | admin-key | ingest-key | Notes |
|---------------------|---------|------|-------------|----------|-----------|------------|-------|
| `/health`           | GET     | 200  | 200         | 200      | 200       | 200        | Returns 503 when `runtimeInit != ""`. |
| `/api/v1/auth/login`   | POST    | 200  | 200         | 200      | 200       | 200        | Local password login when `PLATFORM_DEV_*` enabled. |
| `/api/v1/auth/oidc`    | POST    | 200  | 200         | 200      | 200       | 200        | OIDC code exchange. |
| `/api/v1/auth/signup`  | POST    | 200  | 200         | 200      | 200       | 200        | Disabled in prod when configured; verify before deploy. |

> All four of these routes are reachable without auth by design. If any
> later mutates state on behalf of a caller without further checks, that
> is a Critical finding.

## User-authenticated endpoints (any authenticated identity)

| Path                                                  | Methods       | anon | user-cookie | user-key | admin-key | ingest-key | Notes |
|-------------------------------------------------------|---------------|------|-------------|----------|-----------|------------|-------|
| `/api/v1/auth/me`                                        | GET           | 401  | 200         | 200      | 200       | 401/403    | Returns identity claims; ingest-only keys are not API auth identities. |
| `/api/v1/user/registry-credentials`                      | GET, POST     | 401  | 200         | 200      | 401       | 401/403    | Ingest-only key must NOT manage user creds. Verify rejection. |
| `/api/v1/user/registry-credentials/{id}`                 | DELETE        | 401  | 200         | 200      | 401       | 401/403    | Revoke an owned credential. Legacy POST uses `{id}/revoke`. |
| `/api/v1/user/activity/image-publish`                    | POST          | 401  | 202         | 202      | 401       | 401/403    | Record an image publication for the authenticated user. |
| `/api/v1/user/api-keys`                                  | GET, POST     | 401  | 200         | 200      | 401       | 401/403    | Lifecycle for user-owned keys. |
| `/api/v1/user/api-keys/{id}`                             | DELETE        | 401  | 200         | 200      | 401       | 401/403    | |
| `/api/v1/runtime/servers`                                | GET, POST     | 401  | 200         | 200      | 200       | 401/403    | List/create MCP servers. |
| `/api/v1/runtime/observability/links`                    | GET           | 401  | 200/403     | 200/403  | 200       | 401/403    | Normal users are limited to team namespaces or caller-owned catalog servers. |
| `/api/v1/runtime/observability/prometheus/query`         | GET           | 401  | 200/403     | 200/403  | 200       | 401/403    | PromQL is allowlisted and server-scoped by the API. |
| `/api/v1/runtime/teams`                                  | GET           | 401  | 200         | 200      | 200       | 401/403    | |
| `/api/v1/runtime/teams`                                  | POST          | 401  | 403         | 403      | 200       | 401/403    | Admin-only team + namespace provisioning. |
| `/api/v1/runtime/teams/{id}`                             | GET           | 401  | 200         | 200      | 200       | 401/403    | Team members can read only their teams; admins can read all teams. |
| `/api/v1/runtime/teams/{id}/members`                     | GET, POST     | 401  | 200         | 200      | 200       | 401/403    | POST requires admin or team owner. |
| `/api/v1/runtime/teams/{slug}/agents`                    | GET           | 401  | 200         | 200      | 200       | 401/403    | Team members can list/search their team's agents. |
| `/api/v1/runtime/teams/{slug}/agents`                    | POST          | 401  | 403         | 403      | 201       | 401/403    | Create requires admin or owning-team owner. |
| `/api/v1/runtime/agents/{id}`                            | GET           | 401  | 200/403     | 200/403  | 200/404   | 401/403    | Admin/team members can view; other teams are forbidden. |
| `/api/v1/runtime/agents/{id}`                            | PATCH         | 401  | 403         | 403      | 200/404   | 401/403    | Rename requires admin or owning-team owner. |
| `/api/v1/runtime/agents/{id}/deactivate`               | POST          | 401  | 403         | 403      | 200/404   | 401/403    | Admin or owning-team owner; marks inactive and revokes active sessions across namespaces. |
| `/api/v1/runtime/agents/{id}/reactivate`               | POST          | 401  | 403         | 403      | 200/404   | 401/403    | Lifecycle change requires admin or owning-team owner. |
| `/api/v1/users`                                          | POST          | 401  | 403         | 403      | 200       | 401/403    | Admin-only password user create. |
| `/api/v1/runtime/namespaces`                             | GET           | 401  | 200         | 200      | 200       | 401/403    | |
| `/api/v1/runtime/namespaces/{name}`                      | GET           | 401  | 200         | 200      | 200       | 401/403    | |
| `/api/v1/deployments`                                    | GET           | 401  | 200         | 200      | 200       | 401/403    | |
| `/api/v1/deployments/{namespace}/{name}`                               | GET           | 401  | 200         | 200      | 200       | 401/403    | |
| `/api/v1/runtime/server-events`                          | GET           | 401  | 200/403     | 200/403  | 200       | 401/403    | Full event details only for admin, server owner, or team owner; regular namespace readers are forbidden. |
| `/api/v1/runtime/grants`                                 | GET           | 401  | 200         | 200      | 200       | 401/403    | Lists only grants for servers the caller can administer; regular team/catalog readers receive an empty scoped set. |
| `/api/v1/runtime/grants`                                 | POST          | 401  | 200/403/422/503 | 200/403/422/503 | 200/422/503 | 401/403 | Create/update requires admin, server owner, or team owner. Agent subjects must be active directory entries belonging to the subject team; cross-team human/agent subjects must also belong to the named team and require an expiry within `MCP_CROSS_TEAM_GRANT_MAX_TTL` (default seven days). Invalid subjects/expiry return 422; unavailable directory returns 503; invalid TTL configuration returns 500. |
| `/api/v1/runtime/grants/{ns}/{name}`                     | GET           | 401  | 200/403     | 200/403  | 200       | 401/403    | Full grant summary only for admin, server owner, or team owner. |
| `/api/v1/runtime/grants/{ns}/{name}`                     | DELETE        | 401  | 200/403     | 200/403  | 200       | 401/403    | Mutating; same owner/team-owner gate as apply. |
| `/api/v1/runtime/grants/{ns}/{name}/enable`              | POST          | 401  | 200/403     | 200/403  | 200       | 401/403    | Mutating; same owner/team-owner gate as apply. |
| `/api/v1/runtime/grants/{ns}/{name}/disable`             | POST          | 401  | 200/403     | 200/403  | 200       | 401/403    | Mutating; same owner/team-owner gate as apply. |
| `/api/v1/runtime/grants/{ns}/{name}/revoke-sessions`      | POST          | 401  | 200/403     | 200/403  | 200       | 401/403    | Revokes every active adapter session issued from this grant; requires authority over its referenced server. |
| `/api/v1/runtime/sessions`                               | GET           | 401  | 200         | 200      | 200       | 401/403    | Lists only sessions for servers the caller can administer. |
| `/api/v1/runtime/sessions`                               | POST          | 401  | 403         | 403      | 200/422/503 | 401/403   | Admin/internal-only; agent subjects must be active directory entries in the subject team. |
| `/api/v1/runtime/sessions/{ns}/{name}`                   | GET           | 401  | 200/403     | 200/403  | 200       | 401/403    | Full session summary only for admin, server owner, or team owner. |
| `/api/v1/runtime/sessions/{ns}/{name}`                   | DELETE        | 401  | 200/403     | 200/403  | 200       | 401/403    | Mutating; requires admin, server owner, or team owner. |
| `/api/v1/runtime/sessions/{ns}/{name}/revoke`            | POST          | 401  | 200/403     | 200/403  | 200       | 401/403    | Mutating; requires admin, server owner, or team owner. |
| `/api/v1/runtime/sessions/{ns}/{name}/unrevoke`          | POST          | 401  | 200/403     | 200/403  | 200       | 401/403    | Mutating; requires admin, server owner, or team owner. |
| `/api/v1/runtime/policy`                                 | GET           | 401  | 200/403     | 200/403  | 200       | 401/403    | Rendered policy is visible only to admin, server owner, or team owner. |

## Admin-only endpoints (`requireRole(roleAdmin, …)`)

| Path                              | Methods       | anon | user-cookie (non-admin) | user-key | admin-key | ingest-key | Notes |
|-----------------------------------|---------------|------|-------------------------|----------|-----------|------------|-------|
| `/api/v1/events`                     | GET           | 401  | 403                     | 403      | 200       | 403        | Admin event log. |
| `/api/v1/stats`                      | GET           | 401  | 403                     | 403      | 200       | 403        | |
| `/api/v1/sources`                    | GET           | 401  | 403                     | 403      | 200       | 403        | |
| `/api/v1/event-types`                | GET           | 401  | 403                     | 403      | 200       | 403        | |
| `/api/v1/analytics/usage`            | GET           | 401  | 403                     | 403      | 200       | 403        | |
| `/api/v1/dashboard/summary`          | GET           | 401  | 403                     | 403      | 200       | 403        | |
| `/api/v1/admin/namespaces`           | GET           | 401  | 403                     | 403      | 200       | 403        | |
| `/api/v1/admin/audit`                | GET           | 401  | 403                     | 403      | 200       | 403        | |
| `/api/v1/admin/operations`           | GET           | 401  | 403                     | 403      | 200       | 403        | |
| `/api/v1/admin/deployments`          | GET           | 401  | 403                     | 403      | 200       | 403        | |
| `/api/v1/runtime/components`         | GET           | 401  | 403                     | 403      | 200       | 403        | Cluster component/workload health and errors. |
| `/api/v1/runtime/actions/restart`    | POST          | 401  | 403                     | 403      | 200       | 403        | Cluster-affecting. |

## Method gating

`http.NewServeMux` does not enforce HTTP methods; the handler does. For each
row above, the auditor must also confirm:

- The handler rejects methods not listed with **405**.
- A method-confused request (e.g., `OPTIONS` followed by `POST`) cannot
  bypass the role check.
- Unknown sub-paths under `/api/v1/runtime/grants/` and similar prefix routes
  return **404**, not 200 with a default action.

## Drift check

The platform audit harness in
`.codex/skills/security-audit/references/platform.md` reads this table
and exercises each row. The starter harness lives in
`docs/security/authz-matrix.json` (subset of rows); unit tests in each split
service load that file via `pkg/authzmatrix`. Expand the JSON toward full table
parity over time. Each row object looks like:

```json
{ "path": "/api/v1/events", "method": "GET", "role": "ingest", "expect": 403 }
```

Keep the JSON and the markdown in sync; the markdown is canonical for
review, the JSON is canonical for the harness.

## Open follow-ups

- Expand `docs/security/authz-matrix.json` toward full parity with the markdown
  table; run `bash hack/validate-authz-matrix.sh` on a live cluster for drift.
- Confirm whether `user-key` and `user-cookie` should be merged for
  authorization purposes, or whether some routes (e.g., billing, admin
  bootstrap) intentionally accept only one credential type.
- Document whether `/api/v1/runtime/*` mutations are scoped to the calling
  user's tenancy, and how that tenancy is derived (subject claim,
  namespace label, etc.). The audit cannot judge cross-tenant safety
  without that anchor.
