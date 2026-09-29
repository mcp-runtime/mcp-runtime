#!/usr/bin/env bash
# Offline tests for test/e2e/lib/staging.sh: the disposable-target guard, the
# marker check, and the stage runner/summary. No network or cluster needed;
# DNS is replaced by a static table through STAGING_RESOLVE_TABLE.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=test/e2e/lib/staging.sh
source "${SCRIPT_DIR}/lib/staging.sh"
# shellcheck source=test/e2e/lib/k3s-installer.sh
source "${SCRIPT_DIR}/lib/k3s-installer.sh"
# shellcheck source=hack/deploy/mcpruntime-org/lib/adapter-readiness.sh
source "${SCRIPT_DIR}/../../hack/deploy/mcpruntime-org/lib/adapter-readiness.sh"

TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0

pass() { echo "[pass] $1"; }
failed() {
  echo "[fail] $1" >&2
  FAILURES=$((FAILURES + 1))
}
expect_ok() {
  local name="$1"
  shift
  if "$@" >"${TMP}/out" 2>&1; then pass "${name}"; else
    failed "${name}"
    cat "${TMP}/out" >&2
  fi
}
expect_fail() {
  local name="$1"
  shift
  if "$@" >"${TMP}/out" 2>&1; then
    failed "${name} (expected refusal)"
    cat "${TMP}/out" >&2
  else pass "${name}"; fi
}
expect_eq() {
  if [[ "$2" == "$3" ]]; then pass "$1"; else failed "$1: got '$2', want '$3'"; fi
}

# --- flags and URL parsing --------------------------------------------------
installer_download_case() (
  local scenario="$1" calls=0
  curl() {
    calls=$((calls + 1))
    case "${scenario}:${calls}" in
      primary:1|fallback:2) printf '#!/bin/sh\nexit 0\n' >"${TMP}/installer.sh"; return 0 ;;
      empty:*) : >"${TMP}/installer.sh"; return 0 ;;
      invalid:*) printf 'if\n' >"${TMP}/installer.sh"; return 0 ;;
      *) printf 'partial download\n' >"${TMP}/installer.sh"; return 22 ;;
    esac
  }
  if staging_download_k3s_installer "${TMP}/installer.sh"; then
    case "${scenario}" in
      primary) [[ "${calls}" == 1 ]] ;;
      fallback) [[ "${calls}" == 2 ]] ;;
      *) return 1 ;;
    esac
  else
    [[ "${calls}" == 2 && ! -e "${TMP}/installer.sh" && "${scenario}" != primary && "${scenario}" != fallback ]]
  fi
)
for scenario in primary fallback failure empty invalid; do
  expect_ok "k3s installer download: ${scenario}" installer_download_case "${scenario}"
done

printf '%s\n' '{"error":"session_not_found"}' >"${TMP}/session-pending.json"
printf '%s\n' '{"error":"invalid_token"}' >"${TMP}/invalid-token.json"
printf '%s\n' '{"error":"missing_identity","detail":"session_not_found"}' >"${TMP}/missing-identity.json"
printf '%s\n' 'not json session_not_found' >"${TMP}/invalid-json.txt"
expect_ok "initialize waits for a newly enrolled session" mcpruntime_adapter_initialize_pending 401 "${TMP}/session-pending.json"
expect_fail "initialize rejects invalid token immediately" mcpruntime_adapter_initialize_pending 401 "${TMP}/invalid-token.json"
expect_fail "initialize rejects missing identity despite matching detail" mcpruntime_adapter_initialize_pending 401 "${TMP}/missing-identity.json"
expect_fail "initialize rejects malformed auth error" mcpruntime_adapter_initialize_pending 401 "${TMP}/invalid-json.txt"
expect_fail "initialize rejects a policy denial" mcpruntime_adapter_initialize_pending 403 "${TMP}/session-pending.json"
expect_fail "initialize does not retry success" mcpruntime_adapter_initialize_pending 200 "${TMP}/session-pending.json"
for status in 000 404 502 503; do
  expect_ok "initialize waits for route HTTP ${status}" mcpruntime_adapter_initialize_pending "${status}" "${TMP}/session-pending.json"
