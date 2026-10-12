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
| Contributor security/performance | `cluster-ops/` | Traffic/load tests require Kind node identity; posture inventory is read-only |
| Service authorization matrix | `validate-authz-matrix.sh` | Explicit loopback service URLs and a real non-admin credential; Kind only |
| OpenAPI contract review | `oasdiff-openapi.sh <base-ref>` | Explicit previous revision; missing tools/specs fail; breaking releases remain allowed |
| Image vulnerability review | `trivy-platform-images.sh` | Builds/scans locally; excludes unfixed CVEs under the existing scanner policy |
| E2E image reuse | `e2e-image-cache/` | Content-hash cache consumed by QA E2E |
| Release component manifest | `release/platformmanifest/` | Source of truth shared with `mcp-runtime update` |
| Hosted release acceptance | `deploy/hosted-platform/` | Disposable runner; checks publish/deploy, grant/session and allowed/denied calls |
| Reference platform recovery/update | `deploy/mcpruntime-org/` | Explicit profile, source ref, context and cluster UID; see its README |
| Go package documentation | `deploy/pkgsite/` | CI-built image, isolated source rewrite, tested remote rollback |
| Generated Go header | `boilerplate.go.txt` | Code-generation asset |

## Contributor checks

The automation gate requires Python 3.9+ and ShellCheck (`apt-get install shellcheck` or `brew install shellcheck`). Go builds/tests require the version in `go.mod` and cached/downloadable dependencies.

```bash
hack/dev/deps.sh check
hack/dev/dev-setup.sh generate
hack/ci/smoke-ci.sh
bash hack/trivy-platform-images.sh
bash hack/oasdiff-openapi.sh <previous-release>
```

Unit-test wrappers refuse hosts with `/etc/rancher/k3s/k3s.yaml`: core discovery can fall back to that file. Use an isolated container/CI runner on those hosts. The script regression gate uses fake tools and can still run offline.

For full cluster coverage use `test/e2e/qa-e2e.sh` on Kind and Pre-release
Regression's disposable VM staging suite. Offline smoke does not claim image,
cluster, policy, browser or recovery acceptance. Performance comparisons require
successful S1–S3 measurements and an existing baseline; deliberately create one with `PERF_RECORD_BASELINE=1`.

## Production and recovery

See [the reference helper guide](deploy/mcpruntime-org/README.md) and
[the deployment runbook](../docs/reference-deployment.md). Normal updates use
`mcp-runtime update` through `rollout.sh`; they never rewrite registry pull
credentials, TLS, tenant policies or configuration. Full setup and recovery are
separate deliberate operations. No script automatically restores `latest`.

`clean.sh` is a destructive reset, never a release prerequisite. It requires
an explicit cluster confirmation and a checksum-verified full backup. A live
volume copy is not proof of recoverability: rehearse database-native recovery
before resetting the hosted platform and keep protected backup copies off-host.

The historical API-split milestone scripts were removed. Their useful routing,
authentication and service-readiness checks belong in maintained service tests,
the authorization matrix and E2E suites.
