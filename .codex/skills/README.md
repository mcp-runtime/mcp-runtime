# MCP Runtime Agent Skills

Last checked: 2026-09-29.

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
    agents/openai.yaml      # optional
    evals/evals.json
    evals/trigger_queries.json
    references/             # long runbooks; load only for the chosen mode
  scripts/
    validate_skill_evals.py
  _shared/
    design-principles.md
    FINDINGS-TEMPLATE.md
```

Thin `SKILL.md` entrypoints route to `references/` by mode. Prefer
deterministic CI / Staging E2E / `test/e2e` over replaying long playbooks.

## Current skills (7)

| Skill | Role |
|-------|------|
| `contributor-cluster` | Kind+test-mode bring-up + local URLs/keys |
| `access-governance` | Grants, sessions, adapter, MCP JSON-RPC |
| `cluster-ops` | Live Kind ops / security / perf QA + troubleshoot |
| `production-platform` | Public TLS/DNS + k3s / Staging E2E ops |
| `dashboard-browser-qa` | Browser UI QA |
| `mcp-protocol-compliance` | Protocol bumps / spec audits (thin) |
| `security-audit` | PR / platform / k8s / supply-chain review |

## How regression works

1. `contributor-cluster` creates or recovers the Kind test-mode cluster.
2. `cluster-ops` covers live ops, security, perf, and failure debug (modes).
3. `dashboard-browser-qa` requires browser evidence for UI changes.
4. `security-audit` is judgment + scanner routing; prefer CI /
   `pre-release-regression.yaml` for gitleaks/gosec/Trivy/SBOM.
5. Ship gate: CI green, then Pre-release Regression with Staging E2E (`docs/contributor/staging-e2e.md`), then
   the focused skill for the diff. No release-orchestrator skill.
6. Docs drift: update nearest docs/AGENTS; golden/docs CI is deterministic.
7. `scripts/validate_skill_evals.py` validates eval manifests.

## CI ownership (short)

| Surface | Prefer |
|---------|--------|
| Unit / vet / staticcheck / golden / envtest | GitHub CI |
| Path-selected Kind QA E2E | CI + `test/e2e/qa-e2e.sh` |
| Strict-prod install path | Staging E2E |
| Live security suites A/E/F/G/I | `hack/cluster-ops/security-regression.sh` |
| Live perf baseline compare | `hack/cluster-ops/perf-regression.sh` |
| K8s posture inventory / hygiene | `hack/cluster-ops/k8s-hardening-check.sh` |
| Supply-chain scanners | `pre-release-regression.yaml` |
| Live Kind judgment / debug | `cluster-ops` |
| Browser UI | `dashboard-browser-qa` |
| Protocol bump | `mcp-protocol-compliance` |
