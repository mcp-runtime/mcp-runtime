# mcpruntime.org k3s deployment scripts

Env file: `config/deployments/mcpruntime-org.env` (see `.example`).

| Script | Purpose |
|--------|---------|
| `setup.sh` | Build CLI, run `mcp-runtime setup`, auto-restore platform backup when present |
| `backup.sh` | Save a setup-focused object/Secret snapshot (`--setup`) or a full online K3s/local-path bundle (`--full --online-copy`) |
| `clean.sh --yes` | Backup TLS/OIDC/bootstrap secrets, delete MCP Runtime namespaces |
| `restore.sh` | Re-apply platform-runtime backup (TLS, certs, config) |
| `rollout.sh` | Build/push platform API+UI images; optionally update mcp-auth from published Docker Hub or selected local source; roll deployments |
| `multitenancy-test.sh` | End-to-end multi-tenant demo via platform API only |

Shared helpers live in `lib/`:

| Library | Responsibility |
|---------|----------------|
| `lib/env.sh` | Load deployment env, kubeconfig, registry host |
| `lib/backup.sh` | Snapshot and restore platform state; export the full Kubernetes resource inventory |
| `lib/clean.sh` | Namespace selection and cluster-scoped CR cleanup |
| `lib/registry.sh` | Registry pull secret and internal skopeo push via port-forward |

Runbook: `docs/reference-deployment.md`

Run production builds with the workstation's selected Docker daemon and set
`MCP_IMAGE_PLATFORM=linux/amd64` for the current k3s nodes. Set
`MCP_REGISTRY_PUSH_MODE=public` to push unique tags directly to
`registry.mcpruntime.org/`. The optional mcp-auth update pulls the published
Docker Hub image by default; building source requires an explicitly selected
clean ref and `MCP_AUTH_IMAGE_SOURCE=local`. Follow the runbook for the full
release and user-verification flow.
