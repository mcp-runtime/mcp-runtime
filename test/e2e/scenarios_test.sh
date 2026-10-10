#!/usr/bin/env bash
set -euo pipefail

python3 "$(dirname "$0")/ui_assets_test.py"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
python3 "${SCRIPT_DIR}/image_architecture_test.py"
python3 "${SCRIPT_DIR}/resource_usage_test.py"
bash "${SCRIPT_DIR}/namespace_placement_test.sh"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
QA_E2E_SCRIPT="${PROJECT_ROOT}/test/e2e/qa-e2e.sh"
SELECT_SCRIPT="${PROJECT_ROOT}/test/e2e/select_pr_scenarios.sh"

run_valid() {
  local name="$1"
  local scenarios="$2"
  local expected="$3"
  local output

  if ! output="$(E2E_COLOR=never E2E_VALIDATE_SCENARIOS_ONLY=1 E2E_SCENARIOS="${scenarios}" bash "${QA_E2E_SCRIPT}" 2>&1)"; then
    echo "[fail] ${name}: expected validation success" >&2
    printf '%s\n' "${output}" >&2
    exit 1
  fi

  if ! printf '%s\n' "${output}" | grep -F -x -q -- "[info] E2E scenarios: ${expected}"; then
    echo "[fail] ${name}: missing selected-scenario output" >&2
    printf '%s\n' "${output}" >&2
    exit 1
  fi

  echo "[pass] ${name}"
}

run_invalid() {
  local name="$1"
  local scenarios="$2"
  local expected_error="$3"
  local output

  if output="$(E2E_COLOR=never E2E_VALIDATE_SCENARIOS_ONLY=1 E2E_SCENARIOS="${scenarios}" bash "${QA_E2E_SCRIPT}" 2>&1)"; then
    echo "[fail] ${name}: expected validation failure" >&2
    printf '%s\n' "${output}" >&2
    exit 1
  fi

  if ! printf '%s\n' "${output}" | grep -F -q -- "${expected_error}"; then
    echo "[fail] ${name}: missing expected error" >&2
    printf '%s\n' "${output}" >&2
    exit 1
  fi

  echo "[pass] ${name}"
}

run_valid "all" "all" "all"
run_valid "smoke-auth" "smoke-auth" "smoke-auth"
run_valid "governance" "governance" "governance"
run_valid "trust" "trust" "trust"
run_valid "oauth" "oauth" "oauth"
run_valid "multitenancy" "smoke-auth,multitenancy" "smoke-auth,multitenancy"
run_valid "api-platform" "api-platform" "api-platform"
run_valid "ui-auth" "ui-auth" "ui-auth"
run_valid "adapter-proxy" "adapter-proxy" "adapter-proxy"
run_valid "adapter-certificates" "adapter-certificates" "adapter-certificates,oauth"
run_valid "cli-platform" "cli-platform" "cli-platform"
run_invalid "removed-mtls" "mtls" "unsupported E2E scenario: mtls"
run_valid "platform-update" "platform-update" "platform-update"
run_valid "observability-with-deps" "smoke-auth,governance,trust,oauth,observability" "smoke-auth,governance,trust,oauth,observability"
run_valid "whitespace-trimmed" " smoke-auth , governance " "smoke-auth,governance"
run_valid "duplicates-deduped" "smoke-auth,smoke-auth" "smoke-auth"
run_valid "all-overrides-subsets" "all,smoke-auth" "all"

run_invalid "empty" "" "E2E_SCENARIOS must not be empty"
run_invalid "blank-spaces" "   " "E2E_SCENARIOS must not be empty"
run_invalid "unsupported-token" "smoke-auth,bad" "unsupported E2E scenario: bad"
run_invalid "observability-alone" "observability" "observability requires smoke-auth, governance, trust, and oauth scenarios"
run_invalid "observability-missing-oauth" "smoke-auth,governance,trust,observability" "observability requires smoke-auth, governance, trust, and oauth scenarios"

if ! output="$(E2E_COLOR=never E2E_VALIDATE_SCENARIOS_ONLY=1 E2E_DEEP_REQUEST_FLOWS=1 E2E_SCENARIOS=all bash "${QA_E2E_SCRIPT}" 2>&1)"; then
  echo "[fail] deep-request-flows-all: expected validation success" >&2
  printf '%s\n' "${output}" >&2
  exit 1
fi
if ! printf '%s\n' "${output}" | grep -F -q -- "Pre-release deep request-flow checks: enabled"; then
  echo "[fail] deep-request-flows-all: missing deep-mode output" >&2
  printf '%s\n' "${output}" >&2
  exit 1
fi
echo "[pass] deep-request-flows-all"

