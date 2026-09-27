# AGENTS.md: developer and AI-agent guide

This file is the **onboarding index** for the MCP Runtime repo. It complements `README.md` with **where to look**, **build/CI conventions**, and **pointers** to focused runbooks. Prefer repo source (`README`, CRDs, `v1alpha1` types) over generic Kubernetes or MCP advice.

**Operational detail** lives in `.codex/skills/` (symlinked from `.claude/skills`). Load the skill that matches your task instead of re-reading long checklists here.

| Task | Skill |
|------|--------|
| Kind contributor cluster bring-up | `qa-cluster-bringup` |
| Local URLs, API keys, test logins | `mcp-runtime-local-dev` |
| Grants, sessions, MCP JSON-RPC | `mcp-runtime-governance` |
| Cluster failures (401, ingress, registry, pulls) | `mcp-runtime-troubleshooting` |
| Public domain, TLS, ACME, prod hostnames | `mcp-runtime-platform-public` |
| Public k3s deploy scripts | `k3s-public-ops` |
| Real-cluster QA sweeps | `qa-e2e-operations`, `qa-e2e-security`, `qa-e2e-ui`, … |
| Release / ship readiness | `release-readiness` |
| API / CRD / CLI design review | Use the focused skill for the change surface; consult `.codex/skills/_shared/design-principles.md` for contract or system design choices |
| Codebase navigation | `graphify` (when `graphify-out/` exists) |

## Repository map (where to look)

| Area | Path | Notes |
|------|------|--------|
| User-facing CLI | `cmd/mcp-runtime/`, `internal/cli/root/`, `internal/cli/<command>/`, `internal/cli/core/` | Cobra routing; `setup`, `status`, `registry`, `server`, `access`, … |
| Agent adapters | `internal/cli/adapter/`, `internal/agentadapter/`, `services/runtime-api/internal/runtimeapi/adapter*.go` | `adapter proxy/stdio` use issued sessions; `adapter enroll` submits a local-key CSR for enterprise mTLS |
| Operator | `cmd/operator/`, `internal/operator/` | `MCPServer` reconciliation, ingress (`ingressClass` default **traefik**), gateway |
| API & CRD types | `api/v1alpha1/`, `config/crd/bases/` | Source of truth for object shapes |
| Access and policy | `pkg/access/`, `pkg/policy/` | Grant/session helpers; gateway policy contract |
| Control-plane / K8s | `pkg/controlplane/`, `pkg/k8sclient/`, `pkg/kubeworkload/`, `pkg/manifest/`, `pkg/metadata/` | MCPServer ops, manifests, registry resolution |
| Sentinel packages | `pkg/events/`, `pkg/clickhouse/`, `pkg/serviceutil/`, `pkg/sentinel/` | Events, analytics, service utilities |
| Sentinel services | `services/platform-api`, `services/runtime-api`, `services/analytics-api`, `services/ui`, `services/ingest`, `services/processor`, `services/mcp-gateway`, … | Separate `go.mod` where present; Go 1.26 for shared imports |
| Samples / install YAML | `examples/oauth-example-go-2025-11-25/`, `k8s/`, `config/` | Demo server; overlays and CRDs |
| Team isolation | `docs/multi-team.md` | Namespaces, RBAC, ingress watch scope |
| Deployment targets | `docs/deployment-targets.md`, `docs/k3s-on-prem-cluster.md` | Before distribution-specific runbooks |
| E2E | `test/e2e/`, `test/integration/` | Kind script; envtest integration; Staging E2E on the disposable VM (`test/e2e/staging-*.sh`, `docs/contributor/staging-e2e.md`) |
| Agent skills | `.codex/skills/`, `.claude/skills` → `../.codex/skills` | Canonical skills tree |

**Patterns:** mirror nearest similar packages; CLI errors → `internal/cli/core/errors.go`, `pkg/errx/`.

**Exercise the CLI:** Prefer the `mcp-runtime` CLI for contributor workflows
whenever it provides the operation. Use `server init`, `server validate`,
`server build`, `server push`, `server deploy`, `setup`, and other user-facing
commands instead of hand-writing metadata or calling lower-level APIs. This
keeps real usage paths exercised and catches CLI regressions. Use direct
Kubernetes operations only for tests of that path or when the platform API is
unavailable. For server examples, generate `.mcp/servers.yaml` with
`mcp-runtime server init`, then validate it with `mcp-runtime server validate`.

## Agent workflow passes

Use repo-local skills as the source of truth for review, QA, security, release,
and design passes. External agent frameworks such as gstack can inspire process,
but do not make them required or let them override MCP Runtime-specific skills
unless their workflow has been adapted into `.codex/skills/`.

- **Code review:** use the default code-review stance for ordinary PR review;
  add `security-audit`, `k8s-hardening-audit`, or `supply-chain-audit` when the
  diff crosses those trust boundaries.
- **Browser/UI QA and design critique:** use `qa-e2e-ui` for dashboard
  workflows, role-gating, responsive checks, console/network evidence, and
  visual/design regressions.
