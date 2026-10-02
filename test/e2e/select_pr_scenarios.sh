#!/usr/bin/env bash
set -euo pipefail

# Select the QA E2E scenarios for a PR/manual run.
# Read changed paths from arguments or stdin. smoke-auth is the install check.
# A path adds only the scenario that exercises that path. The full matrix stays
# on the manual pre-release workflow (E2E_SCENARIOS=all).

declare -a changed_paths=()
if [[ "$#" -gt 0 ]]; then
  changed_paths=("$@")
else
  while IFS= read -r path; do
    [[ -n "${path}" ]] && changed_paths+=("${path}")
  done
fi

declare -a scenarios=("smoke-auth")

add_scenario() {
  local wanted="$1"
  local existing
  for existing in "${scenarios[@]}"; do
    if [[ "${existing}" == "${wanted}" ]]; then
      return
    fi
  done
  scenarios+=("${wanted}")
}

add_observability() {
  add_scenario "governance"
  add_scenario "trust"
  add_scenario "oauth"
  add_scenario "observability"
}

classify_path() {
  local path="$1"

  case "${path}" in
    ""|AGENTS.md|CHANGELOG.md|README.md|articles/*|docs/*|website/*|.codex/skills/*.md)
      return
      ;;
    test/e2e/staging-*.sh|test/e2e/staging_lib_test.sh|test/e2e/lib/staging.sh|test/e2e/lib/cluster-wait.sh|.github/workflows/staging-e2e*.yaml)
      # Staging E2E runs only on the disposable VM through its own workflows;
      # its offline guard/stage tests run in CI without a Kind cluster.
      return
      ;;
    test/e2e/*|.github/workflows/ci.yaml|.github/workflows/pre-release-regression.yaml|go.mod|go.sum|Makefile*|Dockerfile*)
      # The baseline install builds the images and runs setup. Pre-release
      # still runs every scenario.
      return
      ;;
    internal/operator/mtls*|internal/cli/certmanager/*|traefik-plugins/spiffe-identity/*|config/cert-manager/*|pkg/identity/*|pkg/certauth/*)
      # Certificate enrollment. The runner adds the oauth scenario because
      # those assertions live in that block.
      add_scenario "adapter-certificates"
      return
      ;;
    traefik-plugins/pii-redactor/*)
      # PII redaction is asserted in the observability pass.
      add_observability
      return
      ;;
    api/*|cmd/operator/*|internal/operator/*|config/*|k8s/*|pkg/controlplane/*|pkg/k8sclient/*|pkg/kubeworkload/*|pkg/manifest/*|pkg/metadata/*)
      # Setup plus the namespace-placement check in smoke-auth covers install
      # and reconcile. Product flows are selected from their own paths.
      return
      ;;
    internal/cli/update/*|internal/platformrelease/*|hack/release/*)
      add_scenario "platform-update"
      return
      ;;
    cmd/mcp-runtime/*|internal/cli/root/*|internal/cli/catalog/*)
      add_scenario "cli-platform"
      return
      ;;
    internal/cli/adapter/*|internal/agentadapter/*)
      add_scenario "adapter-proxy"
      return
      ;;
    internal/cli/access/*|pkg/access/*|pkg/policy/*)
      add_scenario "governance"
      add_scenario "trust"
      return
      ;;
    internal/cli/team/*|services/platform-api/internal/platformstore/*)
      add_scenario "api-platform"
      add_scenario "cli-platform"
      add_scenario "multitenancy"
      return
      ;;
    internal/cli/auth/*|internal/cli/cluster/*|internal/cli/registry/*|internal/cli/server/*|internal/cli/setup/*|internal/cli/sentinel/*)
      add_scenario "cli-platform"
      return
      ;;
    services/runtime-api/internal/runtimeapi/*team*|services/runtime-api/internal/runtimeapi/*namespace*|services/runtime-api/internal/runtimeapi/*registry*|services/runtime-api/internal/runtimeapi/*deploy*|services/runtime-api/internal/runtimeapi/*server*)
      add_scenario "api-platform"
      add_scenario "multitenancy"
      return
      ;;
    services/runtime-api/internal/runtimeapi/*tool*)
      add_scenario "api-platform"
      add_scenario "cli-platform"
      return
      ;;
    services/runtime-api/internal/runtimeapi/*adapter*|services/runtime-api/internal/runtimeapi/*grant*|services/runtime-api/internal/runtimeapi/*session*|services/runtime-api/internal/runtimeapi/*access*)
      add_scenario "api-platform"
      add_scenario "governance"
      add_scenario "adapter-proxy"
      return
      ;;
    services/platform-api/*|services/runtime-api/*|services/analytics-api/*)
      add_scenario "api-platform"
      return
      ;;
    services/ui/*)
      add_scenario "ui-auth"
      return
      ;;
    services/mcp-gateway/*)
      add_scenario "governance"
      return
      ;;
    services/ingest/*|services/processor/*|pkg/clickhouse/*|pkg/events/*|pkg/sentinel/*|pkg/serviceutil/*)
      add_observability
      return
      ;;
    examples/*)
      add_scenario "trust"
      return
      ;;
    test/integration/*|test/golden/*|test/benchmark/*)
      return
      ;;
    *)
      return
      ;;
  esac
}

for path in "${changed_paths[@]}"; do
  classify_path "${path}"
done

IFS=','
echo "${scenarios[*]}"
