# Repository automation

`hack/` contains small, maintained tools for contributors, CI and the hosted
reference deployment. User workflows belong in `mcp-runtime`; scripts compose
those workflows rather than duplicating cluster management.

| Task | Tool | Boundary |
|---|---|---|
| Developer dependencies | `dev/deps.sh check` | Reports host/toolchain readiness; `install` explicitly changes the host |
| Code generation | `dev/dev-setup.sh generate` | Changes generated source; never creates a cluster |
| Automation behavior/lint | `ci/check-scripts.sh` | Fake-tool regressions, pkgsite rollback, shell syntax and ShellCheck; no live targets |
| Offline verification | `ci/smoke-ci.sh` | Script regressions, shell syntax and unit tests; missing prerequisites fail |
| Static analysis | `ci/staticcheck.sh ./...` | Pre-commit helper; pinned installation guidance |
| Isolated Go tests | `ci/go-test.sh <module> [args...]` | Replaces inherited kubeconfig with an empty file; refuses hosts with a system k3s kubeconfig |
| Contributor security/performance | `qa/` | Traffic/load tests require Kind node identity; posture inventory is read-only |
| Service authorization matrix | `qa/validate-authz-matrix.sh` | Explicit loopback service URLs and a real non-admin credential; Kind only |
| OpenAPI contract review | `ci/oasdiff-openapi.sh <base-ref>` | Explicit previous revision; missing tools/specs fail; breaking releases remain allowed |
| Image vulnerability review | `ci/trivy-platform-images.sh` | Builds/scans locally; excludes unfixed CVEs under the existing scanner policy |
| Multi-team demo | `qa/multitenancy-test.sh` | Loopback or disposable staging only; private credentials and scoped cleanup |
| E2E image reuse | `qa/image-cache/` | Content-hash cache consumed by QA E2E |
| Release component manifest | `release/platformmanifest/` | Source of truth shared with `mcp-runtime update` |
| Hosted release acceptance | `deploy/acceptance/` | Disposable runner; checks publish/deploy, grant/session and allowed/denied calls |
| Reference platform recovery/update | `deploy/reference/` | Explicit profile, source ref, context and cluster UID; see its README |
| Go package documentation | `deploy/pkgsite/` | CI-built image, isolated source rewrite, tested remote rollback |
| Generated Go header | `boilerplate.go.txt` | Code-generation asset |

## Layout and ownership

```text
hack/
  dev/                 dependencies and generation
  ci/                  offline checks, static analysis and scanners
  qa/                  cluster probes, demos and image-cache tooling
  deploy/reference/    public reference setup, update, backup and recovery
  deploy/acceptance/   hosted platform acceptance
  deploy/pkgsite/      Go documentation hosting
  release/             release manifest tooling
  lib/                 shared sourced/imported helpers
  tests/               offline automation regressions
  boilerplate.go.txt   code-generation asset
```

The table above is the canonical script catalogue. `AGENTS.md` routes tasks here;
focused directory READMEs and runbooks explain execution. `lib/` files are
internal helpers, not commands. QA fixtures and their credentials belong on
disposable infrastructure; deployment profiles belong outside committed source.

When adding or moving automation:

- Choose the directory by purpose; do not add executable scripts at `hack/` root.
- Document its purpose, prerequisites, target boundary, inputs and verification.
- Keep supported product operations in the CLI; scripts compose them.
- Update all callers, workflows, tests, docs and skill references in the same PR.
- Add behavior coverage for new logic and run `hack/ci/check-scripts.sh`.
- Keep one canonical path; update callers instead of adding old-path wrappers.

## Contributor checks

The automation gate requires Python 3.9+ and ShellCheck (`apt-get install shellcheck` or `brew install shellcheck`). Go builds/tests require the version in `go.mod` and cached/downloadable dependencies.

```bash
hack/dev/deps.sh check
hack/dev/dev-setup.sh generate
hack/ci/smoke-ci.sh
bash hack/ci/trivy-platform-images.sh
bash hack/ci/oasdiff-openapi.sh <previous-release>
```

Unit-test wrappers refuse hosts with `/etc/rancher/k3s/k3s.yaml`: core discovery can fall back to that file. Use an isolated container/CI runner on those hosts. The script regression gate uses fake tools and can still run offline.

For full cluster coverage use `test/e2e/qa-e2e.sh` on Kind and Pre-release
Regression's disposable VM staging suite. Offline smoke does not claim image,
cluster, policy, browser or recovery acceptance. Performance comparisons require
successful S1–S3 measurements and an existing baseline; deliberately create one with `PERF_RECORD_BASELINE=1`.

## Production and recovery

See [the reference helper guide](deploy/reference/README.md) and
[the deployment runbook](../docs/reference-deployment.md). Normal updates use
`mcp-runtime update --to <tag> --build --expect-cluster-id <uid>` (what the hosted
release pipeline runs); they never rewrite registry pull credentials, TLS or
tenant policies. A release may declare the ServiceAccounts, RBAC, ConfigMaps and
Deployment wiring `update` applies. Full setup is `mcp-runtime setup`, which
refuses a wrong cluster ID and an existing mcp-auth it would drop. The remaining
scripts here are recovery tools only. No script automatically restores `latest`.

`reset.sh` is a destructive reset, never a release prerequisite. It requires
an explicit cluster confirmation and a checksum-verified full backup. A live
volume copy is not proof of recoverability: rehearse database-native recovery
before resetting the hosted platform and keep protected backup copies off-host.

The historical API-split milestone scripts were removed. Their useful routing,
authentication and service-readiness checks belong in maintained service tests,
the authorization matrix and E2E suites.
