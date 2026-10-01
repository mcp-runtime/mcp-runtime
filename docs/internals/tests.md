# Tests

This page maps the main test suites to the code they protect. Prefer the
narrowest suite while iterating, then broaden before pushing when the change
touches shared contracts.

## Fast Package Tests

| Surface | Tests | Use when |
|---|---|---|
| API types | `go test ./api/v1alpha1/... -count=1` | CRD structs, validation, deepcopy, scheme registration |
| CLI | `go test ./internal/cli/... -count=1` | command routing, command behavior, setup planning, registry helpers, doctor checks |
| Operator | `go test ./internal/operator/... -race -count=1` | reconciliation defaults, owned resources, status, registry/image behavior |
| Metadata | `go test ./pkg/metadata/... -count=1` | `.mcp` loading, host resolution, manifest generation |
| Sentinel services | `go test -race -count=1 ./...` in each service module | API, UI, ingest, processor, proxy service logic |

## Golden CLI Tests

CLI help and stable output snapshots live under `test/golden/cli`.

Run:

```bash
go test ./test/golden/... -count=1
```

Update golden files only when the CLI output intentionally changes. For help
text, verify the actual command output from `./bin/mcp-runtime <command> --help`
before editing snapshots.

## Integration Tests

Integration tests live under `test/integration` and use envtest assets. They are
the right place for Kubernetes API behavior that fake clients cannot represent.

Run:

```bash
go test ./test/integration/... -count=1
```

If envtest assets are missing, follow the setup in `Makefile.operator` or the CI
workflow.

## QA E2E

`test/e2e/qa-e2e.sh` creates a Kind cluster, configures a local registry mirror,
builds and publishes runtime images, runs `setup --test-mode`, deploys example
servers, exercises MCP requests, and verifies governance/observability paths.

For the component-level request paths behind each scenario, see [Request Flows](request-flows.md).

Useful local runs:

```bash
E2E_SCENARIOS=smoke-auth bash test/e2e/qa-e2e.sh
E2E_SCENARIOS=api-platform bash test/e2e/qa-e2e.sh
E2E_SCENARIOS=ui-auth bash test/e2e/qa-e2e.sh
E2E_SCENARIOS=adapter-proxy bash test/e2e/qa-e2e.sh
E2E_SCENARIOS=cli-platform bash test/e2e/qa-e2e.sh
E2E_SCENARIOS=all MCP_DEPLOYMENT_TIMEOUT=900s bash test/e2e/qa-e2e.sh
E2E_DEEP_REQUEST_FLOWS=1 E2E_SCENARIOS=all bash test/e2e/qa-e2e.sh
E2E_KEEP_CLUSTER=1 E2E_SCENARIOS=smoke-auth bash test/e2e/qa-e2e.sh
E2E_CACHE_MODE=1 E2E_SCENARIOS=smoke-auth bash test/e2e/qa-e2e.sh
```

Supported scenario selectors are `all`, `smoke-auth`, `governance`, `trust`,
`oauth`, `observability`, `multitenancy`, `api-platform`, `ui-auth`,
`adapter-proxy`, and `cli-platform`. The targeted PR-only selectors map to
request-path surfaces: `api-platform` covers platform API auth, user, registry,
team, runtime, deployment, and admin routes; `ui-auth` covers direct UI and
gateway cookie auth/static routes; `adapter-proxy` covers platform-issued
adapter sessions plus the local adapter proxy MCP path; `cli-platform` covers
the platform-backed CLI request flow.

`E2E_CACHE_MODE=1` is for repeated local debugging. It implies
`E2E_KEEP_CLUSTER=1`, reuses the existing Kind cluster and local registry when
present, skips platform setup if the core platform is already ready, and reuses
image tags already published to the local registry.

### Content-hash GHCR image cache

CI QA E2E and disposable-VM Staging E2E can skip rebuilding unchanged platform
images by pulling content-hash tags from GitHub Container Registry. QA E2E
publishes cache misses; Staging E2E uses the same tags without a write credential:

