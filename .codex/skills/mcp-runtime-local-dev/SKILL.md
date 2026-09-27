---
name: mcp-runtime-local-dev
description: Local Kind contributor endpoints, API keys, test-mode logins, port-forward URLs, and Sentinel auth for MCP Runtime. Use when developing against the `mcp-runtime` Kind cluster with `test-mcp-runtime` context, curling `/api/v1` or MCP paths on localhost:18080, or debugging 401s in test-mode — after cluster bring-up (qa-cluster-bringup).
---

# MCP Runtime — local dev endpoints and auth

## Prerequisites

Cluster running per `.codex/skills/qa-cluster-bringup/SKILL.md` or `docs/contributor/README.md`.
On a fresh workstation, create the isolated test kubeconfig using
`.codex/skills/qa-cluster-bringup/SKILL.md` or
`docs/contributor/local-kind.md`. Keep `KUBECONFIG` on
`$HOME/.kube/test-mcp-runtime-config` for local commands; do not use the
production kubeconfig for test-mode setup.

```bash
kubectl port-forward -n traefik svc/traefik 18080:8000
```

## URLs (test-mode)

| Service | URL |
|---------|-----|
| UI | `http://localhost:18080/` |
| API | `http://localhost:18080/api/v1` |
| Grafana | `http://localhost:18080/grafana` |
| MCP samples | `http://localhost:18080/oauth-example-go-2025-11-25-gateway/mcp`, `…/example-python-2025-11-25-gateway/mcp`, `…/example-rust-2025-11-25-gateway/mcp` |
| Prometheus (debug) | `kubectl port-forward -n mcp-sentinel svc/prometheus 9090:9090` |

PII redaction: `config/ingress/overlays/http` + `pii-redactor@file` — keep off `/api/v1` routes (keys and grant subjects must stay exact).

## API keys

```bash
UI_KEY="$(kubectl get secret mcp-sentinel-secrets -n mcp-sentinel \
  -o jsonpath='{.data.UI_API_KEY}' | base64 -d)"
INGEST_KEY="$(kubectl get secret mcp-sentinel-secrets -n mcp-sentinel \
  -o jsonpath='{.data.INGEST_API_KEYS}' | base64 -d)"
```

Use these variables in local requests; avoid printing their values or adding
them to reports.

- `UI_API_KEY` must appear in both `API_KEYS` and `ADMIN_API_KEYS` for admin curl + browser login
- `INGEST_API_KEYS` for analytics ingest only
- After secret changes: roll platform-api, runtime-api, analytics-api, UI, ingest, and gateway workloads
- `/api/v1` 401 → `./bin/mcp-runtime cluster doctor`

## Test-mode logins

`setup --test-mode` seeds (local only):

- `test@mcpruntime.org` / `test@123`
- `admin@mcpruntime.org` / `admin@123`

Override via `PLATFORM_DEV_*` in `mcp-sentinel-secrets`; roll the split API Deployments after changes.

## Platform admin bootstrap (one-shot)

```bash
kubectl apply -f k8s/21-platform-admin-bootstrap-job.yaml
kubectl wait --for=condition=complete job/mcp-sentinel-platform-admin-bootstrap -n mcp-sentinel --timeout=120s
kubectl patch secret mcp-sentinel-secrets -n mcp-sentinel --type merge -p '{"stringData":{"PLATFORM_ADMIN_PASSWORD":""}}'
```

Clear `PLATFORM_ADMIN_PASSWORD` from steady-state API env after bootstrap.

## Quick commands

```bash
./bin/mcp-runtime status
./bin/mcp-runtime bootstrap          # preflight only
./bin/mcp-runtime cluster doctor
```

Governance traffic and grants: `.codex/skills/mcp-runtime-governance/SKILL.md`.