done

expect_ok "flag true" staging_flag_enabled true
expect_ok "flag 1" staging_flag_enabled 1
expect_ok "flag YES" staging_flag_enabled YES
expect_fail "flag false" staging_flag_enabled false
expect_fail "flag empty" staging_flag_enabled ""
expect_eq "url host https" "$(staging_url_host https://Platform.E2E.mcpruntime.org/api/v1)" platform.e2e.mcpruntime.org
expect_eq "url host with port" "$(staging_url_host registry.e2e.mcpruntime.org:443)" registry.e2e.mcpruntime.org
expect_eq "url host trailing dot" "$(staging_url_host https://mcp.e2e.mcpruntime.org./x)" mcp.e2e.mcpruntime.org

# --- suffix rules -------------------------------------------------------------
expect_ok "suffix match" staging_host_has_suffix platform.e2e.mcpruntime.org e2e.mcpruntime.org
expect_ok "suffix match nested" staging_host_has_suffix run-abc.e2e.mcpruntime.org .e2e.mcpruntime.org
expect_fail "suffix apex itself" staging_host_has_suffix e2e.mcpruntime.org e2e.mcpruntime.org
expect_fail "suffix production host" staging_host_has_suffix platform.mcpruntime.org e2e.mcpruntime.org
expect_fail "suffix lookalike label" staging_host_has_suffix evil-e2e.mcpruntime.org e2e.mcpruntime.org
expect_fail "suffix embedded" staging_host_has_suffix platform.e2e.mcpruntime.org.attacker.test e2e.mcpruntime.org
expect_ok "safe suffix" staging_suffix_is_safe e2e.mcpruntime.org mcpruntime.org
expect_ok "safe foreign suffix" staging_suffix_is_safe e2e.example.test mcpruntime.org
expect_fail "unsafe suffix equals production" staging_suffix_is_safe mcpruntime.org mcpruntime.org
expect_fail "unsafe suffix parent of production" staging_suffix_is_safe org mcpruntime.org
expect_fail "unsafe single-label suffix" staging_suffix_is_safe example.org mcpruntime.org

# --- guard with a fake resolver ------------------------------------------------
cat >"${TMP}/dns" <<'EOF'
platform.mcpruntime.org 198.51.100.14
registry.mcpruntime.org 198.51.100.14
mcp.mcpruntime.org 198.51.100.14
auth.mcpruntime.org 198.51.100.14
platform.e2e.mcpruntime.org 198.51.100.81
registry.e2e.mcpruntime.org 198.51.100.81
mcp.e2e.mcpruntime.org 198.51.100.81
auth.e2e.mcpruntime.org 198.51.100.81
hijacked.e2e.mcpruntime.org 198.51.100.14
vm.example.test 198.51.100.81
EOF
export STAGING_RESOLVE_TABLE="${TMP}/dns"
HOSTS=(platform.e2e.mcpruntime.org registry.e2e.mcpruntime.org mcp.e2e.mcpruntime.org auth.e2e.mcpruntime.org)

expect_ok "guard accepts the disposable VM by IP" staging_guard_hosts 198.51.100.81 "${HOSTS[@]}"
expect_ok "guard accepts the disposable VM by name" staging_guard_hosts vm.example.test "${HOSTS[@]}"
expect_ok "guard accepts on-VM run without VM host" staging_guard_hosts "" "${HOSTS[@]}"
expect_fail "guard refuses the production VM address" staging_guard_hosts 198.51.100.14 "${HOSTS[@]}"
expect_fail "guard refuses a production hostname as VM" staging_guard_hosts platform.mcpruntime.org "${HOSTS[@]}"
expect_fail "guard refuses a production hostname in the E2E host list" staging_guard_hosts 198.51.100.81 platform.mcpruntime.org
expect_fail "guard refuses an E2E host resolving to production" staging_guard_hosts 198.51.100.81 hijacked.e2e.mcpruntime.org
expect_fail "guard refuses an unresolvable E2E host" staging_guard_hosts 198.51.100.81 missing.e2e.mcpruntime.org
expect_fail "guard refuses a VM the E2E hosts do not point at" staging_guard_hosts 203.0.113.9 "${HOSTS[@]}"
expect_fail "guard refuses with no hosts" staging_guard_hosts 198.51.100.81
expect_fail "guard refuses a suffix override to production" \
  env E2E_DISPOSABLE_DOMAIN_SUFFIX=mcpruntime.org bash -c "source '${SCRIPT_DIR}/lib/staging.sh'; staging_guard_hosts 198.51.100.81 platform.mcpruntime.org"
