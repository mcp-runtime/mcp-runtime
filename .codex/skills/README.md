# MCP Runtime Agent Skills

Last checked: 2026-09-23.

This directory contains MCP Runtime's repo-local Agent Skills. The format is
based on the public Agent Skills guidance:

- Overview: https://agentskills.io/home
- Specification: https://agentskills.io/specification
- Best practices: https://agentskills.io/skill-creation/best-practices
- Evaluating skills: https://agentskills.io/skill-creation/evaluating-skills
- Using scripts: https://agentskills.io/skill-creation/using-scripts

## Layout

Each skill lives in its own directory and must contain `SKILL.md`.

```text
.codex/skills/
  <skill-name>/
    SKILL.md
    agents/openai.yaml
    evals/evals.json
    evals/trigger_queries.json
    references/        # optional, loaded only when needed
    scripts/           # optional, reusable helpers owned by that skill
  scripts/
    validate_skill_evals.py
```

`agents/openai.yaml` is client-specific metadata for OpenAI/Codex surfaces. It
is intentionally outside the core Agent Skills spec, which requires only
`SKILL.md` with `name` and `description` frontmatter.

## Agent Skills Compliance Snapshot

The current repo-local skills follow the core Agent Skills structure. The
generic `design-principles` skill has been consolidated into
`_shared/design-principles.md`; focused security, Kubernetes, and protocol
skills point to it only when their task includes design decisions.

- 18 repo-local skills have a `SKILL.md`.
- Every skill `name` matches its directory name.
- Every skill name uses lowercase letters, numbers, and hyphens only.
- Every `description` is non-empty and below the 1024-character limit.
- Every main `SKILL.md` is below the recommended 500-line ceiling. Prefer a
  short entrypoint that routes to references only for the requested workflow.
- Every skill now has a minimal `evals/evals.json` suite with realistic prompts,
  expected outputs, and objective assertions.
- Every skill also has `evals/trigger_queries.json` with should-trigger and
  should-not-trigger prompts for description tuning.
- `.codex/skills/scripts/validate_skill_evals.py` validates every eval and
  trigger manifest so malformed regression prompts fail fast.
- MCP live transport details are split into
  `mcp-protocol-compliance/references/live-conformance.md`.
- Large UI coverage detail is split into
  `dashboard-browser-qa/references/ui-coverage.md` for progressive disclosure.
- Shared report templates are referenced through relative paths such as
  `../_shared/FINDINGS-TEMPLATE.md`.

Current entrypoint sizes are intentionally not duplicated here because they
change with skill edits. Check them with `wc -l .codex/skills/*/SKILL.md`; keep
the main file under 500 lines and move workflow-specific detail to focused
references with a clear load condition.

Current gap against the fuller Agent Skills evaluation guidance: the skills now
have output and trigger prompt suites plus a local manifest validator, but there
is not yet a model-backed runner that executes each case with-skill versus
baseline and aggregates pass rates, timing, trigger rates, or token costs. Today
we validate skill/eval structure and run MCP Runtime product regression suites
that the skills instruct agents to run.

## Regression Timing

These are historical timings measured on 2026-05-18 from the repo root on an
existing `kind-mcp-runtime` contributor cluster. Skill-eval rows covered the
11 skills present at that time.

| Check | Command or flow | Time observed | What it covers |
|---|---|---:|---|
| Skill frontmatter validation | `quick_validate.py` over all skill dirs | 0.59s | Required `SKILL.md` metadata and basic structure |
| Skill eval JSON validation | `python -m json.tool` over `*/evals/*.json` | 0.72s | Output and trigger eval prompt suites parsed as JSON for the 11 skills present at measurement time |
| Skill eval schema validation | `.codex/skills/scripts/validate_skill_evals.py` | 0.08s | Eval IDs, prompts, assertions, trigger positives, and trigger negatives for the 11 skills present at measurement time |
| OpenAI skill metadata smoke | shell check over `agents/openai.yaml` | 0.13s | Client metadata files exist with display name, short description, and default prompt |
| UI service unit tests | `cd services/ui && go test ./... -race -count=1` | 6.99s | UI server handlers, config, auth/session behavior covered by Go tests |
| CLI golden tests | `go test ./test/golden/... -count=1` | 4.64s | CLI help/output drift that can affect docs and UI-adjacent flows |
| UI static syntax | `node --check services/ui/static/app.js` | 0.30s | Browser bundle JavaScript parses |
| UI skill validation | `quick_validate.py .codex/skills/dashboard-browser-qa` | 0.07s | `dashboard-browser-qa` format after edits |
| UI browser smoke | Playwright against `http://localhost:18080/` | about 4-6 min manual | Signed-out state, tenant login, admin login, tabs, UI-triggered API 200s, console sanity, and mobile overflow |
| Cached QA E2E smoke/governance | `KUBECONFIG="$HOME/.kube/test-mcp-runtime-config" E2E_CACHE_MODE=1 E2E_SCENARIOS=smoke-auth,governance CLUSTER_NAME=mcp-runtime E2E_KEEP_CLUSTER=1 bash test/e2e/qa-e2e.sh` | 630.62s, about 10m31s | Real cluster auth, grant/session governance, gateway policy, CLI flows, ingress, registry auth |