if output="$(E2E_COLOR=never E2E_VALIDATE_SCENARIOS_ONLY=1 E2E_DEEP_REQUEST_FLOWS=1 E2E_SCENARIOS=smoke-auth,governance bash "${QA_E2E_SCRIPT}" 2>&1)"; then
  echo "[fail] deep-request-flows-subset: expected validation failure" >&2
  printf '%s\n' "${output}" >&2
  exit 1
fi
if ! printf '%s\n' "${output}" | grep -F -q -- "E2E_DEEP_REQUEST_FLOWS=1 requires all E2E scenarios"; then
  echo "[fail] deep-request-flows-subset: missing expected error" >&2
  printf '%s\n' "${output}" >&2
  exit 1
fi
echo "[pass] deep-request-flows-subset"

for mode in tenant org public; do
  if ! output="$(E2E_COLOR=never E2E_VALIDATE_SCENARIOS_ONLY=1 E2E_SCENARIOS=smoke-auth E2E_PLATFORM_MODE="${mode}" bash "${QA_E2E_SCRIPT}" 2>&1)"; then
    echo "[fail] platform-mode-${mode}: expected validation success" >&2
    printf '%s\n' "${output}" >&2
    exit 1
  fi
  if ! printf '%s\n' "${output}" | grep -F -q -- "[info] E2E platform mode: ${mode}"; then
    echo "[fail] platform-mode-${mode}: missing platform-mode output" >&2
    printf '%s\n' "${output}" >&2
    exit 1
  fi
  echo "[pass] platform-mode-${mode}"
done

if output="$(E2E_COLOR=never E2E_VALIDATE_SCENARIOS_ONLY=1 E2E_SCENARIOS=smoke-auth E2E_PLATFORM_MODE=bad bash "${QA_E2E_SCRIPT}" 2>&1)"; then
  echo "[fail] platform-mode-invalid: expected validation failure" >&2
  printf '%s\n' "${output}" >&2
  exit 1
fi
if ! printf '%s\n' "${output}" | grep -F -q -- "unsupported E2E platform mode: bad"; then
  echo "[fail] platform-mode-invalid: missing expected error" >&2
  printf '%s\n' "${output}" >&2
  exit 1
fi
echo "[pass] platform-mode-invalid"

selector_expect() {
  local name="$1"
  local expected="$2"
  shift 2
  local output

  output="$(printf '%s\n' "$@" | bash "${SELECT_SCRIPT}")"
  if [[ "${output}" != "${expected}" ]]; then
    echo "[fail] selector-${name}: expected ${expected}, got ${output}" >&2
    printf 'paths:\n' >&2
    printf '  %s\n' "$@" >&2
    exit 1
  fi
  echo "[pass] selector-${name}"
}

selector_expect "docs-only" "smoke-auth" "docs/internals/testing.md"
selector_expect "changelog-only" "smoke-auth" "CHANGELOG.md"
selector_expect "ui" "smoke-auth,ui-auth" "services/ui/main.go"
selector_expect "ui-with-changelog" "smoke-auth,ui-auth" "services/ui/main.go" "CHANGELOG.md"
selector_expect "ui-with-guides" "smoke-auth,ui-auth" "services/ui/main.go" "articles/cache.md" ".codex/skills/production-platform/references/operations.md"
selector_expect "api" "smoke-auth,api-platform" "services/platform-api/auth/login.go"
selector_expect "runtime-tools-api" "smoke-auth,api-platform,cli-platform" "services/runtime-api/internal/runtimeapi/tools.go"
selector_expect "catalog-cli" "smoke-auth,cli-platform" "internal/cli/catalog/catalog.go"
selector_expect "test-replica-profile" "smoke-auth,cli-platform,governance,trust,oauth,observability" "internal/cli/setup/platform/test_mode_manifest.go"
selector_expect "adapter" "smoke-auth,adapter-proxy" "internal/cli/adapter/proxy.go"
selector_expect "mtls-operator" "smoke-auth,adapter-certificates" "internal/operator/mtls.go"
selector_expect "gateway" "smoke-auth,governance,trust,oauth,adapter-certificates" "services/mcp-gateway/main.go"
selector_expect "ui-manifest" "smoke-auth,ui-auth" "k8s/09-ui.yaml"
selector_expect "api-manifest" "smoke-auth,api-platform" "k8s/08-runtime-api.yaml"
selector_expect "observability-manifest" "smoke-auth,governance,trust,oauth,observability" "k8s/12-grafana.yaml"
selector_expect "operator-policy" "smoke-auth,governance,trust,oauth" "internal/operator/policy.go"
selector_expect "operator-deployment" "smoke-auth,governance,trust,oauth,adapter-certificates" "internal/operator/deployment.go"
selector_expect "cli-platform-client" "smoke-auth,cli-platform" "internal/cli/platformapi/client.go"
selector_expect "ui-flow-harness" "smoke-auth,ui-auth" "test/e2e/ui_auth_flows.py"
selector_expect "api-flow-harness" "smoke-auth,api-platform,multitenancy" "test/e2e/api_platform_flows.py"
selector_expect "cert-harness" "smoke-auth,adapter-certificates" "test/e2e/lib/adapter-certificates.sh"
selector_expect "platform-auth" "smoke-auth,api-platform,multitenancy,ui-auth" "pkg/platformauth/middleware.go"
selector_expect "api-shared" "smoke-auth,api-platform" "pkg/apihttp/errors.go"
selector_expect "oauth-resource" "smoke-auth,oauth" "pkg/oauthresource/url.go"
selector_expect "cli-login-store" "smoke-auth,cli-platform" "pkg/authfile/store.go"
selector_expect "access" "smoke-auth,governance,trust" "pkg/policy/evaluator.go"
selector_expect "observability" "smoke-auth,governance,trust,oauth,observability" "services/ingest/main.go"
selector_expect "platform-update" "smoke-auth,platform-update" "internal/cli/update/plan.go"
selector_expect "team-management" "smoke-auth,api-platform,cli-platform,multitenancy" "internal/cli/team/team.go"
selector_expect "runtime-team" "smoke-auth,api-platform,multitenancy" "services/runtime-api/internal/runtimeapi/team_members.go"
selector_expect "broad" "smoke-auth" "api/v1alpha1/mcpserver_types.go"
selector_expect "harness" "smoke-auth" "test/e2e/qa-e2e.sh" ".github/workflows/ci.yaml" "go.mod"
selector_expect "unknown" "smoke-auth" "pkg/platforminventory/credentials.go"
selector_expect "staging-e2e-only" "smoke-auth" "test/e2e/staging-vm.sh" "test/e2e/lib/staging.sh" ".github/workflows/staging-e2e.yaml"

