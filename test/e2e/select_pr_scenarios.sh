#!/usr/bin/env bash
set -euo pipefail

# Select the QA E2E scenarios for a PR/manual run.
# Read changed paths from arguments or stdin. smoke-auth is the install check.
# A path adds only the scenario that exercises that path. Pre-release uses
# Staging E2E; full Kind sweeps remain available through E2E_SCENARIOS=all.

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
    test/e2e/api_platform_flows.py)
      # A scenario's own test code must run that scenario.
      add_scenario "api-platform"
      add_scenario "multitenancy"
      return
      ;;
    test/e2e/ui_auth_flows.py)
      add_scenario "ui-auth"
      return
      ;;
    test/e2e/mcp_header_proxy.py)
      add_scenario "oauth"
      return
      ;;
    test/e2e/lib/adapter-certificates.sh)
      add_scenario "adapter-certificates"
      return
      ;;
    test/e2e/scenarios/platform-update.sh)
      add_scenario "platform-update"
      return
      ;;
    test/e2e/*|.github/workflows/ci.yaml|.github/workflows/pre-release-regression.yaml|go.mod|go.sum|Makefile*|Dockerfile*)
      # The baseline install builds the images and runs setup. Pre-release
      # uses the separate Staging E2E suite.
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
    k8s/09-ui.yaml)
      add_scenario "ui-auth"
      return
      ;;
    k8s/08-platform-api*.yaml|k8s/08-runtime-api*.yaml|k8s/08-analytics-api.yaml|k8s/20-postgres*.yaml|k8s/22-split-api-networkpolicy.yaml)
      add_scenario "api-platform"
      return
      ;;
    k8s/03-clickhouse*.yaml|k8s/04-clickhouse-init.yaml|k8s/05-kafka*.yaml|k8s/06-ingest.yaml|k8s/07-processor.yaml|k8s/11-prometheus.yaml|k8s/12-grafana.yaml|k8s/15-otel-collector.yaml|k8s/16-tempo.yaml|k8s/17-loki.yaml|k8s/18-promtail.yaml|k8s/19-grafana-datasources.yaml|k8s/21-grafana-dashboards.yaml)
      add_observability
      return
      ;;
    k8s/14-mcp-gateway-sidecar.yaml|internal/operator/policy.go|internal/operator/oauth_resources.go)
      # The operator renders gateway policy and OAuth resources per server.
      add_scenario "governance"
      add_scenario "trust"
      add_scenario "oauth"
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
    internal/cli/core/*|internal/cli/platformapi/*|internal/cli/kube/*|internal/cli/kubeerr/*|internal/cli/status/*|internal/cli/platformstatus/*|internal/cli/agent/*|internal/cli/admin/*|internal/cli/bootstrap/*|\
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
    pkg/platformauth/*|pkg/authzmatrix/*)
      # API key/bearer auth and the role matrix every API and the UI proxy use.
      add_scenario "api-platform"
      add_scenario "multitenancy"
      add_scenario "ui-auth"
      return
      ;;
    pkg/apihttp/*|pkg/internalapi/*|pkg/platform/*|pkg/publishscope/*|pkg/registrypush/*)
      # Shared HTTP, DTO, publish-scope and registry-push code under the APIs.
      add_scenario "api-platform"
      return
      ;;
    pkg/oauthresource/*)
      add_scenario "oauth"
      return
      ;;
    pkg/authfile/*|pkg/runtimeconfig/*)
      # The CLI's saved login and config paths.
      add_scenario "cli-platform"
      return
      ;;
    services/ui/*)
      add_scenario "ui-auth"
      return
      ;;
    services/mcp-gateway/*)
      # The gateway enforces grants, trust, OAuth and adapter certificates.
      add_scenario "governance"
      add_scenario "trust"
      add_scenario "oauth"
      add_scenario "adapter-certificates"
      return
      ;;
    pkg/svcboot/*|services/ingest/*|services/processor/*|pkg/clickhouse/*|pkg/events/*|pkg/sentinel/*|pkg/serviceutil/*)
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
