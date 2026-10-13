# AGENTS.md: developer and AI-agent guide

This is the onboarding and task-routing guide for MCP Runtime. Prefer repository
source, CRDs and focused runbooks over generic advice. `hack/README.md` owns the
[automation catalogue](hack/README.md), including prerequisites and execution
boundaries. Repo-local skills live in `.codex/skills/`; `.claude/skills` stays
symlinked to it.

## Prod guardrails

Tests and scripts that fall back to the current kube context have written to a production cluster: a pre-commit `go test ./...` run re-applied setup manifests and broke the public registry route and the operator image. Treat every command as able to reach whatever cluster `kubectl` currently points at.

- **Never leave a production context as the current context.** Keep `kubectl config current-context` on a Kind context (e.g. `kind-mcp-runtime`). Reach production only through an explicit, per-command `KUBECONFIG=<prod file>`. Don't merge prod credentials into `~/.kube/config` as the default.
- **Never run unit tests on a production k3s host.** Core kubeconfig discovery can fall back to `/etc/rancher/k3s/k3s.yaml` even with an empty environment kubeconfig. Use an isolated container/CI runner without system credentials; `hack/ci/go-test.sh` refuses that host shape.
- **Isolate unit tests and commits.** Before `go test`, `pre-commit`, or `git commit` (the hooks run the Go test suite), run `export KUBECONFIG=$(mktemp)` in that shell so no test can reach a real cluster. Unit tests must use fakes; a test that needs a live cluster is an integration/E2E test and belongs under `test/`.
- **QA E2E runs only against `kind-*` contexts.** Pass the Kind kubeconfig explicitly; never let `test/e2e/qa-e2e.sh` or any script default to the ambient context.
- **Staging E2E never targets production.** The disposable-VM suites (`test/e2e/staging-*.sh`) must pass their target guard; never set the `E2E_GUARD_ALLOW_*` escape hatches.
- **Production changes are deliberate.** Follow the `production-platform` non-negotiables: confirm the ref and the mcp-auth choice, snapshot state before mutating, preserve certificates, and prefer targeted rollouts. Don't run `setup`, `cluster doctor --fix`, `kubectl apply/patch/delete`, or cleanup scripts against production as a side effect of testing.
- **Verify prod after any suspected leak.** Check `kubectl get <kind> -o json --show-managed-fields` for recent `mcp-runtime`-manager updates (for example the registry Ingress host and the operator Deployment image), then roll back from the prior ReplicaSet or snapshot.

## Task routing

| Task | Entry point | Detailed guidance |
|---|---|---|
| Developer prerequisites / generation | `hack/dev/deps.sh check`, `hack/dev/dev-setup.sh generate` | `hack/README.md`, `Makefile` |
| Offline automation checks | `hack/ci/check-scripts.sh` | `hack/README.md`; fake tools, no live cluster |
| Unit / lint / contract / image checks | `hack/ci/` and GitHub CI | Build and validation below; `security-audit` for review |
| Kind setup / local URLs and credentials | `contributor-cluster` skill | `docs/contributor/README.md` |
| Live security / performance / posture | `hack/qa/` | `cluster-ops` skill; traffic/load probes require Kind identity |
| Multi-team demo | `hack/qa/multitenancy-test.sh` | `hack/qa/README.md`; loopback or disposable staging only |
| Grants / sessions / MCP calls | `access-governance` skill | `docs/teams-and-access.md` |
| Browser / platform UI QA | `dashboard-browser-qa` skill | Role gating, browser errors and real user workflows |
| Cluster failure diagnosis | `cluster-ops` skill, mode `troubleshoot` | Structured diagnosis before ad-hoc commands |
| Reference update / setup / recovery | `mcp-runtime update` / `setup`; recovery scripts in `hack/deploy/reference/` | `production-platform` skill, `docs/reference-deployment.md` |
| Hosted acceptance | `hack/deploy/acceptance/` | Real agent/grant/certificate session; allowed and denied calls |
| Go documentation hosting | `hack/deploy/pkgsite/` | Deployment workflow and offline rollback tests |
| Protocol spec changes | `mcp-protocol-compliance` skill | Full conformance audit only for protocol work |
| Security / API design review | `security-audit` and focused surface skill | `.codex/skills/_shared/design-principles.md` |
| Merge / release | CI green before merge; Pre-release Regression before tag | Staging E2E on disposable VM; `production-platform` for deployment |

Read a relevant skill before applying its workflow. Prefer existing green CI /
QA E2E evidence for the exact commit; add judgment or focused checks where needed.
Scripts compose supported CLI workflows. Shared `hack/lib/` and deployment
`lib/` files are internal helpers, not independent commands.

## Repository map