python3 - "${PROJECT_ROOT}/.github/workflows/staging-e2e.yaml" "${PROJECT_ROOT}/test/e2e/qa-e2e.sh" "${PROJECT_ROOT}/docs/contributor/staging-e2e.md" "${PROJECT_ROOT}/.github/workflows/ci.yaml" <<'PY'
import pathlib
import sys

workflow = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
# Staging E2E is a pre-release gate on the one disposable VM: no merge
# trigger, called by Pre-release Regression, and one run at a time.
on_block = workflow[workflow.index("\non:\n"):workflow.index("\njobs:\n")]
assert "\n  push:" not in on_block, "staging E2E must not run on every merge to main"
assert "\n  workflow_call:" in on_block and "\n  workflow_dispatch:" in on_block
job = workflow[workflow.index("\n  staging-e2e:\n"):]
assert "concurrency:\n      group: staging-e2e-disposable-vm\n      cancel-in-progress: false" in job, (
    "the VM lock must be on the job so a called run still holds it"
)
assert "RUN_MULTITENANCY: ${{ inputs.run-multitenancy }}" in workflow
assert "FRESH_CERTIFICATE: ${{ inputs.fresh-certificate }}" in workflow
prerelease = pathlib.Path(sys.argv[1]).with_name("pre-release-regression.yaml").read_text(encoding="utf-8")
assert "uses: ./.github/workflows/staging-e2e.yaml" in prerelease, "Pre-release Regression must run Staging E2E"
assert "\n  qa-e2e:\n" not in prerelease, "pre-release must not duplicate PR Kind QA"
assert "test/e2e/qa-e2e.sh" not in prerelease, "Staging must be the only pre-release cluster suite"
assert "run-cache-replay" not in prerelease and "cache-scenarios" not in prerelease, "removed Kind inputs must not remain"
assert "run-multitenancy: true" in prerelease, "staging must retain tenant isolation coverage"
assert "  packages: read" in workflow, "staging must be allowed to pull private GHCR cache images"
assert workflow.index("Verify disposable target") < workflow.index("E2E_GHCR_AUTH_STDIN=1"), (
    "the VM must pass the disposable-target guard before receiving a GHCR token"
)
print("[pass] staging E2E is a single-VM pre-release gate")

staging_docs = pathlib.Path(sys.argv[3]).read_text(encoding="utf-8")
assert "gh workflow run staging-e2e.yaml" in staging_docs, "staging E2E docs must name the on-VM workflow"

ci_workflow = pathlib.Path(sys.argv[4]).read_text(encoding="utf-8")
assert "predicate-quantifier: some-with-excludes" in ci_workflow, (
    "documentation exclusions must override the catch-all code path filter"
)
assert "list-files: json" in ci_workflow and "CHANGED_FILES_JSON: ${{ steps.e2e_changes.outputs.changed_files }}" in ci_workflow, (
    "PR filenames must enter the selector as JSON through the environment"
)
assert "changed_files=( ${{ steps.e2e_changes.outputs.changed_files }} )" not in ci_workflow, (
    "PR filenames must not be interpolated into shell code"
)
print("[pass] CI path exclusions and selector input are guarded")

