# hack/qa

Deterministic live-cluster checks for the Kind contributor context
(a `kind-*` or `test-mcp-runtime` context with actual `kind://` node provider IDs). Prefer these over replaying long skill playbooks.

| Script | Covers |
|--------|--------|
| `security-regression.sh` | Suites A/E/F/G/I (auth, headers, login, proxy, log secret scan) |
| `perf-regression.sh` | Scenarios S1–S3 with local baseline compare |
| `validate-authz-matrix.sh` | Explicit per-service loopback URLs and a real non-admin credential; Kind only |
| `multitenancy-test.sh` | CLI/API multi-team demo on loopback or disposable `.e2e.mcpruntime.org` targets |
| `image-cache/` | Content-hash image reuse consumed by QA E2E |
| `k8s-hardening-check.sh` | Namespace inventory, NetworkPolicy presence, privileged/hostNetwork |

Related:

- `hack/qa/validate-authz-matrix.sh` — JSON authz matrix probe
- Staging E2E — strict-prod ship gate (`docs/contributor/staging-e2e.md`)
- Skill routing — `.codex/skills/cluster-ops/SKILL.md`

Example:

```bash
export KUBECONFIG="$HOME/.kube/test-mcp-runtime-config"
# Performance requires actual measurements and a baseline; to record deliberately:
# PERF_RECORD_BASELINE=1 SCENARIOS=S3 bash hack/qa/perf-regression.sh
kubectl -n traefik port-forward svc/traefik 18080:8000 >/tmp/pf-traefik.log 2>&1 &
bash hack/qa/security-regression.sh
SCENARIOS=S3 bash hack/qa/perf-regression.sh
bash hack/qa/k8s-hardening-check.sh
```

Posture inventory is read-only and may target an explicitly chosen kubeconfig. It checks Deployments, StatefulSets and DaemonSets, including init containers. It does not prove NetworkPolicy enforcement; use E2E traffic tests.

The old S4 annotation timer was removed: waiting on an already-Ready server did not measure reconciliation. Use the operator integration and deployment E2E for reconciliation coverage.

Demo passwords are random and stored with mode 0600 in the private profile directory as `demo-credentials-<run-id>.json`. Reusing the run/profile retains them. Exclude this directory from shared artifacts. Demo accounts/teams remain on disposable infrastructure for inspection; cleanup selects only its own servers and sessions. See [the demo runbook](../../docs/reference-deployment.md#multitenancy-test) for configuration.