grep -v '^platform.mcpruntime.org\|^registry.mcpruntime.org\|^mcp.mcpruntime.org\|^auth.mcpruntime.org' \
  "${TMP}/dns" >"${TMP}/dns-noprod"
expect_fail "guard fails closed when production does not resolve" \
  env STAGING_RESOLVE_TABLE="${TMP}/dns-noprod" bash -c "source '${SCRIPT_DIR}/lib/staging.sh'; staging_guard_hosts 198.51.100.81 platform.e2e.mcpruntime.org"
expect_ok "local ips distinct from production" staging_guard_local_ips 10.0.0.5 198.51.100.81
expect_fail "local ips include production" staging_guard_local_ips 10.0.0.5 198.51.100.14
expect_eq "no IP literal is hardcoded for production" \
  "$(grep -cE '([0-9]{1,3}\.){3}[0-9]{1,3}' "${SCRIPT_DIR}/lib/staging.sh" || true)" 0

# --- marker --------------------------------------------------------------------
staging_marker_content vm-e2e-01 >"${TMP}/marker"
expect_ok "marker valid" staging_marker_valid "${TMP}/marker" vm-e2e-01
expect_fail "marker missing" staging_marker_valid "${TMP}/absent" vm-e2e-01
expect_fail "marker for another host" staging_marker_valid "${TMP}/marker" other-vm
printf 'something else\nhostname=vm-e2e-01\nsuffix=e2e.mcpruntime.org\n' >"${TMP}/bad-magic"
expect_fail "marker bad magic" staging_marker_valid "${TMP}/bad-magic" vm-e2e-01
printf '%s\nhostname=vm-e2e-01\nsuffix=e2e.other.test\n' "${STAGING_MARKER_MAGIC}" >"${TMP}/bad-suffix"
expect_fail "marker for another suffix" staging_marker_valid "${TMP}/bad-suffix" vm-e2e-01
staging_marker_content devbox >"${TMP}/devbox"
expect_fail "marker on a production machine name" staging_marker_valid "${TMP}/devbox" devbox

# --- stage runner and summary ----------------------------------------------------
run_stage_suite() {
  local dir="$1"
  STAGING_LOG_PREFIX="test"
  staging_stages_init "${dir}" "${dir}/state.env"
  # shellcheck disable=SC2329 # invoked indirectly through staging_run_stage
  stage_ok() { echo "hello"; staging_state_set HANDOFF "from stage"; }
  # shellcheck disable=SC2329
  stage_soft_fail() { false; echo "not reached"; }
  # shellcheck disable=SC2329
  stage_skip() { staging_skip "not configured"; }
  # shellcheck disable=SC2329
  stage_uses_handoff() { [[ "${HANDOFF}" == "from stage" ]]; }
  # shellcheck disable=SC2329
  stage_critical_fail() { return 3; }
  staging_run_stage ok soft "n/a" stage_ok
  staging_run_stage soft-fail soft "a soft check broke" stage_soft_fail
  staging_run_stage skip soft "n/a" stage_skip
  staging_run_stage handoff soft "state file not sourced" stage_uses_handoff
  staging_run_stage critical critical "setup broke" stage_critical_fail
  staging_run_stage after-critical soft "n/a" stage_ok
  if staging_finish test-run >"${dir}/finish.out" 2>&1; then
    return 1
  fi
  return 0
}
STAGE_DIR="${TMP}/stages"
# Not inside `if`/`||`: bash ignores errexit for everything run from a
# conditional context, including the stage subshells, and the runners never
# call staging_run_stage that way either.
set +e
(run_stage_suite "${STAGE_DIR}") >"${TMP}/suite.out" 2>&1
suite_rc=$?
set -e
if [[ ${suite_rc} -eq 0 ]]; then
  pass "stage runner reports overall failure"