The cached QA E2E command initially failed after 1m17s because a manual
port-forward was already using `localhost:18080`. That was an environment
collision from the QA session, not a product regression. The port-forward was
stopped and the command was rerun.

## How Skill-Based Regression Works Today

The QA skills are run as guided workflows, not as one monolithic test binary.
The agent activates the relevant skill, reads its `SKILL.md`, and then runs the
checks that match the requested scope:

1. `contributor-cluster-setup` creates or recovers the real Kind test-mode cluster.
2. `dashboard-browser-qa` uses browser tooling first, then supports findings with curl,
   static asset checks, UI Go tests, golden tests, and optional cached QA E2E.
3. `security-regression-qa`, `cluster-operations-qa`, `performance-regression-qa`, and
   `mcp-protocol-compliance` cover runtime security, operations, performance, and
   protocol-specific regressions against the same live cluster.
4. Audit skills use `../_shared/FINDINGS-TEMPLATE.md` so findings stay
   comparable across security, supply chain, Kubernetes, and protocol reviews.
5. `release-readiness` composes CI parity, live cluster, browser, security,
   protocol, performance, docs, canary, and deployment evidence for merge,
   ship, canary, and release decisions.
6. `documentation-sync` checks whether code or behavior changes require docs,
   AGENTS.md, runbook, or contributor guidance updates.
7. `.codex/skills/scripts/validate_skill_evals.py` checks that every skill's
   eval and trigger prompt manifests stay structurally usable.

The live QA skills now carry an explicit regression evidence contract: a pass
requires live cluster/browser/API evidence for the relevant surface, including
negative or denied cases where the feature has auth, policy, protocol, or
role-gating behavior. If the live path cannot run, the result is **blocked**,
not passed by substituting static checks.

## CI And Test Coverage Audit

The skills are not one giant test runner. Coverage is routed by change surface,
with CI remaining the source of truth for exact commands. As of this audit, every
CI/test surface has an owning skill:

| CI/test surface | Owning skill path | Coverage status |
|---|---|---|
| `gofmt`, `go vet`, `staticcheck` | `cluster-operations-qa` | CI parity gate for merge/release readiness |
| Root unit tests and `test/integration` envtest | `cluster-operations-qa` | CI parity gate; envtest asset setup included |
| Sentinel service module tests (`services/platform-api`, `services/runtime-api`, `services/analytics-api`, `ingest`, `processor`, `mcp-gateway`, `ui`) | `cluster-operations-qa` plus focused QA skills | CI parity gate plus live rollout checks |
| CLI golden tests | `documentation-sync`, `cluster-operations-qa`, `dashboard-browser-qa` | Docs/help drift and UI-adjacent CLI changes |
| `test/e2e/scenarios_test.sh` selector validation | `cluster-operations-qa` | CI parity gate covers scenario parsing edge cases |
| QA E2E `all` plus cached `smoke-auth,governance` | `contributor-cluster-setup`, `cluster-operations-qa`, `security-regression-qa`, `mcp-protocol-compliance` | Full merge gate and targeted live regression gates |
| Browser UI workflows and responsive checks | `dashboard-browser-qa` | Browser evidence required; curl-only pass is blocked |
| Merge, ship, canary, and release readiness | `release-readiness` | Coordinates focused skills and gives one go/no-go/blocked decision |
| Benchmarks under `test/benchmark` | `cluster-operations-qa`, `performance-regression-qa` | CI benchmark plus live baseline comparison |
| Generated CRD/manifests and Go package docs drift | `cluster-operations-qa`, `documentation-sync` | Exact generator commands and `git diff --exit-code` |
| Gitleaks, gosec, Trivy, dependency review | `change-security-audit`, `platform-security-audit`, `supply-chain-audit` | Security scan selection and supply-chain workflow audit |
| Repository and operator image SBOMs | `supply-chain-audit` | SBOM generation/diff and image scan guidance |
| Docs and website validation/deploy assumptions | `documentation-sync` | Docs build, generated-doc drift, and stale guidance checks |

Audit result: the skills can route all current CI/test surfaces, but they still
do not enumerate every individual unit test case. That is intentional; the skill
contract is to select and run the right suite, then add or update concrete tests
when a behavior gap is found.

For `dashboard-browser-qa`, the current smoke coverage includes role-based navigation,
auth flows, key tabs, network/API evidence, console evidence, static assets,
and responsive sanity. Full UI coverage is broader and lives in
`dashboard-browser-qa/references/ui-coverage.md`; it includes forms, filters, destructive
actions, empty/error states, and public-host defenses. Destructive UI actions
must use temporary `qa-audit-*` objects only.

## Gaps To Close

- Record with-skill versus baseline results, timing, and pass rates in an eval
  workspace, following the Agent Skills evaluation guidance.
- Add a model-backed trigger-eval runner that measures trigger rates over
  `evals/trigger_queries.json` for each skill.
- Script the UI browser smoke so the 4-6 minute manual pass becomes repeatable
  and produces durable evidence.
- Keep future reusable helpers in `scripts/`, make them non-interactive, add
  `--help`, use structured output, and pin dependencies.
