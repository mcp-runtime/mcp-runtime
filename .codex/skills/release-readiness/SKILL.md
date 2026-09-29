---
name: release-readiness
description: Compose MCP Runtime pre-merge, ship, canary, and release readiness across CI parity, live Kind operations, security, UI/browser QA, protocol compliance, performance, docs, and deployment evidence. Use when asked if a branch can ship, to prepare a release, run a canary, produce release evidence, or coordinate final review before merge/tag/deploy.
---

# Release Readiness

## Overview

Use this skill when the user asks whether a branch can merge, ship, canary, or
release. It is a coordinator, not a replacement for the focused skills. Load
the matching skill for each surface you exercise and report one consolidated
go/no-go result.

This workflow is intentionally repo-native. External agent workflows can inspire
the order of review passes, but MCP Runtime release evidence must come from the
repo's own commands, skills, CI, cluster state, docs, and security model.

## Step 1 - Define Release Scope

Inspect the current state before choosing checks:

```bash
git status --short
git diff --stat origin/main...HEAD
git diff --name-only origin/main...HEAD
```

If the working tree has unrelated user changes, do not include them in a release
commit or claim they were validated. If the user wants a tag or deploy, confirm
the target version, branch, and environment before any destructive or external
action.

Classify the request:

- **merge-readiness**: branch can be reviewed and merged.
- **release-readiness**: version/tag candidate can be cut.
- **canary-readiness**: candidate can be promoted to a limited environment.
- **post-release-check**: verify a just-shipped build and document evidence.

## Step 2 - Route By Change Surface

Choose every applicable focused skill:

| Surface | Required skill |
|---|---|
| Operator, setup, registry, ingress, CLI, CRDs, service rollout | `cluster-operations-qa` |
| Auth, grants, sessions, gateway policy, audit, TLS, secrets | `change-security-audit` and often `security-regression-qa` |
| Platform-wide release security signoff | `platform-security-audit` |
| Kubernetes RBAC, PSS, NetworkPolicy, manifests | `kubernetes-hardening-audit` |
| Images, SBOMs, GitHub Actions, dependency or base-image changes | `supply-chain-audit` |
| Sentinel dashboard, UI-visible API behavior, login/admin/tenant flows | `dashboard-browser-qa` |
| MCP protocol behavior or upstream spec compatibility | `mcp-protocol-compliance` |
| Gateway/API/operator hot paths or "feels slower" risk | `performance-regression-qa` |
| Public k3s, TLS, ACME, production hostnames | `production-operations` or `public-platform-configuration` |
| Full strict-prod install path (setup, public TLS, registry auth, tenants) | `production-operations` Staging E2E workflows on the disposable VM (`docs/contributor/staging-e2e.md`) |
| Docs, CLI examples, contributor guidance, release notes | `documentation-sync` |

Do not substitute a generic review pass for a domain skill when one exists.

## Step 3 - Minimum Gates

For non-doc code changes, release readiness requires:

```bash
TEST_ISOLATION_KUBECONFIG="$(mktemp)"
(
  export KUBECONFIG="$TEST_ISOLATION_KUBECONFIG"
  gofmt -s -l .
  go vet ./...
  go test ./... -count=1
  go test ./test/golden/... -count=1
)
rm -f "$TEST_ISOLATION_KUBECONFIG"
```

For merge or release candidates, prefer the CI-parity gate from
`cluster-operations-qa` Step 3, which adds race tests, service-module tests,
benchmarks, scenario selector validation, envtest integration, generated-file
drift checks, and docs-generated reference drift checks.

For release candidates, run or verify CI has run:

```bash
KUBECONFIG="$HOME/.kube/test-mcp-runtime-config" \
  E2E_SCENARIOS=all bash test/e2e/qa-e2e.sh
```

When reusing a contributor cluster for a targeted canary, record the cached
traffic gate:

```bash
KUBECONFIG="$HOME/.kube/test-mcp-runtime-config" \
  E2E_CACHE_MODE=1 E2E_KEEP_CLUSTER=1 CLUSTER_NAME=mcp-runtime \
  E2E_SCENARIOS=smoke-auth,governance bash test/e2e/qa-e2e.sh
```

If a live cluster or browser gate cannot run, mark that surface **blocked**.
Do not call the release green by replacing live evidence with static checks.

## Step 4 - Canary And Deploy Evidence

For a canary or deploy-readiness request, collect:

- exact image tags, binary version output, commit SHA, and target environment
- rollout status for each changed deployment
- `./bin/mcp-runtime cluster doctor` and platform status output
- smoke-auth/governance traffic evidence through the ingress path
- browser evidence for changed UI workflows
- rollback plan: previous image/tag, command, and expected health signal

For public k3s or TLS/ACME work, switch to `production-operations` or
`public-platform-configuration` and use their runbooks for live validation.

## Step 5 - Docs And Release Notes

Assess `CHANGELOG.md` for every PR and record whether it was updated or why an
entry is unnecessary. Follow the changelog maintenance rules in `AGENTS.md`.
Before publishing a release, promote its `Unreleased` entries to the dated
version section and update comparison links. Prepare GitHub release notes from
those curated entries, including compatibility and migration guidance.

Run `documentation-sync` when the change affects CLI help, commands, setup,
configuration, docs, API/CRD shape, deployment behavior, agent guidance, or
operator/debug workflows.

Release notes should be evidence-based:

- user-visible change
- compatibility or migration notes
- validation commands or CI runs
- known limitations or follow-up issues

Do not invent roadmap promises or production support claims not backed by code
or docs.

## Step 6 - Final Report

Lead with one of:

- **Go**: all required gates passed and blockers are resolved.
- **No-go**: at least one blocking finding or failed gate remains.
- **Blocked**: required evidence could not be collected.

Then include:

- scope: branch/commit/range, release mode, changed surfaces
- gates run: command, result, and important evidence
- focused skills used and why
- findings ordered by severity with file/line references where applicable
- skipped checks and whether they are acceptable for this release mode
- exact next action: merge, tag, canary, deploy, rollback, or fix list
