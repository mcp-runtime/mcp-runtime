# shellcheck shell=bash
# Internal Staging assertions. The caller has already passed the disposable
# target guard and created the multitenancy fixtures through the supported CLI.

# ConfigMap projection and the gateway's 5–7s polling happen independently of
# anonymous initialize. Retry only the expected pre-publication grant miss;
# identity failures, different denials and malformed successes fail immediately.
staging_adapter_wait_granted_tool() {
  local credential_dir="$1" url="$2" body="$3" token="$4" code
  local deadline=$((SECONDS + ${STAGING_ADAPTER_POLICY_WAIT_SECONDS:-240}))
  while true; do
    code="$(staging_adapter_mcp_post "${credential_dir}" "${url}" \
      '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"aaa-ping","arguments":{}}}' "${body}" "${token}")" || return 1
    if [[ "${code}" == 200 ]]; then
      ((SECONDS <= deadline)) || return 1
      jq -e '.jsonrpc == "2.0" and .id == 2 and .error == null and .result.isError != true and any(.result.content[]?; .type == "text" and .text == "pong")' "${body}" >/dev/null
      return $?
    fi
    if [[ "${code}" != 403 ]] || ! jq -e '.error == "no_matching_grant"' "${body}" >/dev/null; then
      staging_err "granted tool returned unexpected HTTP ${code}"
      return 1
    fi
    if ((SECONDS >= deadline)); then
      staging_adapter_capture_policy_status "${credential_dir}" "${url}" || true
      staging_err "granted tool still has no matching grant after ${STAGING_ADAPTER_POLICY_WAIT_SECONDS:-240}s"
      return 1
    fi
    sleep 2
  done
}

# Read-only diagnostics over a local port-forward; use the enrolled certificate
# and workload CA with the gateway Service DNS name, preserving TLS validation.
staging_adapter_capture_policy_status() (
  local credential_dir="$1" url="$2" server namespace="${ns:-mcp-servers}" port pid i
  server="${url%/mcp}"
  server="${server##*/}"
  [[ "${server}" =~ ^[a-z0-9][a-z0-9-]*$ && "${namespace}" =~ ^[a-z0-9][a-z0-9-]*$ ]] || return 1
  local out="${STAGING_ARTIFACT_DIR}/adapter-enrollment"
  mkdir -p "${out}" || return 1
  kubectl -n "${namespace}" get configmap "${server}-gateway-policy" -o jsonpath='{.data.policy\.json}' |
    jq '{schema_version,revision}' >"${out}/${server}-rendered-revision.json" || return 1
  kubectl -n "${namespace}" port-forward --address=127.0.0.1 "svc/${server}" :80 >"${out}/${server}-status-forward.log" 2>&1 &
  pid=$!
  trap 'kill "${pid}" 2>/dev/null || true; wait "${pid}" 2>/dev/null || true' EXIT
  for ((i = 0; i < 5; i++)); do
    port="$(sed -n 's/^Forwarding from 127\.0\.0\.1:\([0-9]*\) -> .*/\1/p' "${out}/${server}-status-forward.log" | head -1)"
    [[ -z "${port}" ]] || break
    sleep 1
  done
  [[ "${port}" =~ ^[0-9]+$ ]] || return 1
  curl --silent --show-error --fail --max-time 10 --noproxy '*' \
    --connect-to "${server}.${namespace}.svc:443:127.0.0.1:${port}" \
    --cacert "${credential_dir}/ca.crt" --cert "${credential_dir}/client.crt" --key "${credential_dir}/client.key" \
    "https://${server}.${namespace}.svc/config/status" >"${out}/${server}-active-policy-status.json"
)

staging_monitor_cli() {
  local profile="$1"
  shift
  KUBECONFIG="" MCP_RUNTIME_CONFIG_DIR="${STAGING_MT_CONFIG_DIR}" \
    MCP_PLATFORM_API_TOKEN="" MCP_PLATFORM_API_URL="${PLATFORM_URL}" \
    MCP_PLATFORM_API_PROFILE="${profile}" "${BIN}" "$@"
}