else
  failed "stage runner reports overall failure"
  cat "${TMP}/suite.out" >&2
fi
statuses="$(jq -r '[.stages[] | "\(.stage)=\(.status)"] | join(",")' "${STAGE_DIR}/summary.json")"
expect_eq "stage statuses" "${statuses}" \
  "ok=passed,soft-fail=failed,skip=skipped,handoff=passed,critical=failed,after-critical=skipped"
expect_eq "summary result" "$(jq -r .result "${STAGE_DIR}/summary.json")" failed
expect_eq "summary counts" "$(jq -c .counts "${STAGE_DIR}/summary.json")" '{"failed":2,"passed":2,"skipped":2}'
expect_eq "skip reason recorded" "$(jq -r '.stages[] | select(.stage=="skip") | .detail' "${STAGE_DIR}/summary.json")" "not configured"
expect_ok "stage log written" grep -q hello "${STAGE_DIR}/stages/01-ok.log"
expect_ok "errexit stops a stage body" bash -c "! grep -q 'not reached' '${STAGE_DIR}/stages/02-soft-fail.log'"
expect_ok "summary.md names the failure and hint" grep -q 'critical.*setup broke' "${STAGE_DIR}/summary.md"
expect_ok "failure summary printed" grep -q 'FAILED critical -- likely cause: setup broke' "${STAGE_DIR}/finish.out"

# --- adapter-certificate setup environment ---------------------------------------
adapter_env() {
  (
    unset MCP_ADAPTER_CERTIFICATES MCP_TRUST_DOMAIN MCP_DEFAULT_INGRESS_TLS_SECRET_NAMESPACE \
      E2E_ADAPTER_TRUST_DOMAIN E2E_MTLS_TRUST_DOMAIN E2E_DISPOSABLE_DOMAIN_SUFFIX
    # shellcheck disable=SC2163 # NAME=VALUE pairs
    export "$@"
    staging_configure_adapter_certificates
    printf '%s|%s|%s' "${MCP_ADAPTER_CERTIFICATES:-}" "${MCP_TRUST_DOMAIN:-}" "${MCP_DEFAULT_INGRESS_TLS_SECRET_NAMESPACE:-}"
  )
}
expect_eq "adapter certificates enabled with the workload issuer" \
  "$(adapter_env E2E_MTLS_CLUSTER_ISSUER=mcp-runtime-ca)" "true|e2e.mcpruntime.org|mcp-servers"
expect_eq "adapter certificates honor an explicit trust domain" \
  "$(adapter_env E2E_MTLS_CLUSTER_ISSUER=mcp-runtime-ca E2E_ADAPTER_TRUST_DOMAIN=staging.example.test)" \
  "true|staging.example.test|mcp-servers"
expect_eq "adapter certificates off without a workload issuer" \
  "$(adapter_env E2E_MTLS_CLUSTER_ISSUER= MCP_ADAPTER_CERTIFICATES=true)" "||"
expect_eq "adapter certificates opt-out" \
  "$(adapter_env E2E_MTLS_CLUSTER_ISSUER=mcp-runtime-ca E2E_ADAPTER_CERTIFICATES=false)" "||"

# --- diagnostics failed-check parsing -------------------------------------------
printf '\033[30;42m SUCCESS \033[0m ok check — fine\n\033[30;101m  ERROR  \033[0m \033[91msentinel OIDC configuration — tenant mode\033[0m\n\033[30;101m         \033[0m continuation line\n  ERROR   MCPServer reconcile smoke — timed out\n' >"${TMP}/diag.log"
expect_eq "failed checks parsed" "$(staging_failed_checks "${TMP}/diag.log" | paste -sd'|' -)" \
  "MCPServer reconcile smoke|sentinel OIDC configuration"

if [[ "${FAILURES}" -ne 0 ]]; then
  echo "${FAILURES} staging lib test(s) failed" >&2
  exit 1
fi
echo "all staging lib tests passed"
