#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
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

  if ! printf '%s\n' "${output}" | grep -F -q -- "[info] E2E scenarios: ${expected}"; then
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
run_valid "api-platform" "api-platform" "api-platform"
run_valid "ui-auth" "ui-auth" "ui-auth"
run_valid "adapter-proxy" "adapter-proxy" "adapter-proxy"
run_valid "adapter-certificates" "adapter-certificates" "adapter-certificates"
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

selector_expect "docs-only" "smoke-auth" "docs/internals/tests.md"
selector_expect "ui" "smoke-auth,ui-auth" "services/ui/main.go"
selector_expect "api" "smoke-auth,api-platform" "services/platform-api/auth/login.go"
selector_expect "runtime-tools-api" "smoke-auth,api-platform,cli-platform" "services/runtime-api/internal/runtimeapi/tools.go"
selector_expect "catalog-cli" "smoke-auth,cli-platform" "internal/cli/catalog/catalog.go"
selector_expect "adapter" "smoke-auth,adapter-proxy,governance" "internal/cli/adapter/proxy.go"
selector_expect "mtls-operator" "smoke-auth,oauth,adapter-proxy,adapter-certificates" "internal/operator/mtls.go"
selector_expect "gateway" "smoke-auth,governance,trust,oauth,adapter-proxy" "services/mcp-gateway/main.go"
selector_expect "observability" "smoke-auth,governance,trust,oauth,observability" "services/ingest/main.go"
selector_expect "platform-update" "smoke-auth,platform-update" "internal/cli/update/plan.go"
selector_expect "broad" "all" "api/v1alpha1/mcpserver_types.go"
selector_expect "staging-e2e-only" "smoke-auth" "test/e2e/staging-vm.sh" "test/e2e/lib/staging.sh" ".github/workflows/staging-e2e.yaml"

python3 - "${PROJECT_ROOT}/.github/workflows/staging-e2e.yaml" "${PROJECT_ROOT}/test/e2e/qa-e2e.sh" "${PROJECT_ROOT}/docs/contributor/staging-e2e.md" <<'PY'
import pathlib
import sys

workflow = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
assert "  push:\n    branches: [main]\n    paths:" in workflow, "staging E2E must trigger on relevant pushes to main"
for path in (
    "'.github/workflows/staging-e2e-remote.yaml'",
    "'services/**'",
    "'internal/**'",
    "'test/e2e/**'",
    "'hack/deploy/mcpruntime-org/**'",
):
    assert path in workflow, f"staging E2E push trigger is missing {path}"
assert "RUN_MULTITENANCY: ${{ github.event_name == 'push' || inputs.run-multitenancy }}" in workflow
assert "FRESH_CERTIFICATE: ${{ github.event_name == 'workflow_dispatch' && inputs.fresh-certificate }}" in workflow
print("[pass] staging E2E main-push trigger and event defaults")

staging_docs = pathlib.Path(sys.argv[3]).read_text(encoding="utf-8")
assert "gh workflow run staging-e2e.yaml" in staging_docs, "staging E2E docs must name the on-VM workflow"
assert "gh workflow run staging-e2e-remote.yaml" in staging_docs, "staging E2E docs must name the runner-driven workflow"

kind = pathlib.Path(sys.argv[2]).read_text(encoding="utf-8")
build_start = kind.index("build_and_publish_image() {")
build_end = kind.index("\n}\n", build_start)
build_image = kind[build_start:build_end]
assert "pull_cached_image" not in build_image, (
    "images built from the checkout must not be replaced with stale local-mirror tags"
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
setup_ready = kind.index("wait_core_platform_rollouts\n\n# Setup can reuse an existing IngressClass")
user_flows = kind.index('echo "[cli] checking platform status commands"', setup_ready)
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
