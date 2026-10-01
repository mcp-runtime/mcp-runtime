
# Public Platform Configuration

## Hostname model

With `export MCP_PLATFORM_DOMAIN=example.com` (apex only, no `https://`):

| Role | Host |
|------|------|
| Registry (push/pull ingress) | `registry.example.com` |
| MCP servers (default host-based) | `mcp.example.com` |
| Dashboard / API / Grafana path | `platform.example.com` |

Override individual hosts with `MCP_REGISTRY_INGRESS_HOST`, `MCP_MCP_INGRESS_HOST`, `MCP_PLATFORM_INGRESS_HOST`.

Operator may set `MCP_DEFAULT_INGRESS_HOST=mcp.<domain>` from platform domain env.

## Expected URLs (after DNS + TLS)

- **Dashboard:** `https://platform.<domain>/` (API at `/api/v1/*` via Traefik). Grafana at `/grafana` via `mcp-platform-observability` + `sentinel-admin-auth@file` (admin cookie or admin `x-api-key`). Prometheus stays internal — port-forward only for backend debug.
- **Registry:** `https://registry.<domain>/v2/` (admin auth via `registry-admin-auth@file` → `/api/v1/registry/authz`)
- **MCP server:** `https://mcp.<domain>/<server-name>/mcp` (path-based; set `spec.publicPathPrefix` and `MCP_PATH`)

Default `MCPServer` ingress class: **`traefik`**.

## TLS setup

```bash
./bin/mcp-runtime setup --with-tls --acme-email <addr>
# staging: --acme-staging or MCP_ACME_STAGING=1
# enterprise CA (no ACME): --with-tls --tls-cluster-issuer <name>  # mutually exclusive with --acme-email
```

**DNS requirements**

- A/AAAA (or CNAME) for `registry.`, `mcp.`, and `platform.` → same ingress IP/LB
- Port **80** → Traefik for HTTP-01 before certs issue
- Registry default-deny policies must allow Traefik to reach cert-manager HTTP-01 solver pods on TCP 8089. Setup applies `config/registry/base/acme-networkpolicy.yaml` before requesting registry certificates; the registry overlay retains it for renewals. If challenge self-checks return 502 while solver pods are Ready, inspect this policy and Traefik pod labels before retrying issuance.
- Typos (`regsitry`, `platfrom`) break matching certificates

**Certificates**

- `registry/registry-cert` → `registry/registry-tls` (only supported owner for that Secret; registry Ingress must not use `cert-manager.io/cluster-issuer` on the Ingress itself)
- Platform UI: `mcp-platform-tls` in `mcp-platform` via `mcp-platform-ui` Ingress
- Bundled HTTPS may create `cert-manager/mcp-runtime-ca`; nodes must trust `tls.crt` for image pulls
- Private CA without ACME: `config/cert-manager/` and omit `--acme-email`

## Registry and image pull (public)

- `MCP_REGISTRY_HOST` — public alias; do not let it override node pull endpoint incorrectly
- HTTPS public: `MCP_REGISTRY_ENDPOINT=registry.<domain>`
- Platform pull secrets: `MCP_PLATFORM_IMAGE_PULL_SECRET` in `mcp-runtime`, `mcp-platform`, and `mcp-observability` when using external auth registries
- Tenant pulls: platform creates `mcp-runtime-registry-pull` on `mcp-workload` SA per team namespace

## OIDC / Google sign-in

Non-test public TLS (`--platform-mode public --with-tls`) requires `GOOGLE_CLIENT_ID` / `MCP_GOOGLE_CLIENT_ID`, or all of `OIDC_ISSUER`, `OIDC_AUDIENCE`, `OIDC_JWKS_URL`. Setup preserves existing values in `mcp-shared-config` on reruns.

## k3s-specific

Use `.codex/skills/production-platform/SKILL.md`, `docs/k3s-deployment-runbook.md`, and `docs/cluster-readiness.md` for scripted deploy, node `registries.yaml`, and multitenancy smoke.

## Troubleshooting cross-links

General failures (ImagePullBackOff, UI 404, cert-manager pods missing): `.codex/skills/cluster-ops/reference.md`.