staging_monitor_apply() {
  local mode="$1" grant="$2" manifest="$3"
  staging_monitor_cli "acme-owner-${MT_RUN_ID}" access grant init "${grant}" \
    --namespace "${MT_ACME_NS}" --server "${MT_ACME_SERVER}" \
    --server-namespace "${MT_ACME_NS}" --team-id "${MT_GLOBEX_TEAM_ID}" \
    --agent-id "${MT_GLOBEX_AGENT_ID}" --mode "${mode}" --trust low \
    --side-effect read --tool-rule add:deny:low --expires-in 30m \
    --output "${manifest}" --force >/dev/null || return 1
  staging_monitor_cli "acme-owner-${MT_RUN_ID}" access grant apply --file "${manifest}" >/dev/null || return 1
  local deadline=$((SECONDS + 120))
  while ((SECONDS < deadline)); do
    if staging_monitor_cli "acme-owner-${MT_RUN_ID}" access grant get "${grant}" --namespace "${MT_ACME_NS}" |
        jq -e --arg mode "${mode}" '.mode == $mode' >/dev/null &&
      kubectl -n "${MT_ACME_NS}" get configmap "${MT_ACME_SERVER}-gateway-policy" -o jsonpath='{.data.policy\.json}' |
        jq -e --arg name "${grant}" --arg mode "${mode}" '.grants | any(.name == $name and .mode == $mode)' >/dev/null; then
      return 0
    fi
    sleep 2
  done
  staging_err "grant ${grant} did not converge to ${mode} through the API and rendered policy"
  return 1
}

staging_monitor_post() {
  local dir="$1" credential_dir="$2" mcp_session="$3" body="$4"
  local args=()
  if [[ "${credential_dir}" != - ]]; then
    args+=(--cert "${credential_dir}/client.crt" --key "${credential_dir}/client.key")
  fi
  if [[ -n "${mcp_session}" ]]; then args+=(-H "Mcp-Session-Id: ${mcp_session}"); fi
  curl --silent --show-error --max-time 15 "${args[@]}" \
    -D "${dir}/response.headers" -o "${dir}/response.json" --write-out '%{http_code}' \
    -H 'content-type: application/json' -H 'accept: application/json, text/event-stream' \
    -H 'Mcp-Protocol-Version: 2025-06-18' --data "${body}" "${MCP_URL}/${MT_ACME_SERVER}/mcp"
}