| Surface | Source |
|---|---|
| CLI / shared errors | `cmd/mcp-runtime/`, `internal/cli/`, `internal/cli/core/errors.go`, `pkg/errx/` |
| Operator / gateway reconciliation | `cmd/operator/`, `internal/operator/` |
| API / CRDs | `api/v1alpha1/`, `config/crd/bases/` |
| Agents / adapters | `internal/cli/adapter/`, `internal/agentadapter/`, `services/runtime-api/internal/runtimeapi/adapter*.go` |
| Access / policy | `pkg/access/`, `pkg/policy/` |
| Cluster / manifests / registry | `pkg/controlplane/`, `pkg/k8sclient/`, `pkg/kubeworkload/`, `pkg/manifest/`, `pkg/metadata/` |
| Platform / services | `pkg/platformstack/`, `pkg/platforminventory/`, `services/*` (separate modules where present) |
| Samples / installation | `examples/`, `k8s/`, `config/` |
| Integration / E2E | `test/integration/`, `test/e2e/`; `docs/contributor/staging-e2e.md` |
| Automation / release manifest | `hack/README.md`, `hack/release/platformmanifest/` |

Platform namespaces come from `pkg/platforminventory`; see `docs/namespaces.md`.
Public paths are validated by `pkg/publicroutes`; see `docs/self-hosting.md` and
`config/deployments/single-host.env.example`. Preserve platform API, registry,
dashboard asset/session and MCP Auth discovery routes when changing prefixes.

## Product management paths

Use supported CLI and UI workflows for setup, teams/users, registry publishing,
servers, grants, sessions, updates and cleanup. For servers, use `server init` →
`server validate` → `server build` → `server push` → `server deploy`.
A failed product operation is an issue to diagnose, cover and repair; do not
bypass it with Kubernetes mutations, database edits or direct API writes.
Read-only diagnostics are allowed. Direct mutations are allowed only when
explicitly requested or when a test targets that path; record that limitation,
and do not count it as CLI/UI validation.

Account creation with `team user create` / UI is atomic with membership creation.
For existing accounts use `team user add` / **Add existing user**, preserving
passwords. Verify validation, conflicts and rollback when changing these paths.
Adapters use issued sessions by default; `--auth-mode header` supplies local
credentials without enrollment. Enterprise mTLS uses `adapter enroll` with a
local-key CSR.

## Build and validation

Use the Go version in `go.mod`. Go builds/tests require available dependencies.
From the repository root, on an isolated non-production runner:

```bash
hack/dev/deps.sh check
gofmt -s -l .
go build -o bin/mcp-runtime ./cmd/mcp-runtime
hack/ci/go-test.sh . -count=1 -race ./...
go vet ./...
```

Use targeted tests while iterating, then the full root suite before merge.
Run tests inside touched service modules too. CLI help changes need golden
updates under `test/golden/cli/testdata/`. Integration tests need envtest assets;
pre-commit handles `KUBEBUILDER_ASSETS` for its integration hooks.
Staticcheck installation/version guidance lives in `hack/ci/staticcheck.sh`.
Use `pre-commit install` and `pre-commit run --all-files` on an isolated runner.

For automation changes run `hack/ci/check-scripts.sh` (Python 3.9+ and
ShellCheck). It covers fake-tool/HTTP behavior, pkgsite rollback, Bash syntax and
ShellCheck. It can run offline on a production host because it does not use
cluster credentials; the Go test wrapper refuses hosts with system k3s config.

CI owns unit/golden/service/integration tests, vet, staticcheck, generated-file
checks and path-selected Kind QA E2E. Reuse an existing healthy contributor
cluster for focused E2E; pass its explicit kubeconfig. Staging E2E is the
strict-production-shaped suite on the disposable VM and runs from Pre-release
Regression or manual dispatch. Never target production or enable guard escapes.
Hosted acceptance must test allowed and denied calls through a real
agent/grant/certificate session before updating the live-version badge.

## Change conventions and documentation

- Match nearby patterns and keep scope focused. Branches use `component/feature_name`;
  agents create a branch and PR, never push directly to `main`.
- Use `fix(component):`, `feat(component):`, `doc:` or `website:` commits.
  Keep credentials, tokens, deployment profiles and backup material out of git/logs.
- Every PR states `Changelog: updated` or `Changelog: not needed — <reason>`.
- `hack/README.md` owns the script catalogue. Each script needs a documented purpose,
  prerequisites, execution boundary, inputs and verification. Update all callers,
  workflows, tests, docs and skills together when moving automation; do not add
  executable scripts at `hack/` root or old-path wrappers.
- Update affected `.codex/skills/*/SKILL.md` and references when workflows change;
  put long procedures in `references/` instead of enlarging the entrypoint.
- Update nearest user docs when behavior changes. Copy command wording from the
  built CLI's `--help`; preserve existing linked heading anchors.

