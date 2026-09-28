#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/adapter-certificates.sh"
TEST_DIR="$(mktemp -d)"
trap 'rm -rf "${TEST_DIR}"' EXIT

# Dummy PEMs so adapter_certificate_credential_dir accepts the temp dir.
: >"${TEST_DIR}/client.crt"
: >"${TEST_DIR}/client.key"

run_case() (
  local name="$1" expected="$2" reason="$3" statuses="$4" want_rc="$5" want_calls="$6"
  local retry_server_errors="${8:-false}"
  export ADAPTER_CERT_PATH="${TEST_DIR}" ADAPTER_CERT_DIR="${TEST_DIR}" \
    OAUTH_SERVER_HOST=localhost MCP_PROTOCOL_VERSION=2025-06-18
  export ADAPTER_CERT_POLICY_WAIT_SECONDS="${7:-180}"
  printf '%s\n' "${statuses}" >"${TEST_DIR}/responses"
  : >"${TEST_DIR}/calls"
  curl() {
    local body="" headers="" response
    while [[ $# -gt 0 ]]; do
      case "$1" in
        -o) body="$2"; shift ;;
        -D) headers="$2"; shift ;;
        --cert) printf '%s\n' "$2" >>"${TEST_DIR}/certs-used" ;;
      esac
      shift
    done
    response="$(head -n 1 "${TEST_DIR}/responses")"
    tail -n +2 "${TEST_DIR}/responses" >"${TEST_DIR}/next"
    mv "${TEST_DIR}/next" "${TEST_DIR}/responses"
    printf 'call\n' >>"${TEST_DIR}/calls"
    [[ "${response}" != "curl-error" ]] || return 7
    printf 'mcp-session-id: test-session\r\n' >"${headers}"
    printf '%s' "${response#* }" >"${body}"
    printf '%s' "${response%% *}"
  }
  sleep() { :; }
  local rc=0
  wait_for_adapter_certificate_initialize https://localhost/mcp "${expected}" "${reason}" \
    "${TEST_DIR}/headers" "${TEST_DIR}/body" "${retry_server_errors}" >"${TEST_DIR}/output" 2>&1 || rc=$?
  local calls
  calls="$(wc -l <"${TEST_DIR}/calls" | tr -d ' ')"
  if [[ "${rc}" != "${want_rc}" || "${calls}" != "${want_calls}" ]]; then
    cat "${TEST_DIR}/output" >&2
    echo "[fail] ${name}: status=${rc}, calls=${calls}; want ${want_rc}/${want_calls}" >&2
    exit 1
  fi
  echo "[pass] adapter-certificate-${name}"
)

# Regression: enroll writes under <configDir>/certs/<hash>/; the wait helper
# must not curl the config root when ADAPTER_CERT_PATH is the scoped dir.
run_scoped_path_prefers_path_over_config_root() (
  local config_root="${TEST_DIR}/config-root"
  local scoped="${config_root}/certs/abcdef0123456789"
  mkdir -p "${scoped}"
  : >"${scoped}/client.crt"
  : >"${scoped}/client.key"
  # Deliberately leave no PEMs under the config root (the CI failure mode).
  export ADAPTER_CERT_DIR="${config_root}" ADAPTER_CERT_PATH="${scoped}" \
    OAUTH_SERVER_HOST=localhost MCP_PROTOCOL_VERSION=2025-06-18 \
    ADAPTER_CERT_POLICY_WAIT_SECONDS=180
  : >"${TEST_DIR}/calls"
  : >"${TEST_DIR}/certs-used"
  printf '%s\n' '200 {"result":{}}' >"${TEST_DIR}/responses"
  curl() {
    local body="" headers=""
    while [[ $# -gt 0 ]]; do
      case "$1" in
        -o) body="$2"; shift ;;
        -D) headers="$2"; shift ;;
        --cert) printf '%s\n' "$2" >>"${TEST_DIR}/certs-used" ;;
      esac
      shift
    done
    printf 'call\n' >>"${TEST_DIR}/calls"
    printf 'mcp-session-id: test-session\r\n' >"${headers}"
    printf '%s' '{"result":{}}' >"${body}"
    printf '200'
  }
  sleep() { :; }
  wait_for_adapter_certificate_initialize https://localhost/mcp 200 "" \
    "${TEST_DIR}/headers" "${TEST_DIR}/body" >/dev/null
  local used
  used="$(tr -d '\n' <"${TEST_DIR}/certs-used")"
  if [[ "${used}" != "${scoped}/client.crt" ]]; then
    echo "[fail] scoped-path: curl --cert was ${used:-<empty>}; want ${scoped}/client.crt" >&2
    exit 1
  fi
  echo "[pass] adapter-certificate-scoped-path-over-config-root"
)

run_parse_enroll_output() {
  local scoped="${TEST_DIR}/enroll-parse/certs/deadbeef"
  mkdir -p "${scoped}"
  : >"${scoped}/client.crt"
  : >"${scoped}/client.key"
  local out got
  out="$(printf 'issued spiffe://example/ns/mcp-servers/session/s1 (expires 2099-01-01T00:00:00Z)\nsaved certificate files in %s\nuse --tls-client-cert %s/client.crt\n' "${scoped}" "${scoped}")"
  got="$(parse_adapter_enroll_credential_dir "${out}")"
  if [[ "${got}" != "${scoped}" ]]; then
    echo "[fail] parse-enroll: got ${got:-<empty>}; want ${scoped}" >&2
    exit 1
  fi
  if parse_adapter_enroll_credential_dir "issued spiffe://x\n" >/dev/null 2>&1; then
    echo "[fail] parse-enroll: expected failure when enroll omits saved path" >&2
    exit 1
  fi
  local empty_dir="${TEST_DIR}/enroll-parse/empty"
  mkdir -p "${empty_dir}"
  if parse_adapter_enroll_credential_dir "saved certificate files in ${empty_dir}" >/dev/null 2>&1; then
    echo "[fail] parse-enroll: expected failure when PEM files are missing" >&2
    exit 1
  fi
  echo "[pass] adapter-certificate-parse-enroll-output"
}

run_case enrollment 200 '' $'401 {"error":"session_not_found"}\n200 {"result":{}}' 0 2
run_case revocation 401 session_revoked $'200 {"result":{}}\n401 {"error":"session_revoked"}' 0 2
run_case wrong-session 401 session_revoked '401 {"error":"session_not_found"}' 1 1
run_case revoked-at-enrollment 200 '' '401 {"error":"session_revoked"}' 1 1
run_case gateway-error 200 '' '503 {"error":"policy_unavailable"}' 1 1
run_case timeout 200 '' '401 {"error":"session_not_found"}' 1 1 0
run_case tls-error 200 '' curl-error 1 1
run_case route-converges 401 session_not_found $'curl-error\n500 Internal Server Error\n502 Bad Gateway\n401 {"error":"session_not_found"}' 0 4 180 true
run_case route-stays-unavailable 401 session_not_found '500 Internal Server Error' 1 1 0 true
run_case route-accepts-certificate 401 session_not_found '200 {"result":{}}' 1 1 180 true
run_scoped_path_prefers_path_over_config_root
run_parse_enroll_output