staging_monitor_event() {
  local input="$1" trace="$2" mode="$3"
  jq -e --arg trace "${trace}" --arg mode "${mode}" --arg ns "${MT_ACME_NS}" \
    --arg server "${MT_ACME_SERVER}" --arg agent "${MT_GLOBEX_AGENT_ID}" \
    --arg session "${STAGING_MONITOR_SESSION}" '
    [.events[] | select(.trace_id == $trace and .namespace == $ns and .server == $server
      and .agent_id == $agent and .session_id == $session and .tool_name == "add")
      | . as $event | (.payload | if type == "string" then fromjson else . end) as $payload
      | select(if $mode == "monitor" then
          $event.decision == "allow" and $payload.status == 200 and $payload.would_deny == true
            and $payload.would_deny_reason == "tool_denied"
        else $event.decision == "deny" and $payload.status == 403 and $payload.reason == "tool_denied"
            and $payload.would_deny != true end)
      | {trace_id, namespace, server, agent_id, session_id, tool_name, decision,
         status: $payload.status, reason: $payload.reason,
         would_deny: ($payload.would_deny // false), would_deny_reason: $payload.would_deny_reason}]
    | if length > 0 then .[0] else empty end' "${input}"
}

staging_monitor_expect_call() {
  local dir="$1" credential_dir="$2" mcp_session="$3" mode="$4" evidence="$5"
  local deadline=$((SECONDS + 120)) code trace audit_attempt
  local call='{"jsonrpc":"2.0","id":307,"method":"tools/call","params":{"name":"add","arguments":{"a":5,"b":7}}}'
  while ((SECONDS < deadline)); do
    code="$(staging_monitor_post "${dir}" "${credential_dir}" "${mcp_session}" "${call}")" || return 1
    if [[ "${mode}:${code}" == monitor:200 || "${mode}:${code}" == enforce:403 ]]; then
      if [[ "${mode}" == monitor ]] && ! jq -e '.jsonrpc == "2.0" and .id == 307 and .error == null and .result.isError != true and any(.result.content[]?; .type == "text" and .text == "12")' "${dir}/response.json" >/dev/null; then
        staging_err "monitor call did not return the expected add result"
        return 1
      fi
      trace="$(awk 'tolower($1)=="x-trace-id:" {gsub(/\r/,"",$2); print $2}' "${dir}/response.headers" | tail -1)"
      [[ "${trace}" =~ ^[0-9a-f]{32}$ ]] || { staging_err "gateway response has no usable trace ID"; return 1; }
      for ((audit_attempt = 0; audit_attempt < 5; audit_attempt++)); do
        if staging_api_curl --fail --max-time 10 --get \
          --data-urlencode "trace_id=${trace}" --data-urlencode "namespace=${MT_ACME_NS}" \
          --data-urlencode "server=${MT_ACME_SERVER}" --data-urlencode event_type=mcp.request \
          --data-urlencode limit=20 "${PLATFORM_URL}/api/v1/events" >"${dir}/events.json" &&
          staging_monitor_event "${dir}/events.json" "${trace}" "${mode}" >"${evidence}" && ((SECONDS <= deadline)); then
          staging_log "${mode} add call: HTTP ${code}; matching trace/session audit verified"
          return 0
        fi
        ((SECONDS < deadline)) || break
        sleep 2
      done
    fi
    sleep 2
  done
  staging_err "${mode} call/audit did not converge within 120s (last HTTP ${code:-none})"
  return 1
}

staging_monitor_cleanup() {
  local status=$?
  trap - EXIT
  if [[ -n "${STAGING_MONITOR_GRANT:-}" ]]; then
    staging_monitor_cli "acme-owner-${MT_RUN_ID}" access grant delete "${STAGING_MONITOR_GRANT}" --namespace "${MT_ACME_NS}" >/dev/null 2>&1 || status=1
  fi
  if [[ "${STAGING_MONITOR_KEEP_SESSION:-0}" != 1 && -s "${STAGING_MONITOR_SESSION_FILE:-/dev/null}" ]]; then
    local session
    while IFS= read -r session; do
      [[ -n "${session}" ]] || continue
      staging_monitor_cli admin access session delete "${session}" --namespace "${MT_ACME_NS}" >/dev/null 2>&1 || status=1
    done <"${STAGING_MONITOR_SESSION_FILE}"
  fi
  exit "${status}"
}

staging_check_grant_monitor() (
  [[ "$(staging_stage_status multitenancy)" == passed ]] || staging_skip "needs the CLI-created multitenancy fixtures"
  staging_mt_names
  staging_mt_read_agents || return 1
  local dir="${WORK_DIR}/grant-monitor" evidence="${STAGING_ARTIFACT_DIR}/grant-monitor"
  mkdir -p "${dir}" "${evidence}" || return 1
  chmod 700 "${dir}" || return 1
  STAGING_MONITOR_GRANT="monitor-${MT_RUN_ID}"
  STAGING_MONITOR_SESSION_FILE="${dir}/sessions"
  STAGING_MONITOR_KEEP_SESSION=0
  : >"${STAGING_MONITOR_SESSION_FILE}"
  trap staging_monitor_cleanup EXIT
  staging_monitor_apply monitor "${STAGING_MONITOR_GRANT}" "${dir}/grant.yaml" || return 1
  local human_token credential_dir session_json mcp_session code
  human_token="$(jq -er --arg p "${MT_GLOBEX_PROFILE}" '.accounts[$p].token' "${MT_CONFIG}")" || return 1
  staging_adapter_enroll "${dir}/certs" "${human_token}" "${MT_ACME_SERVER}" "${MT_ACME_NS}" \
    "${MT_GLOBEX_AGENT_ID}" "$(staging_adapter_trust_domain)" "${STAGING_MONITOR_SESSION_FILE}" || return 1
  credential_dir="$(cat "${dir}/certs/.credential_dir")" || return 1
  STAGING_MONITOR_SESSION="$(tail -1 "${STAGING_MONITOR_SESSION_FILE}")" || return 1
  [[ -n "${credential_dir}" && -n "${STAGING_MONITOR_SESSION}" ]] || return 1
  session_json="$(staging_monitor_cli admin access session get "${STAGING_MONITOR_SESSION}" --namespace "${MT_ACME_NS}")" || return 1
  local init='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"staging-grant-monitor","version":"1"}}}'
  local deadline=$((SECONDS + 120))
  while true; do
    code="$(staging_monitor_post "${dir}" "${credential_dir}" "" "${init}")" || return 1
    [[ "${code}" != 200 ]] || break
    ((SECONDS < deadline)) || { staging_err "monitor enrollment did not initialize (HTTP ${code})"; return 1; }
    sleep 2
  done
  mcp_session="$(awk 'tolower($1)=="mcp-session-id:" {gsub(/\r/,"",$2); print $2}' "${dir}/response.headers" | tail -1)"
  [[ -n "${mcp_session}" ]] || { staging_err "initialize returned no MCP session"; return 1; }
  code="$(staging_monitor_post "${dir}" "${credential_dir}" "${mcp_session}" '{"jsonrpc":"2.0","method":"notifications/initialized"}')" || return 1
  [[ "${code}" == 200 || "${code}" == 202 || "${code}" == 204 ]] || return 1
  staging_monitor_expect_call "${dir}" "${credential_dir}" "${mcp_session}" monitor "${evidence}/monitor.json" || return 1
  code="$(staging_monitor_post "${dir}" - "${mcp_session}" '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"add","arguments":{"a":5,"b":7}}}')" || return 1
  if [[ "${code}" != 401 ]] || ! jq -e '.error == "missing_identity"' "${dir}/response.json" >/dev/null; then
    staging_err "monitor grant bypassed the identity gate (HTTP ${code})"
    return 1
  fi
  staging_monitor_apply enforce "${STAGING_MONITOR_GRANT}" "${dir}/grant.yaml" || return 1
  staging_monitor_expect_call "${dir}" "${credential_dir}" "${mcp_session}" enforce "${evidence}/enforce.json" || return 1
  local user_id
  user_id="$(jq -er '.subject.humanID' <<<"${session_json}")" || return 1
  staging_state_set STAGING_MONITOR_CREDENTIAL_DIR "${credential_dir}" || return 1
  staging_state_set STAGING_MONITOR_SESSION "${STAGING_MONITOR_SESSION}" || return 1
  staging_state_set STAGING_MONITOR_MCP_SESSION "${mcp_session}" || return 1
  staging_state_set STAGING_MONITOR_USER_ID "${user_id}" || return 1
  STAGING_MONITOR_KEEP_SESSION=1
)

staging_check_account_deactivation() (
  [[ "$(staging_stage_status grant-monitor)" == passed ]] || staging_skip "needs the monitor stage's enrolled user session"
  staging_mt_names
  staging_mt_read_agents || return 1
  STAGING_MONITOR_SESSION_FILE="${WORK_DIR}/grant-monitor/sessions"
  STAGING_MONITOR_GRANT=""
  STAGING_MONITOR_KEEP_SESSION=0
  trap staging_monitor_cleanup EXIT
  if ! staging_monitor_cli admin user deactivate --help >/dev/null 2>&1; then
    staging_skip "this ref has no supported account deactivation command"
  fi
  local dir="${WORK_DIR}/grant-monitor" evidence="${STAGING_ARTIFACT_DIR}/grant-monitor"
  local code deadline human_token
  [[ "${STAGING_MONITOR_USER_ID}" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]] || return 1
  # Wait for temporary enforcing-grant deletion before measuring revocation.
  deadline=$((SECONDS + 120))
  while true; do
    code="$(staging_monitor_post "${dir}" "${STAGING_MONITOR_CREDENTIAL_DIR}" "${STAGING_MONITOR_MCP_SESSION}" '{"jsonrpc":"2.0","id":536,"method":"tools/call","params":{"name":"add","arguments":{"a":5,"b":7}}}')" || return 1
    if [[ "${code}" == 200 ]] && jq -e '.jsonrpc == "2.0" and .id == 536 and .error == null and .result.isError != true and any(.result.content[]?; .type == "text" and .text == "12")' "${dir}/response.json" >/dev/null; then break; fi
    ((SECONDS < deadline)) || { staging_err "user call never recovered after temporary grant cleanup"; return 1; }
    sleep 2
  done
  staging_monitor_cli admin user deactivate "${STAGING_MONITOR_USER_ID}" --yes --json >"${evidence}/deactivation.json" || return 1
  deadline=$((SECONDS + 5))
  jq -e --arg id "${STAGING_MONITOR_USER_ID}" '.user_id == $id and .status == "deactivated" and .revoked_sessions >= 1' "${evidence}/deactivation.json" >/dev/null || return 1
  human_token="$(jq -er --arg p "${MT_GLOBEX_PROFILE}" '.accounts[$p].token' "${MT_CONFIG}")" || return 1
  code="$(staging_http_code --max-time 10 -H "authorization: Bearer ${human_token}" -H "x-api-key: ${human_token}" "${PLATFORM_URL}/api/v1/user/me")" || return 1
  [[ "${code}" == 401 || "${code}" == 403 ]] || { staging_err "deactivated user's token returned HTTP ${code}"; return 1; }
  while true; do
    code="$(staging_monitor_post "${dir}" "${STAGING_MONITOR_CREDENTIAL_DIR}" "${STAGING_MONITOR_MCP_SESSION}" '{"jsonrpc":"2.0","id":537,"method":"tools/call","params":{"name":"add","arguments":{"a":5,"b":7}}}')" || return 1
    if ((SECONDS <= deadline)) && [[ "${code}" == 401 || "${code}" == 403 ]] && jq -e '.error == "session_revoked" or .error == "session_not_found"' "${dir}/response.json" >/dev/null; then break; fi
    ((SECONDS < deadline)) || { staging_err "deactivated user's existing certificate/session was not revoked within 5s"; return 1; }
    sleep 0.2
  done
  staging_monitor_cli admin user deactivate "${STAGING_MONITOR_USER_ID}" --yes --json >"${evidence}/deactivation-repeat.json" || return 1
  jq -e '.already_deactivated == true and .revoked_sessions == 0 and .revoked_api_keys == 0 and .revoked_registry_credentials == 0 and .revoked_refresh_tokens == 0 and .removed_memberships == 0' "${evidence}/deactivation-repeat.json" >/dev/null || return 1
  staging_log "CLI-created user: enrollment succeeded; deactivation denied saved token and existing certificate/session; repeat was idempotent"
)