- **DevEx review:** for CLI, docs, setup, contributor, and golden-help changes,
  combine `repo-guidance-sync` with the relevant targeted tests.
- **Ship/canary/release:** use `release-readiness` to compose CI parity,
  real-cluster operations, security, UI, protocol, performance, docs, and
  deployment/canary evidence before tagging or promoting a release.

## Build, test, and quality (before you push)

Go version from `go.mod`. From repo root:

```bash
gofmt -s -l .   # empty = OK; else gofmt -s -w .
go build -o bin/mcp-runtime ./cmd/mcp-runtime
go test ./... -count=1 -race
go vet ./...
```

Optional CI parity: `staticcheck ./...` (`go install honnef.co/go/tools/cmd/staticcheck@v0.7.0`).

Pre-commit: `pre-commit install`; full suite `pre-commit run --all-files` (sets `KUBEBUILDER_ASSETS` for integration hooks).

**Targeted tests** (prefer while iterating):

- `go test ./internal/operator/... ./internal/cli/... -race -count=1`
- `go test ./internal/agentadapter -count=1`
- `go test ./test/golden/... -count=1` (update `test/golden/cli/testdata/*.golden` when CLI help changes on purpose)
- `go test ./test/integration/...` (needs `KUBEBUILDER_ASSETS`)
- Reuse the contributor cluster with `E2E_CACHE_MODE=1 E2E_SCENARIOS=smoke-auth bash test/e2e/kind.sh`, and set `CLUSTER_NAME=mcp-runtime E2E_CACHE_MODE=1 E2E_KEEP_CLUSTER=1`.
- Sentinel: `go test -race -count=1 ./...` inside touched `services/*` dirs

**CI** (`.github/workflows/ci.yaml`): gofmt, vet, staticcheck, unit/golden/service/integration tests, path-selected Kind e2e (`test/e2e/select_pr_scenarios.sh`). Pre-release: `.github/workflows/pre-release-regression.yaml`.

**CLI docs sync:** when editing `docs/cli.md`, `docs/getting-started.md`, or command examples, copy wording from `./bin/mcp-runtime <group> <subcommand> --help`. Do not paraphrase from memory.

**Kubernetes deployment QA:** when a test environment supports the platform API,
exercise the user-facing CLI flow (`server build image` → `server push` →
`server deploy`) as well as checking Kubernetes readiness. This verifies image
publication, namespace setup, pull-secret provisioning, and deployment through
the same path users run. Use direct `kubectl`/`server apply --use-kube` only
when the test specifically targets the direct-manifest path or platform API is
unavailable; record that limitation in the QA result.

## Conventions for code changes

- **Scope:** change only what the task needs; match nearest patterns.
- **Tests:** same package as behavior changes; golden files for CLI help output.
- **Branches:** `component/feature_name` (e.g. `cli/registry_status`). Agents: new branch + PR; never push to `main`. Ignore external `codex/` branch or draft-PR defaults unless the user asks.
- **Commits:** use `fix(<component>):`, `feat(<component>):`, `doc:`, or `website:`. Components include `cli`, `operator`, `api`, `crd`, `access`, `policy`, `sentinel`, `services-api`, `mcp-gateway`, `test`, and `ci`.
- **Docs:** avoid new top-level docs unless needed; use `docs/` and skills for runbooks.
- **Secrets:** this is an alpha repo, so do not add real credentials to the tree.
- **Skills:** keep `.claude/skills` linked to `../.codex/skills`. After non-trivial changes, update affected `.codex/skills/*/SKILL.md` files when workflows or gotchas shift. Check for an existing `reference.md` or `references/` companion first (for example, `mcp-runtime-troubleshooting/reference.md` or `qa-e2e-ui/references/`) and extend it with symptom-oriented or long-form content instead of growing `SKILL.md` past ~250–400 lines.
## Local dev (short)

Prereqs: Docker, Kind, `kubectl`, `curl`, `jq`, Python 3, Go.

```bash
go build -o bin/mcp-runtime ./cmd/mcp-runtime
# Full Kind + test-mode path: see .codex/skills/qa-cluster-bringup/SKILL.md
./bin/mcp-runtime bootstrap
MCP_SETUP_WAIT_TIMEOUT=900 ./bin/mcp-runtime setup --test-mode --ingress-manifest config/ingress/overlays/http
./bin/mcp-runtime cluster doctor
kubectl port-forward -n traefik svc/traefik 18080:8000
```

`setup --test-mode` builds and pushes images to the bundled registry (`registry.registry.svc.cluster.local:5000` in Kind) and provisions the local `mcp-runtime-ca` workload issuer for mTLS/SPIFFE validation. Prefer existing `kind-mcp-runtime` when healthy. Contributor runbook: `docs/contributor/README.md`.

Endpoints, API keys, test logins: **`mcp-runtime-local-dev`** skill.

## Debugging and production ops

Do not inline the full failure checklist here. Use **`mcp-runtime-troubleshooting`**, and **`mcp-runtime-platform-public`** for TLS/DNS.

