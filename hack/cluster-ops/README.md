# hack/cluster-ops

Deterministic live-cluster checks for the Kind contributor context
(a `kind-*` or `test-mcp-runtime` context with actual `kind://` node provider IDs). Prefer these over replaying long skill playbooks.

| Script | Covers |
|--------|--------|
| `security-regression.sh` | Suites A/E/F/G/I (auth, headers, login, proxy, log secret scan) |
| `perf-regression.sh` | Scenarios S1–S3 with local baseline compare |
| `k8s-hardening-check.sh` | Namespace inventory, NetworkPolicy presence, privileged/hostNetwork |

Related:

- `hack/validate-authz-matrix.sh` — JSON authz matrix probe
- Staging E2E — strict-prod ship gate (`docs/contributor/staging-e2e.md`)
- Skill routing — `.codex/skills/cluster-ops/SKILL.md`

Example:

```bash
export KUBECONFIG="$HOME/.kube/test-mcp-runtime-config"
# Performance requires actual measurements and a baseline; to record deliberately:
# PERF_RECORD_BASELINE=1 SCENARIOS=S3 bash hack/cluster-ops/perf-regression.sh
kubectl -n traefik port-forward svc/traefik 18080:8000 >/tmp/pf-traefik.log 2>&1 &
bash hack/cluster-ops/security-regression.sh
SCENARIOS=S3 bash hack/cluster-ops/perf-regression.sh
bash hack/cluster-ops/k8s-hardening-check.sh
```

Posture inventory is read-only and may target an explicitly chosen kubeconfig. It checks Deployments, StatefulSets and DaemonSets, including init containers. It does not prove NetworkPolicy enforcement; use E2E traffic tests.

The old S4 annotation timer was removed: waiting on an already-Ready server did not measure reconciliation. Use the operator integration and deployment E2E for reconciliation coverage.
