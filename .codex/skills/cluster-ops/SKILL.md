---
name: cluster-ops
description: Live MCP Runtime cluster operations — Kind ops QA, security regression, performance baselines, and Kind/k3s troubleshooting (ingress, registry, auth, pods, UI). Use when verifying a change on a live cluster, investigating 401/404/ImagePullBackOff/TLS failures, or checking gateway latency. Assumes contributor-cluster for Kind work. Prefer Staging E2E and CI for deterministic ship gates.
---

# Cluster Ops

One skill for live cluster validation and failure debug. Prefer **deterministic**
gates first: CI, Staging E2E (`docs/contributor/staging-e2e.md`), and existing
`test/e2e/qa-e2e.sh` scenarios. Use this skill when you need a live Kind
sweep, a perf compare, or a failure playbook.

## Modes

Pick every mode the change warrants; state them in the report.

| Mode | When | Load |
|------|------|------|
| `ops` | Operator, CLI, setup, registry, ingress, rollouts | [references/ops-qa.md](references/ops-qa.md) |
| `security` | Auth, grants, deny paths, headers, live secret scan | [references/security-regression.md](references/security-regression.md) |
| `perf` | Gateway/API/operator hot paths or "feels slower" | [references/performance.md](references/performance.md) |
| `troubleshoot` | Cluster/ingress/registry/auth/pod/UI failures | [references/troubleshooting.md](references/troubleshooting.md) + [references/troubleshooting-checklist.md](references/troubleshooting-checklist.md) |

## Preconditions (Kind)

```bash
TEST_KUBECONFIG="${TEST_KUBECONFIG:-$HOME/.kube/test-mcp-runtime-config}"
export KUBECONFIG="$TEST_KUBECONFIG"
# contributor-cluster must have produced a healthy test-mcp-runtime context
```

Do **not** re-run the full local CI suite as a substitute for GitHub CI. CI is
the deterministic unit/integration gate; this skill owns live cluster evidence.

`mcp-runtime status` is a quick authenticated platform API check with a
five-second timeout. Use `cluster status`, `sentinel status`, and `server list`
for Kubernetes workload health and server inventories.

## Deterministic preference

| Surface | Prefer over long agent playbooks |
|---------|-----------------------------------|
| Ship / strict-prod install | Staging E2E workflows |
| PR path-selected Kind smoke | `test/e2e/qa-e2e.sh` + CI QA E2E |
| Auth/governance traffic | QA E2E `smoke-auth,governance` scenarios |
| Live security suites A/E/F/G/I | `bash hack/cluster-ops/security-regression.sh` |
| Live perf S1–S4 + baseline | `bash hack/cluster-ops/perf-regression.sh` |
| K8s posture inventory / hygiene | `bash hack/cluster-ops/k8s-hardening-check.sh` |
| Authz matrix probe | `bash hack/validate-authz-matrix.sh` |
| Supply-chain scanners | `pre-release-regression.yaml` / CI |

Run the scripts first for modes `security`, `perf`, and k8s hygiene. Load the
matching `references/*.md` only for judgment, adapter-bound grant suites
(B/C/D), or when a script is blocked.

When a mode’s checks are already covered by a green CI or Staging run for the
same commit, record that evidence and skip re-running the overlapping steps.

## Related

- Cluster bring-up → `contributor-cluster`
- Public k3s / TLS → `production-platform`
- Static / judgment security review → `security-audit`
- Browser UI → `dashboard-browser-qa`
- Protocol bump → `mcp-protocol-compliance`