# Pre-release service tests must track the service modules. A stale entry
# (services/api after the API split) failed the job and skipped E2E.
root = pathlib.Path(sys.argv[4]).parents[2]
prerelease = pathlib.Path(sys.argv[4]).with_name("pre-release-regression.yaml").read_text(encoding="utf-8")
matrix_block = prerelease[prerelease.index("\n  service-tests:\n"):prerelease.index("\n    steps:", prerelease.index("\n  service-tests:\n"))]
listed = {line.strip()[2:] for line in matrix_block.splitlines() if line.strip().startswith("- ")}
modules = {path.parent.name for path in root.glob("services/*/go.mod")}
assert listed == modules, f"pre-release service matrix {sorted(listed)} != service modules {sorted(modules)}"
print("[pass] pre-release service test matrix matches services/*/go.mod")

kind = pathlib.Path(sys.argv[2]).read_text(encoding="utf-8")
build_start = kind.index("build_and_publish_image() {")
build_end = kind.index("\n}\n", build_start)
build_image = kind[build_start:build_end]
assert "pull_cached_image" not in build_image, (
    "images built from the checkout must not be replaced with stale local-mirror tags"
)
assert 'bin/e2e-image-cache' in build_image, "QA images must compare the checkout hash before rebuilding"
qa_job = ci_workflow[ci_workflow.index("\n  qa-e2e:\n"):ci_workflow.index("\n  benchmark:\n")]
# QA E2E runs on GitHub runners so PRs do not queue behind one VM, and it must
# not take the disposable VM's lock or touch the machine Staging E2E uses.
assert "run: bash test/e2e/qa-e2e.sh" in qa_job, "PR QA E2E must run Kind on the GitHub runner"
assert "staging-e2e-disposable-vm" not in qa_job, "PR QA E2E must not hold the Staging E2E VM lock"
assert "E2E_VM_" not in qa_job, "PR QA E2E must not use disposable-VM credentials"
assert "CLUSTER_NAME=mcp-e2e-${suffix}" in qa_job, "parallel QA runs need unique Kind cluster names"
assert 'E2E_IMAGE_CACHE: "1"' in qa_job and 'E2E_GHCR_PUSH: "1"' in qa_job, (
    "QA E2E must use and refresh the content-hash GHCR cache that Staging E2E reads"
)
staging_script = pathlib.Path(sys.argv[2]).parent / "staging-vm.sh"
assert 'E2E_IMAGE_CACHE:-1' in staging_script.read_text(encoding="utf-8"), (
    "staging-vm.sh must enable the content-hash GHCR cache"
)
assert "prune_kind_platform_images" in kind, "setup must evict stale node-local platform image tags"
assert "restart_kind_platform_deployments" in kind, "setup must restart deployments to pull refreshed image tags"
setup_branch = kind.index('echo "[setup] running platform setup in test mode')
setup_call = kind.index('run_logged_stage "setup test mode"', setup_branch)
image_refresh = kind.index("prune_kind_platform_images", setup_branch)
deployment_refresh = kind.index("restart_kind_platform_deployments", setup_call)
assert image_refresh < setup_call < deployment_refresh, (
    "platform image eviction must precede setup and rollout restart must follow its registry pushes"
)
for prefix in (
    "--providers.kubernetesingress.namespaces=",
    "--providers.kubernetescrd.namespaces=",
):
    assert prefix in kind, f"Traefik E2E cleanup must reset {prefix}"
setup_ready = kind.index("\nwait_core_platform_rollouts\n")
user_flows = kind.index('echo "[cli] checking cluster status commands"', setup_ready)
assert "reset_traefik_namespace_watches" in kind[setup_ready:user_flows], (
    "Traefik watch reset must run after setup for cache and fresh-install paths"
)
print("[pass] stale Traefik namespace watches are reset before E2E flows")
PY

echo "[pass] scenario selector validation"

# Validation-only exits before sourcing libraries, so also check that static
# PROJECT_ROOT source targets exist (a merge can revive a removed scenario).
python3 - "${QA_E2E_SCRIPT}" "${PROJECT_ROOT}" <<'PY'
import pathlib, re, sys
script, root = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2])
for target in re.findall(r'^source "\$\{PROJECT_ROOT\}/([^"\n]+)"', script.read_text(), re.MULTILINE):
    assert (root / target).is_file(), f"missing E2E library: {target}"
print("[pass] E2E source targets exist")
PY
bash "${SCRIPT_DIR}/adapter_certificates_test.sh"
