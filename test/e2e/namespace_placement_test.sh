#!/usr/bin/env bash
# Offline check for the shared QA/Staging namespace placement contract.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/namespace-placement.sh
source "${SCRIPT_DIR}/lib/namespace-placement.sh"

FAILURES=0
pass() { printf '%s\n' "[pass] $1"; }
failed() {
  printf '%s\n' "[fail] $1" >&2
  FAILURES=$((FAILURES + 1))
}

require_line() {
  local name="$1" haystack="$2" needle="$3"
  if printf '%s\n' "${haystack}" | grep -Fxq -- "${needle}"; then
    pass "${name}"
  else
    failed "${name}: missing ${needle}"
  fi
}

workloads="$(namespace_placement_workloads)"
secrets="$(namespace_placement_secrets)"
configmaps="$(namespace_placement_configmaps)"
envs="$(namespace_placement_env)"

require_line "platform api workload" "${workloads}" "deployment|mcp-platform|mcp-platform-api"
require_line "analytics workload" "${workloads}" "deployment|mcp-observability|mcp-analytics-api"
require_line "promtail workload" "${workloads}" "daemonset|mcp-log-collector|promtail"
require_line "postgres workload" "${workloads}" "statefulset|mcp-platform|mcp-postgres"
require_line "ui secret" "${secrets}" "mcp-platform|mcp-ui-credentials"
require_line "ingest secret" "${secrets}" "mcp-observability|mcp-ingest-credentials"
require_line "shared config on platform" "${configmaps}" "mcp-platform|mcp-shared-config"
require_line "shared config on observability" "${configmaps}" "mcp-observability|mcp-shared-config"
require_line "ui analytics upstream" "${envs}" "deployment|mcp-platform|mcp-ui|ANALYTICS_UPSTREAM|http://mcp-analytics-api.mcp-observability.svc.cluster.local:8085"
require_line "runtime clickhouse address" "${envs}" "deployment|mcp-platform|mcp-runtime-api|CLICKHOUSE_ADDR|clickhouse.mcp-observability.svc.cluster.local:9000"

if printf '%s\n' "${workloads}${secrets}${configmaps}${envs}" | grep -q 'mcp-sentinel'; then
  failed "contract still names mcp-sentinel"
else
  pass "contract omits mcp-sentinel"
fi

for script in qa-e2e.sh lib/staging.sh; do
  if grep -q 'namespace_placement_verify' "${SCRIPT_DIR}/${script}"; then
    pass "${script} runs the placement check"
  else
    failed "${script} does not run the placement check"
  fi
done

install_fake_kubectl() {
  FAKE_KUBECTL_MODE="$1"
  kubectl() {
    if [[ "$1" == "get" && "$2" == "namespace" && "$3" == "mcp-sentinel" ]]; then
      [[ "${FAKE_KUBECTL_MODE}" == "legacy-namespace" ]]
      return
    fi
    if [[ "$1" == "get" && "$2" == "secret" && "$3" == "mcp-sentinel-secrets" ]]; then
      return 1
    fi
    if [[ "$1" == "get" && "$2" == "secret" && "$3" == "mcp-platform-tls" && "$5" == "mcp-observability" ]]; then
      [[ "${FAKE_KUBECTL_MODE}" == "tls-in-observability" ]]
      return
    fi
    if [[ "$1" == "get" && "$2" == "deployment" && "$3" == "traefik" ]]; then
      case "${FAKE_KUBECTL_MODE}:$5" in
        k3s-traefik:kube-system)
          # k3s bundled Traefik: no namespace flag, so it watches everything.
          printf '%s' '["--providers.kubernetesingress","--providers.kubernetescrd","--providers.kubernetesingress.ingressendpoint.publishedservice=kube-system/traefik"]'
          return 0
          ;;
        k3s-traefik:*) return 1 ;;
        traefik-missing-servers:traefik)
          printf '%s' '--providers.kubernetesingress.namespaces=registry,mcp-platform,mcp-observability'
          return 0
          ;;
        *:traefik)
          printf '%s' '--providers.kubernetesingress.namespaces=registry,mcp-platform,mcp-observability,mcp-servers'
          return 0
          ;;
      esac
      return 1
    fi
    if [[ "$*" == *jsonpath=* ]]; then
      case "$*" in
        *ANALYTICS_UPSTREAM*) printf '%s' 'http://mcp-analytics-api.mcp-observability.svc.cluster.local:8085' ;;
        *RUNTIME_UPSTREAM*) printf '%s' 'http://mcp-runtime-api.mcp-platform.svc.cluster.local:8084' ;;
        *API_UPSTREAM*) printf '%s' 'http://mcp-platform-api.mcp-platform.svc.cluster.local:8080' ;;
        *RUNTIME_CONTROL_URL*) printf '%s' 'http://mcp-runtime-api.mcp-platform.svc.cluster.local:8084' ;;
        *PLATFORM_API_URL*) printf '%s' 'http://mcp-platform-api.mcp-platform.svc.cluster.local:8080' ;;
        *CLICKHOUSE_ADDR*) printf '%s' 'clickhouse.mcp-observability.svc.cluster.local:9000' ;;
        *) printf '%s' 'wrong' ;;
      esac
      return 0
    fi
    if [[ "${FAKE_KUBECTL_MODE}" == "missing-ingest" && "$*" == "get deployment mcp-ingest -n mcp-observability" ]]; then
      return 1
    fi
    return 0
  }
}

run_mode() {
  local name="$1" mode="$2" want_ok="$3"
  install_fake_kubectl "${mode}"
  if namespace_placement_verify >"${TMP}/out" 2>&1; then
    if [[ "${want_ok}" == "yes" ]]; then pass "${name}"; else failed "${name} (expected refusal)"; cat "${TMP}/out" >&2; fi
  else
    if [[ "${want_ok}" == "no" ]]; then pass "${name}"; else failed "${name}"; cat "${TMP}/out" >&2; fi
  fi
}

TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
run_mode "clean install placement" clean yes
run_mode "refuses the old namespace" legacy-namespace no
run_mode "refuses a second platform certificate" tls-in-observability no
run_mode "refuses a missing observability workload" missing-ingest no
run_mode "accepts k3s Traefik watching every namespace" k3s-traefik yes
run_mode "refuses a scoped Traefik missing mcp-servers" traefik-missing-servers no

if [[ "${FAILURES}" -ne 0 ]]; then
  printf '%s\n' "${FAILURES} namespace placement checks failed" >&2
  exit 1
fi
printf '%s\n' "namespace placement checks passed"