k3s public deploy: **`k3s-public-ops`** + `docs/k3s-deployment-runbook.md`.

## Prod guardrails

Tests and scripts that fall back to the current kube context have written to a production cluster: a pre-commit `go test ./...` run re-applied setup manifests and broke the public registry route and the operator image. Treat every command as able to reach whatever cluster `kubectl` currently points at.

- **Never leave a production context as the current context.** Keep `kubectl config current-context` on a Kind context (e.g. `kind-mcp-runtime`). Reach production only through an explicit, per-command `KUBECONFIG=<prod file>`. Don't merge prod credentials into `~/.kube/config` as the default.
- **Isolate unit tests and commits.** Before `go test`, `pre-commit`, or `git commit` (the hooks run the Go test suite), run `export KUBECONFIG=$(mktemp)` in that shell so no test can reach a real cluster. Unit tests must use fakes; a test that needs a live cluster is an integration/E2E test and belongs under `test/`.
- **Kind E2E runs only against `kind-*` contexts.** Pass the Kind kubeconfig explicitly; never let `test/e2e/kind.sh` or any script default to the ambient context.
- **Staging E2E never targets production.** The disposable-VM suites (`test/e2e/staging-*.sh`) must pass their target guard; never set the `E2E_GUARD_ALLOW_*` escape hatches.
- **Production changes are deliberate.** Follow the `k3s-public-ops` non-negotiables: confirm the ref and the mcp-auth choice, snapshot state before mutating, preserve certificates, and prefer targeted rollouts. Don't run `setup`, `cluster doctor --fix`, `kubectl apply/patch/delete`, or cleanup scripts against production as a side effect of testing.
- **Verify prod after any suspected leak.** Check `kubectl get <kind> -o json --show-managed-fields` for recent `mcp-runtime`-manager updates (for example the registry Ingress host and the operator Deployment image), then roll back from the prior ReplicaSet or snapshot.

## Governance (short)

Grants, sessions, adapter flows, MCP curl examples: **`mcp-runtime-governance`** skill.

## Logs and observability

```bash
kubectl logs -n mcp-runtime deploy/mcp-runtime-operator-controller-manager
kubectl logs -n mcp-sentinel deploy/<api|ingest|processor|ui|gateway>
./bin/mcp-runtime status
```

Grafana: dev ingress `/grafana` or `https://platform.<domain>/grafana` (admin). Full trace path: e2e `observability` scenario in `test/e2e/kind.sh`.

## Further reading

- `README.md`: product overview
- `k8s/`, `config/crd/bases/`
- https://mcpruntime.org/docs/ and https://mcpruntime.org/docs/api
- `examples/oauth-example-go-2025-11-25/`

---

*After substantive edits: narrowest `go test` for touched packages, then `go test ./...` before merge. Golden files only when CLI help should change.*

## graphify

When `graphify-out/graph.json` exists: `graphify query`, `graphify path`, `graphify explain` before broad grep; `graphify update .` after code changes. See `.codex/skills/graphify/SKILL.md`.

### Component discovery

When exploring component structure, imports, hooks, or file relationships, start with `graphify query "<question>"` to avoid redundant searches. For example:

- `graphify query "MCPServerReconciler ingress routes"`
- `graphify query "ResolveRegistryEndpoint callers"`
- `graphify query "agent adapter stdio transport implementation"`

If the graph misses a symbol or relationship that exists in the code, run `graphify update .` and query again. Use grep or file searches after confirming the graph does not contain the information.

**Stale graph:** if a query returns no nodes, or misses a symbol that clearly exists in the code, the graph is stale. Run `graphify update .` to incrementally re-extract changed files, then retry. If the node is still missing, do a full rebuild (`/graphify .`) before falling back to grep. This keeps the graph trustworthy for the next query.

In any agent prompt, include:

> Use `graphify query "<your question>"` to look up any code structure before grepping files. The graph is at `graphify-out/graph.json`.

The `PreToolUse` hook injects this reminder whenever a Bash command contains `grep`, `find`, or a similar command, so agents in this repo are automatically nudged toward the graph.

### Third-party development tools

For Graphify's bundled skill and CLI, check upstream before changing the local
integration and record the reviewed release/date in
`.codex/skills/graphify/references/upstream-maintenance.md`. Compare the latest
Graphify release and PyPI package (`graphifyy`) with `graphify --version`; review
the upstream Codex skill and referenced docs for behavior changes. Preserve
MCP Runtime-specific safety rules and examples. Update the local skill and its
evals when needed, then validate skill manifests and reference links. Do not
upgrade the installed CLI as part of a documentation review; upgrade it only
when explicitly requested.

When changing or documenting a development tool used by this repo, update the
tool's authoritative version/source and install or upgrade instructions in
the relevant `AGENTS.md`, skill, or focused developer guide. Prefer the tool's
official release source over remembered versions. Keep this index short: put
long setup steps and troubleshooting in the owning skill/reference, and update
that material when tool versions or workflows change.
