# Contributor Guide

Use this runbook when you need to set up a disposable cluster, change code, rebuild one service,
exercise tenant isolation, or debug the platform UI and MCP request path.

For product concepts, start with [Architecture](../architecture.md). For code
package boundaries, use [Internals](../internals/README.md). This guide focuses
on the day-to-day loop for contributors.

## Contribution Loop

1. Sync the repo and check the worktree before editing.

   ```bash
   git status --short
   ```

2. Build the CLI and run the narrow tests for the area you are touching.

   ```bash
   make deps
   make build
   go test ./internal/cli/... ./internal/operator/... -count=1
   ```

3. Use a disposable Kind cluster for platform, UI, operator, registry, gateway,
   and Sentinel changes.

Start with [Local Kind and Test Mode](local-kind.md).

For a real production-style install on the disposable VM, use
[Staging E2E](staging-e2e.md).

4. Rebuild only the changed service while iterating.

   Use [Service Iteration](service-iteration.md) for API, UI, operator, ingest,
   processor, and MCP proxy rebuilds.

5. Verify with a real MCP route and, when relevant, a tenant-isolation matrix.

   Use [Runtime MCP Testing](runtime-mcp-testing.md).

6. Update docs in the same PR when behavior, commands, defaults, setup flow, UI
   behavior, API shapes, or troubleshooting steps change.

## Where to Work

| Change | Start here | Usual checks |
|---|---|---|
| CLI commands | `cmd/mcp-runtime/`, `internal/cli/` | `go test ./internal/cli/... -count=1`, golden help tests when help text changes |
| Operator reconciliation | `cmd/operator/`, `internal/operator/`, `api/v1alpha1/` | `go test ./internal/operator/... -count=1`, integration tests for CRD behavior |
| Platform API | `services/platform-api`, `services/runtime-api`, `services/analytics-api` | `(cd services/platform-api && go test ./... -count=1)` plus runtime/analytics when touched |
| Platform UI | `services/ui` | `(cd services/ui && go test ./... -count=1)`, `(cd services/ui/frontend && npm run build && npm run test)` |
| MCP gateway | `services/mcp-gateway`, `pkg/policy`, `pkg/access` | service tests plus `E2E_SCENARIOS=governance` or `smoke-auth` when request behavior changes |
| Agent adapters | `internal/agentadapter/`, `internal/cli/adapter/`, `services/runtime-api/internal/runtimeapi/adapter.go` | `go test ./internal/agentadapter ./internal/cli/adapter -count=1` plus `(cd services/runtime-api && go test ./internal/runtimeapi -run TestAdapter -count=1)` when touching the platform-issued session endpoint |
| Docs only | `docs/`, `README.md`, `AGENTS.md` | docs build if available, `rg` for stale terms, `git diff --check` |

## Local Cluster Contract

The contributor Kind flow installs a full local stack:

- CRDs and the operator in `mcp-runtime`
- Split Sentinel API services (`platform-api`, `runtime-api`, `analytics-api`), UI, ingest, processor, and gateway in `mcp-sentinel`
- Traefik ingress on the local gateway path
- A bundled registry for runtime images
- `mcp-servers` as the legacy single-team/example MCP namespace
- `mcp-servers-org` or `mcp-servers-public` only when setup uses
  `--platform-mode org` or `--platform-mode public`
- Optional `mcp-team-<slug>` namespaces for tenant isolation

The default tenant-mode MCP server catalog is authenticated. Anonymous users
should not see MCP servers, and logged-in users see MCPs from team namespaces
they belong to. Use `--platform-mode org` for a shared org catalog, or
`--platform-mode public` for an anonymous public preview catalog.

## Before Opening a PR

Run the smallest checks that prove your change, then widen only when the blast
radius needs it.

```bash
TEST_ISOLATION_KUBECONFIG="$(mktemp)"
(
  export KUBECONFIG="$TEST_ISOLATION_KUBECONFIG"
  gofmt -s -l .
  go vet ./...
  go test ./... -count=1
)
rm -f "$TEST_ISOLATION_KUBECONFIG"
git diff --check
```

Platform API schema regression tests use a disposable local PostgreSQL instance.
From `services/platform-api`, set `MCP_PLATFORM_TEST_POSTGRES_DSN` to its
`postgres://` URL and run `go test ./test/integration -race -count=1` with an
isolated kubeconfig. Only `localhost` or `127.0.0.1` database URLs are accepted;
the test creates and removes its own schema. Without this variable the database
tests skip. CI supplies a pinned PostgreSQL service and runs them automatically.

For changes that affect Kind setup, ingress, registry pushes, gateway policy,
sessions, grants, analytics, or tenant isolation, run the relevant e2e scenario:

```bash
KUBECONFIG="$HOME/.kube/test-mcp-runtime-config" E2E_CACHE_MODE=1 \
  E2E_SCENARIOS=smoke-auth bash test/e2e/qa-e2e.sh
KUBECONFIG="$HOME/.kube/test-mcp-runtime-config" E2E_CACHE_MODE=1 \
  E2E_SCENARIOS=governance bash test/e2e/qa-e2e.sh
KUBECONFIG="$HOME/.kube/test-mcp-runtime-config" E2E_CACHE_MODE=1 \
  E2E_SCENARIOS=multitenancy bash test/e2e/qa-e2e.sh
```

Set `E2E_PLATFORM_MODE=org` or `E2E_PLATFORM_MODE=public` when you need the
Kind setup step to exercise a non-default catalog mode.

Use `E2E_CACHE_MODE=1` only for repeated local debugging. It skips setup when
the platform is ready and reuses mirrored upstream images. If setup must run,
images built from the current checkout are rebuilt so an older `:latest` image
in the local mirror cannot mask source changes; the Kind node's mutable platform
image tags are evicted and the deployments are restarted after setup publishes
them. Omit cache mode for a CI-equivalent fresh cluster.

Image mirroring and local image builds run concurrently during fresh e2e setup.
`E2E_IMAGE_PREP_PARALLELISM` still tunes the default prep concurrency, while
`E2E_IMAGE_MIRROR_PARALLELISM` and `E2E_IMAGE_BUILD_PARALLELISM` can split
pull/push concurrency from heavier Docker builds. CI pins mirror workers to `1`
to avoid Docker/local-registry push contention on shared runners and uses two
build workers; set either value to `1` on constrained machines.
Independent workspace assistant, data utility, and text analysis deployments
also run in parallel; the scenario checks themselves stay ordered because they
share runtime state.
Parallel worker logs are buffered in the e2e workdir under `stage-logs/` and
copied into `E2E_ARTIFACT_DIR` when artifacts are enabled. Live CI output uses
colored `START`, `RUNNING`, `DONE`, and `FAILED` lifecycle lines plus short
stdout/stderr previews, while the full logs stay in the artifact. Major
sequential stages such as setup, cluster doctor, CLI rebuilds, and server
deploys are mirrored under `stage-logs/` in the same artifact.

Every confirmed platform bug should map to a regression check. See the
[Regression Index](regression-index.md); `go test ./test/regression -count=1`
fails when a listed incident has no mapped check or an expired waiver.