| Env | Meaning |
|-----|---------|
| `E2E_IMAGE_CACHE=1` | Enable pull-or-build in QA prebuild and setup; enabled by default in both E2E runners |
| `MCP_SETUP_IMAGE_CACHE=1` | Same cache inside `setup` image publish, including Staging E2E's strict production setup |
| `E2E_GHCR_PUSH=1` | After a cache miss build, push `ghcr.io/<owner>/mcp-runtime/<component>:<hash>-<architecture>` |
| `E2E_IMAGE_CACHE_REGISTRY` | Override registry prefix (default `ghcr.io/<owner>/mcp-runtime`) |
| `E2E_IMAGE_CACHE=0` | Force-disable even if setup cache is set |

Each hash covers the component's Dockerfile, `.dockerignore`, module files,
and the Go files in its transitive import graph for the target architecture.
The UI hash also covers its frontend files. An edit to an unused package
does not rebuild that service; an edit to an imported shared package rebuilds
every service that imports it (see `internal/cli/setup/platform/imagecache`).
Cluster tags stay `:latest` in
test-mode; only GHCR uses the hash tag. Forks without `packages: write` to the
parent org can pull existing cache entries but cannot publish misses. Bust the cache with
`E2E_IMAGE_CACHE=0` or by changing a hashed input file.

`E2E_DEEP_REQUEST_FLOWS=1` is for pre-release sweeps, not normal PR feedback.
It requires `E2E_SCENARIOS=all` and adds broader CLI help, adapter proxy,
platform API, UI auth, registry authz, team, deployment, and item-level runtime
request flows.

Image mirroring and local runtime/Sentinel image builds run with bounded
parallelism. `E2E_IMAGE_PREP_PARALLELISM=<n>` tunes the shared prep default,
`E2E_IMAGE_MIRROR_PARALLELISM=<n>` tunes pull/push mirroring, and
`E2E_IMAGE_BUILD_PARALLELISM=<n>` tunes local Docker builds. CI sets mirroring
to one worker to avoid Docker/local-registry push contention on shared runners
and builds to two workers because builds are heavier on runner CPU, memory, and
Docker. CI also sets `MCP_POLICY_WAIT_TRIES=45` so stuck gateway-policy waits
fail sooner than the local default of 90, and `MCP_HTTP_TIMEOUT=15` so Traefik
504/502 retries on the ingress path recover faster than the local 30s default.

QA E2E deploys a single primary MCP server (`policy-mcp-server`) for most PR
paths. That primary omits `spec.auth` so adapter-certificate and grant/deny
checks exercise cert-first identity; do not apply empty `auth: {}` (that turns
OAuth on via the derived issuer and changes expected `401` reason codes). CI
sets `E2E_MAX_MCP_SERVERS=2` so multitenancy can reuse that server as
tenant-a and deploy only `mt-tenant-b` as the second workload. The older
data-utility, text-analysis, and workspace-assistant sample deploys are not used
anymore; ingress checks exercise multiple tools on the primary server instead.
When `MCP_ADAPTER_CERTIFICATES=true`, adapter and DIRECT/header-proxy probes use
Traefik `websecure` at `https://127.0.0.1:18443/...` (insecure TLS skip for the
local default cert); plain Ingress and platform routes stay on
`http://127.0.0.1:18080/...`.
Set `E2E_MAX_MCP_SERVERS=0` locally for unlimited servers during full
`E2E_SCENARIOS=all` pre-release runs.

**What runs in parallel (safe):** local Docker image builds and registry mirroring
(`E2E_IMAGE_BUILD_PARALLELISM`, `E2E_IMAGE_MIRROR_PARALLELISM`); read-only grant
rule ConfigMap polls (`E2E_GRANT_RULE_PARALLELISM`); and api-platform plus ui-auth
HTTP scripts when `E2E_HTTP_FLOW_PARALLELISM>1` (workers clear the inherited EXIT
trap so they do not delete the shared kubeconfig).

**What stays sequential (shared MCP state):** every `run_parallel_mcp_tool_checks`
batch. Those calls share one session URL, header proxy, and gateway reload window,
so trust/ingress tool waits always run one at a time regardless of other
parallelism knobs.

