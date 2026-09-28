#!/usr/bin/env bash

# Wait for the data plane to apply a session change. ConfigMap contents alone
# do not prove the mounted policy has been projected and reloaded. By default,
# retry only the expected previous state: a missing session during enrollment,
# or a still accepted certificate during revocation. The optional final
# argument also retries transient TLS-port-forward failures and HTTP 5xx while
# a newly created Traefik route converges. Never retry a tool invocation.
wait_for_adapter_certificate_initialize() {
  local url="$1" expected_status="$2" expected_error="$3" headers="$4" body="$5"
  local retry_transient_server_errors="${6:-false}"
  local oauth_token="${7:-${ADAPTER_OAUTH_TOKEN:-}}"
  local -a auth_args=()
  if [[ -n "${oauth_token}" ]]; then
    auth_args+=(-H "Authorization: Bearer ${oauth_token}")
  fi
  # Prefer the scoped enroll directory (…/certs/<hash>); fall back for unit tests
  # that still export ADAPTER_CERT_DIR with flat client.crt/client.key.
  local cert_dir="${ADAPTER_CERT_PATH:-${ADAPTER_CERT_DIR:-}}"
  if [[ -z "${cert_dir}" ]]; then
    echo "adapter certificate initialize: ADAPTER_CERT_PATH or ADAPTER_CERT_DIR is required" >&2
    return 1
  fi
  local deadline=$((SECONDS + ${ADAPTER_CERT_POLICY_WAIT_SECONDS:-180})) status error
  while true; do
    if ! status="$(curl -ksS --max-time 5 \
      --cert "${cert_dir}/client.crt" --key "${cert_dir}/client.key" \
      -D "${headers}" -o "${body}" -w '%{http_code}' \
      -H "Host: ${OAUTH_SERVER_HOST}" -H 'content-type: application/json' \
      -H 'accept: application/json, text/event-stream' -H "Mcp-Protocol-Version: ${MCP_PROTOCOL_VERSION}" \
      ${auth_args[@]+"${auth_args[@]}"} \
      --data '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' "${url}")"; then
      if [[ "${retry_transient_server_errors}" != "true" ]] || (( SECONDS >= deadline )); then
        return 1
      fi
      if declare -F recover_traefik_tls_port_forward_if_needed >/dev/null; then
        recover_traefik_tls_port_forward_if_needed || true
      fi
      sleep 2
      continue
    fi
    error=""
    if [[ "${status}" == "401" ]]; then
      error="$(python3 -c 'import json,sys; print(json.load(sys.stdin).get("error", ""))' <"${body}")" || return 1
    fi
    if [[ "${status}" == "${expected_status}" && "${error}" == "${expected_error}" ]]; then
      return 0
    fi
    if [[ "${retry_transient_server_errors}" == "true" && "${status}" =~ ^(500|502|503)$ ]]; then
      if (( SECONDS < deadline )); then
        sleep 2
        continue
      fi
    fi
    if (( SECONDS >= deadline )) || ! {
      [[ "${expected_status}" == "200" && "${status}" == "401" && "${error}" == "session_not_found" ]] ||
      [[ "${expected_error}" == "session_revoked" && "${status}" == "200" ]]
    }; then
      echo "adapter certificate initialize: expected ${expected_status} ${expected_error}, got ${status}: $(cat "${body}")" >&2
      return 1
    fi
    sleep 2
  done
}