`docs/mkdocs.yml` owns navigation: Getting Started → Server and Client Guides →
Deployment and Operations → Concepts and Architecture → CLI and API Reference →
Development and Testing → Implementation Details. Each section starts with an
audience/prerequisites/reading-order overview. Keep `docs/README.md` and
`docs/llms.txt` aligned. Release examples use `mcp-runtime` on PATH; source builds
use `./bin/mcp-runtime`.

## Release policy before the first external customer

Current deployment phase (confirmed 2026-10-05): the only customer deployment
is our own hosted reference platform at `platform.mcpruntime.org`. Apply this
policy to all releases until the project has its first external customer
deployment and this section is updated.

- Breaking changes are allowed. Target the current architecture and contracts;
  do not add legacy compatibility layers, dual-version APIs or schemas,
  backfill frameworks, or in-place upgrade paths just to support earlier
  releases of our own platform.
- Ordinary releases are delivered with `mcp-runtime update` (images, CRDs and the
  ServiceAccounts, RBAC, ConfigMaps and Deployment wiring a release declares in
  `config/release/wiring.yaml`). A breaking reset follows **backup → fresh setup
  → restore required data → verify** on the reference platform. Document that
  procedure instead of designing a general customer migration system.
- Release instructions must identify the exact revision, install configuration,
  backup contents and location, clean setup steps, restore steps, and recovery
  procedure. Include platform databases, registry images, required Secrets and
  certificates, and the separately hosted identity provider's data and TLS
  state where applicable. Keep backup material and credentials out of git and
  logs.
- Rehearse fresh installation and recovery on disposable infrastructure before
  resetting the reference platform. Check that required data can be restored
  into the new model; document any manual recovery or intentional data reset
  explicitly. A backup alone is not proof of recoverability.
- Verify login, image publishing and pulls, server deployment, agent enrollment,
  grants and sessions, allowed/denied tool calls, and audit/observability after
  setup and recovery. Existing CI, security, and Pre-release Regression gates
  still apply.
- Record breaking contracts, configuration changes, and fresh-install/recovery
  requirements in the changelog and release notes. Breaking `0.x` releases use
  a new minor version.

When the first external customer is onboarded, update this policy and define
upgrade, compatibility, and data-migration commitments for that customer's
deployed version before the next breaking release. This policy sets release
planning and implementation scope; it does not authorize an unrequested reset
of the hosted platform. Follow the existing production guardrails for actual
deployment work.

## Changelog maintenance

`CHANGELOG.md` follows [Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/)
and [Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html)
(reviewed 2026-09-29). Write concise, curated entries describing effects on
users/operators, rather than copying commit logs. Add entries under `Unreleased`
using only applicable `Added`, `Changed`, `Deprecated`, `Removed`, `Fixed`, or
`Security` headings. Link the relevant issue/PR and explain upgrade or fresh
setup/recovery requirements according to the release policy above. Features,
behavior/config/default changes, removals, user-visible fixes, and security
fixes need entries. Internal refactors,
test-only work, and spelling/formatting changes can skip an entry when the PR
explains why; operational CI/setup fixes need one when they affect contributors.

Before publishing a release, move its `Unreleased` entries into a dated
`[X.Y.Z] - YYYY-MM-DD` section, retain an empty `Unreleased` section, and update
version comparison links. Keep releases newest first and omit empty categories.
Use the changelog to prepare GitHub release notes. While versions are `0.x`,
put breaking changes in a new minor version and document the required release
deployment procedure.
Correct factual errors in released notes with traceable context; do not rewrite
published tag contents or invent historical entries without source evidence.

## Diagnosis and maintainability

For production incidents start with Grafana at `https://platform.mcpruntime.org/grafana`.
Use private credentials from `~/.mcpruntime/infra.env` without displaying them;
correlate metrics, logs, traces and client request IDs for the incident window.
See `docs/reference-deployment.md#production-observability-and-debugging`.
`mcp-runtime status` is an authenticated API check, not a Kubernetes/server
inventory; use `server list`, `cluster status`, `ops status` or
`cluster diagnostics` for those surfaces. Setup includes an operational smoke gate.

For concrete maintainability/debuggability findings, search existing issues first.
Create an actionable issue if none exists and attach it to organization project 1,
[Maintainability and Debuggability Improvement](https://github.com/orgs/mcp-runtime/projects/1).
Include redacted evidence, affected components, scope, acceptance checks and
missing instrumentation; never include private payloads or credentials.

## Code navigation and third-party tools

When `graphify-out/graph.json` exists, start code-structure exploration with
`graphify query`, `path` or `explain`. Refresh stale results with
`graphify update .`; rebuild with `/graphify .` if still missing, then use `rg`
when the graph cannot answer. Refresh the graph after code changes. Include the
repository graph path and this instruction in any delegated agent prompt.

Before upgrading Graphify, check its official release and PyPI `graphifyy`
against the installed version and record the reviewed source/date in contributor
notes. Do not upgrade it during documentation work unless requested. When
changing another development tool, keep its authoritative source/version and
installation guidance in the focused guide or this file.
