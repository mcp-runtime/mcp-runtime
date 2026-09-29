---
name: security-audit
description: Security review for MCP Runtime with modes pr, platform, k8s, and supply-chain. Use for PR-scoped trust-boundary review, full threat-model audits, RBAC/PSS/NetworkPolicy hygiene, or dependency/image/SBOM/Actions supply-chain work. Prefer CI and pre-release-regression scanners for deterministic checks; use live cluster-ops security mode for traffic regressions.
---

# Security Audit

Diff-driven and platform security review. Prefer **deterministic** scanners in
CI / `.github/workflows/pre-release-regression.yaml` (gitleaks, gosec, Trivy,
SBOM) before long manual passes. Live auth/gateway regressions belong in
`cluster-ops` mode `security`, not here.

## Modes

| Mode | When | Load |
|------|------|------|
| `pr` | Single change / PR trust boundaries | [references/pr-change.md](references/pr-change.md) |
| `platform` | Deep / pre-release threat-model audit | [references/platform.md](references/platform.md) |
| `k8s` | RBAC, PSS, NetworkPolicy, manifest hygiene | [references/kubernetes.md](references/kubernetes.md) |
| `supply-chain` | Deps, images, SBOMs, signatures, Actions pinning | [references/supply-chain.md](references/supply-chain.md) |

Default for ordinary PRs: mode `pr`. Add `k8s` or `supply-chain` when the diff
touches those surfaces. Use `platform` only when asked for a thorough audit.

When the review includes API/CRD/CLI design choices, also read
[`_shared/design-principles.md`](../_shared/design-principles.md).

## Deterministic preference

| Check | Source of truth |
|-------|-----------------|
| Secret leak scan | gitleaks workflows / pre-release |
| Go SAST | gosec in pre-release |
| FS/image vulns + misconfig | Trivy in pre-release |
| Repository SBOM | CI `sbom` / pre-release SBOM jobs |
| Live auth deny / headers / log scan | `bash hack/cluster-ops/security-regression.sh` |
| Authz matrix rows | `bash hack/validate-authz-matrix.sh` |
| K8s inventory / privileged / NP | `bash hack/cluster-ops/k8s-hardening-check.sh` |
| Live auth deny / headers (judgment) | `cluster-ops` mode `security` references |

If CI or the scripts already passed on this commit, cite the run and focus the
agent pass on judgment (authz gaps, threat model, Actions pin review).

## Report

Use [`_shared/FINDINGS-TEMPLATE.md`](../_shared/FINDINGS-TEMPLATE.md). Lead with
mode(s), commit SHA, and go/no-go/blocked.

## Related

- Live security traffic → `cluster-ops` mode `security`
- Prod TLS / registry auth ops → `production-platform`
