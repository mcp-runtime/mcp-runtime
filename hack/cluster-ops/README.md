# hack/cluster-ops

Deterministic live-cluster checks for the Kind contributor context
(`test-mcp-runtime`). Prefer these over replaying long skill playbooks.

| Script | Covers |
|--------|--------|
| `security-regression.sh` | Suites A/E/F/G/I (auth, headers, login, proxy, log secret scan) |
| `perf-regression.sh` | Scenarios S1–S4 with local baseline compare |
| `k8s-hardening-check.sh` | Namespace inventory, NetworkPolicy presence, privileged/hostNetwork |

Related:

- `hack/validate-authz-matrix.sh` — JSON authz matrix probe
- Staging E2E — strict-prod ship gate (`docs/contributor/staging-e2e.md`)
- Skill routing — `.codex/skills/cluster-ops/SKILL.md`

Example:

```bash
export KUBECONFIG="$HOME/.kube/test-mcp-runtime-config"
kubectl -n traefik port-forward svc/traefik 18080:8000 >/tmp/pf-traefik.log 2>&1 &
bash hack/cluster-ops/security-regression.sh
SCENARIOS=S3,S4 bash hack/cluster-ops/perf-regression.sh
bash hack/cluster-ops/k8s-hardening-check.sh
```
