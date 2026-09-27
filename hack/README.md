# hack/: repository automation scripts

Scripts are grouped by purpose. See the layout below.

## Layout

```
hack/
  boilerplate.go.txt          # Go codegen header template
  ci/                         # CI and pre-commit helpers
    go-test.sh
    smoke-ci.sh
    staticcheck.sh
  dev/                        # Workstation / contributor setup
    deps.sh
    dev-setup.sh
  deploy/
    mcpruntime-org/           # Public k3s cluster (mcpruntime.org)
      backup.sh              # Full online K3s/API/local-path recovery bundle
      setup.sh                # Full platform install
      clean.sh                # Wipe MCP Runtime namespaces (with backup)
      restore.sh              # Re-apply TLS/OIDC/bootstrap backups
      rollout.sh              # Build/push Sentinel API+UI only
      multitenancy-test.sh    # Platform API multi-tenant smoke test
      lib/                    # Shared bash helpers (env, backup, clean, registry)
```

## mcpruntime.org k3s deployment

Configure `config/deployments/mcpruntime-org.env` (copy from `.example`), then:

```bash
hack/deploy/mcpruntime-org/backup.sh --setup
hack/deploy/mcpruntime-org/setup.sh
hack/deploy/mcpruntime-org/clean.sh --yes --wait
hack/deploy/mcpruntime-org/rollout.sh
PLATFORM_URL=... MCP_URL=... REGISTRY_HOST=... hack/deploy/mcpruntime-org/multitenancy-test.sh
```

Use `backup.sh --full --online-copy` before destructive cleanup or for full
node recovery; see `docs/k3s-deployment-runbook.md` for coverage and limits.

See `docs/k3s-deployment-runbook.md` for the full runbook.

## CI / dev

```bash
hack/ci/staticcheck.sh ./...
hack/ci/smoke-ci.sh
hack/dev/deps.sh check
hack/dev/dev-setup.sh all
```