Parallel worker output is buffered under `stage-logs/` in the e2e workdir and
copied into `E2E_ARTIFACT_DIR` when artifacts are enabled. Live output prints
colored `START`, `RUNNING`, `DONE`, and `FAILED` lifecycle lines plus short
stdout/stderr previews; tune those previews with `E2E_LOG_PREVIEW_LINES` and
`E2E_LOG_FAILURE_LINES`. Major sequential stages such as local registry startup,
Kind cluster creation, CLI rebuilds, setup, cluster doctor, and MCP server
deploys are also mirrored to `stage-logs/` in the same artifact.

E2E output uses ANSI color for interactive terminals and GitHub Actions by
default. Set `E2E_COLOR=always` or `E2E_COLOR=never` to override
auto-detection; `NO_COLOR` disables color.

QA E2E traffic uses deterministic curl-based MCP requests. The previous
real-client agent prompts are disabled so CI and local runs do not consume
OpenAI or Anthropic tokens while validating gateway policy, auth, audit, and
observability paths.

The `observability` scenario validates the trace backend through both direct
Tempo and Grafana's Tempo datasource. It must find a single request trace that
contains the gateway service, `mcp-sentinel-ingest`, `mcp-sentinel-processor`,
and the `kafka.produce`, `kafka.consume`, `clickhouse.insert_event`, and
`clickhouse.insert_batch` spans.

Normal PRs run short QA E2E with `smoke-auth` as the baseline, then
`.github/workflows/ci.yaml` calls `test/e2e/select_pr_scenarios.sh` to add
targeted scenarios based on the changed files. API, UI, adapter, CLI, OAuth,
observability, and multi-tenancy changes get the matching request-path mode;
shared or unknown code paths fall back to `all` so CI stays conservative. The
manual Pre-release Regression workflow runs full QA E2E with
`E2E_SCENARIOS=all` and `E2E_DEEP_REQUEST_FLOWS=1` across tenant, org, and
public platform modes, plus a tenant cache-mode replay when requested.

The script writes artifacts when `E2E_ARTIFACT_DIR` is set. In CI, those
artifacts are uploaded from `.e2e-artifacts/qa`.

## CI Coverage

The main CI workflow runs:

- formatting check
- `go vet`
- `staticcheck`
- unit and integration tests
- golden CLI tests
- service module tests
- generated file drift
- repository SBOM generation
- path-selected short QA E2E on PRs and manual CI runs

Relevant pushes to `main` after merge run Staging E2E on the disposable VM;
the CI QA E2E job is skipped on main pushes. QA E2E still uses Kind for its
local test-mode cluster.

The manual Pre-release Regression workflow adds full QA E2E in tenant, org,
and public platform modes, cache replay, benchmarks, repository/operator-image
SBOMs, gosec, Gitleaks, and Trivy scans. Security workflows add pinned gosec,
Trivy repository/image scans with SARIF upload, pinned Gitleaks secret
scanning, operator-image SBOM artifacts, and pull-request dependency review.

Use the [request-flow matrix](request-flows.md#use-case-matrix) to confirm a pre-release run exercises every user-facing control-plane, runtime, registry, policy, analytics, and tenant flow.

## Pre-commit Hooks

`.pre-commit-config.yaml` mirrors the local contributor gates: Go formatting,
`staticcheck`, `go vet`, targeted Go tests, generated-file drift checks, and a
pinned Gitleaks hook for staged secret detection. Install with
`pre-commit install`; run `pre-commit run --all-files` before pushing when you
want the full local hook suite. The full suite includes integration hooks that
install envtest assets and set `KUBEBUILDER_ASSETS`.

## Choosing Coverage

| Change | Minimum local check |
|---|---|
| Cobra help or flags | `go test ./internal/cli/... ./test/golden/... -count=1` |
| setup, registry, or cluster doctor | targeted `internal/cli` tests plus a local Kind or k3s smoke when behavior affects pulls |
| CRD schema | `make -f Makefile.operator generate manifests`, API tests, operator tests |
| reconciliation | operator tests plus integration or e2e when resource ownership changes |
| gateway policy | service tests plus the governance e2e scenario |
| docs-only | `git diff --check`; MkDocs build when available |
