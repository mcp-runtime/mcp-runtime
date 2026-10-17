#!/usr/bin/env bash
# Loaded only by the guarded Kind QA adapter-certificates scenario.

cross_team_cli() {
  local actor_token="$1"
  shift
  env MCP_PLATFORM_API_URL="http://127.0.0.1:${PLATFORM_PORT}" \
    MCP_PLATFORM_API_TOKEN="${actor_token}" \
    MCP_RUNTIME_CONFIG_DIR="${CROSS_TEAM_CLI_CONFIG_DIR:-${WORKDIR}/cross-team-cli}" \
    "${PROJECT_ROOT}/bin/mcp-runtime" "$@"
}

cross_team_login() {
  local email="$1" password="$2"
  CROSS_LOGIN_EMAIL="${email}" CROSS_LOGIN_PASSWORD="${password}" python3 -c '
import json,os
print(json.dumps({"email":os.environ["CROSS_LOGIN_EMAIL"],"password":os.environ["CROSS_LOGIN_PASSWORD"]}))
' | curl -fsS -X POST -H 'content-type: application/json' --data-binary @- \
    "http://127.0.0.1:${API_SERVICE_PORT}/api/v1/auth/login" | python3 -c 'import json,sys; print(json.load(sys.stdin)["access_token"])'
}

cross_team_certificate_request() {
  local credentials="$1" url="$2" host="$3" body="$4" output="$5" headers="$6" session="${7:-}"
  local -a session_args=()
  [[ -z "${session}" ]] || session_args=(-H "Mcp-Session-Id: ${session}")
  curl -ksS --max-time 5 --cert "${credentials}/client.crt" --key "${credentials}/client.key" \
    -H "Host: ${host}" -H 'content-type: application/json' -H 'accept: application/json, text/event-stream' \
    -H "Mcp-Protocol-Version: ${MCP_PROTOCOL_VERSION}" ${session_args[@]+"${session_args[@]}"} \
    -D "${headers}" -o "${output}" -w '%{http_code}' --data "${body}" "${url}"
}

# A Ready workload can precede Traefik TLS/session convergence. Scope the
# recipient credentials and omit the preceding OAuth fixture's bearer.
cross_team_wait_for_initialize() {
  local credentials="$1" url="$2" host="$3" body="$4" output="$5" headers="$6"
  ADAPTER_CERT_PATH="${credentials}" OAUTH_SERVER_HOST="${host}" ADAPTER_OAUTH_TOKEN="" \
    ADAPTER_CERT_POLICY_WAIT_SECONDS=120 wait_for_adapter_certificate_initialize \
    "${url}" 200 "" "${headers}" "${output}" true "" "${body}"
}

cross_team_cleanup() {
  local status="$1" token="$2" namespace="$3" server="$4"
  if ! cross_team_cli "${token}" server delete "${server}" --namespace "${namespace}"; then
    echo "cross-team fixture retirement failed; retry supported CLI retirement" >&2
    [[ "${status}" -ne 0 ]] || status=1
  fi
  exit "${status}"
}

run_e2e_cross_team_scenario() (
  set -euo pipefail
  local context_name
  context_name="$(KUBECONFIG="${KUBECONFIG_FILE}" kubectl config current-context)"
  [[ "${context_name}" == "kind-${CLUSTER_NAME}" ]] || { echo "cross-team E2E requires its explicit Kind target" >&2; return 1; }
  ensure_api_port_forward
  ensure_gateway_port_forward
  ensure_traefik_tls_port_forward
  qa_platform_cli auth status >/dev/null
  local admin_token="${ADAPTER_PLATFORM_TOKEN}" run_id
  run_id="$(date +%s)"
  local team_a="e2e-delegate-a-${run_id}" team_b="e2e-delegate-b-${run_id}" team_c="e2e-delegate-c-${run_id}"
  local email_a="${team_a}@mcpruntime.org" email_b="${team_b}@mcpruntime.org" email_c="${team_c}@mcpruntime.org"
  local password="test-password-123" server_name="e2e-cross-team-${run_id}" grant_name="e2e-cross-team-grant"
  local metadata_dir="${WORKDIR}/cross-team-metadata" grant_file="${WORKDIR}/cross-team-grant.yaml"
  local token_a token_b token_c namespace_a team_a_id team_b_id agent_b agent_c subject_b credentials_b session_b

  log_line policy "three-team delegation: owner A grants member/agent B; unrelated C remains denied"
  for team in "${team_a}" "${team_b}" "${team_c}"; do
    cross_team_cli "${admin_token}" team create "${team}" --name "Cross-team ${team}" >/dev/null
  done
  printf '%s\n' "${password}" | cross_team_cli "${admin_token}" team user create "${team_a}" --email "${email_a}" --password-stdin --role owner >/dev/null
  printf '%s\n' "${password}" | cross_team_cli "${admin_token}" team user create "${team_b}" --email "${email_b}" --password-stdin --role member >/dev/null
  printf '%s\n' "${password}" | cross_team_cli "${admin_token}" team user create "${team_c}" --email "${email_c}" --password-stdin --role member >/dev/null
  token_a="$(cross_team_login "${email_a}" "${password}")"
  token_b="$(cross_team_login "${email_b}" "${password}")"
  token_c="$(cross_team_login "${email_c}" "${password}")"
  curl -fsS -H "Authorization: Bearer ${token_a}" "http://127.0.0.1:${PLATFORM_PORT}/api/v1/runtime/teams/${team_a}" >"${WORKDIR}/cross-team-authority.json"
  namespace_a="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["team"]["namespace"])' <"${WORKDIR}/cross-team-authority.json")"
  team_a_id="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["team"]["id"])' <"${WORKDIR}/cross-team-authority.json")"
  trap 'cross_team_cleanup "$?" "${admin_token}" "${namespace_a}" "${server_name}"' EXIT
  agent_b="$(cross_team_cli "${admin_token}" agent create "${team_b}" --name 'Cross-team grantee agent' | python3 -c 'import re,sys; text=sys.stdin.read(); match=re.search(r"\bagt_[0-9a-z]{26}\b",text); assert match,text; print(match.group(0))')"
  agent_c="$(cross_team_cli "${admin_token}" agent create "${team_c}" --name 'Unrelated agent' | python3 -c 'import re,sys; text=sys.stdin.read(); match=re.search(r"\bagt_[0-9a-z]{26}\b",text); assert match,text; print(match.group(0))')"
  curl -fsS -H "Authorization: Bearer ${token_a}" \
    "http://127.0.0.1:${PLATFORM_PORT}/api/v1/runtime/delegation-recipients?namespace=${namespace_a}&team=${team_b}" >"${WORKDIR}/cross-team-recipients.json"
  team_b_id="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["team"]["id"])' <"${WORKDIR}/cross-team-recipients.json")"
  subject_b="$(CROSS_MEMBER_TOKEN="${token_b}" python3 -c 'import base64,json,os; part=os.environ["CROSS_MEMBER_TOKEN"].split(".")[1]; print(json.loads(base64.urlsafe_b64decode(part+"="*((4-len(part)%4)%4)))["sub"])')"
  python3 - "${WORKDIR}/cross-team-recipients.json" "${agent_b}" "${subject_b}" <<'PY'
import json,sys
page=json.load(open(sys.argv[1]))
assert any(agent["id"]==sys.argv[2] for agent in page["agents"]), "owner A could not discover the known team-B agent"
assert any(member["user_id"]==sys.argv[3] for member in page["members"]), "owner A could not discover the known team-B member"
PY

  # Publish and deploy through the tenant management path, which provisions
  # the namespace, registry authorization and pull credentials itself.
  "${PROJECT_ROOT}/bin/mcp-runtime" server init "${server_name}" --metadata-dir "${metadata_dir}" \
    --image "${server_name}" --tag "${E2E_WORKLOAD_TAG}" --port 8088 --policy-mode allow-list \
    --default-decision deny --session-required=true --tool aaa-ping --tool echo --force
  "${PROJECT_ROOT}/bin/mcp-runtime" server validate --metadata-dir "${metadata_dir}"
  cross_team_cli "${token_a}" server push --scope tenant --image "${SERVER_IMAGE}" --name "${server_name}"
  cross_team_cli "${token_a}" server deploy "${server_name}" --scope tenant --team "${team_a}" --metadata-dir "${metadata_dir}"
  wait_for_named_server_ready "${server_name}" "${namespace_a}"

  "${PROJECT_ROOT}/bin/mcp-runtime" access grant init "${grant_name}" --namespace "${namespace_a}" \
    --server "${server_name}" --server-namespace "${namespace_a}" --human-id "${subject_b}" \
    --agent-id "${agent_b}" --team-id "${team_b_id}" --trust low --side-effect read \
    --tool-rule aaa-ping:allow:low --expires-in 1h --output "${grant_file}" --force
  cross_team_cli "${token_a}" access grant apply --file "${grant_file}"
  if CROSS_TEAM_CLI_CONFIG_DIR="${WORKDIR}/cross-team-c-cert" cross_team_cli "${token_c}" adapter enroll \
    --platform-url "http://127.0.0.1:${PLATFORM_PORT}" --server "${server_name}" --namespace "${namespace_a}" --agent "${agent_c}" \
    >"${WORKDIR}/cross-team-unrelated-enroll.log" 2>&1; then
    echo "unrelated team C obtained a certificate" >&2; return 1
  fi
  grep -q 'no enabled MCPAccessGrant' "${WORKDIR}/cross-team-unrelated-enroll.log"
  local enrollment
  enrollment="$(CROSS_TEAM_CLI_CONFIG_DIR="${WORKDIR}/cross-team-b-cert" cross_team_cli "${token_b}" adapter enroll \
    --platform-url "http://127.0.0.1:${PLATFORM_PORT}" --server "${server_name}" --namespace "${namespace_a}" --agent "${agent_b}")"
  credentials_b="$(parse_adapter_enroll_credential_dir "${enrollment}")"
  session_b="$(printf '%s\n' "${enrollment}" | sed -n 's#.*issued spiffe://[^ ]*/session/\([^ ]*\).*#\1#p')"
  [[ -n "${session_b}" ]] || { echo "cross-team enrollment did not report its session" >&2; return 1; }
  local host url status mcp_session deadline initialize_body
  host="$(kubectl get mcpserver "${server_name}" -n "${namespace_a}" -o jsonpath='{.spec.ingressHost}')"
  url="https://127.0.0.1:${TRAEFIK_TLS_PORT}/${server_name}/mcp"
  initialize_body="$(CROSS_PROTOCOL="${MCP_PROTOCOL_VERSION}" python3 -c 'import json,os; print(json.dumps({"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":os.environ["CROSS_PROTOCOL"],"capabilities":{},"clientInfo":{"name":"cross-team-e2e","version":"1"}}}))')"
  cross_team_wait_for_initialize "${credentials_b}" "${url}" "${host}" "${initialize_body}" \
    "${WORKDIR}/cross-team-initialize.json" "${WORKDIR}/cross-team-initialize.headers"
  mcp_session="$(awk 'BEGIN{IGNORECASE=1} /^Mcp-Session-Id:/{sub(/\r$/,"",$2);print $2}' "${WORKDIR}/cross-team-initialize.headers")"
  status="$(cross_team_certificate_request "${credentials_b}" "${url}" "${host}" '{"jsonrpc":"2.0","method":"notifications/initialized"}' "${WORKDIR}/cross-team-notification.json" "${WORKDIR}/cross-team-notification.headers" "${mcp_session}")"
  [[ "${status}" == "200" || "${status}" == "202" ]] || { echo "cross-team initialized notification failed: HTTP ${status}" >&2; return 1; }
  status="$(cross_team_certificate_request "${credentials_b}" "${url}" "${host}" '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"aaa-ping","arguments":{}}}' "${WORKDIR}/cross-team-allowed.json" "${WORKDIR}/cross-team-allowed.headers" "${mcp_session}")"
  if [[ "${status}" != "200" ]] || ! grep -q pong "${WORKDIR}/cross-team-allowed.json"; then
    echo "granted cross-team tool failed: HTTP ${status}" >&2; return 1
  fi
  status="$(cross_team_certificate_request "${credentials_b}" "${url}" "${host}" '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"message":"cross-team fixture"}}}' "${WORKDIR}/cross-team-denied.json" "${WORKDIR}/cross-team-denied.headers" "${mcp_session}")"
  if [[ "${status}" != "403" ]] || ! grep -q tool_not_granted "${WORKDIR}/cross-team-denied.json"; then
    echo "ungranted cross-team tool was not denied: HTTP ${status}" >&2; return 1
  fi
  cross_team_cli "${token_a}" access grant revoke-sessions "${grant_name}" --namespace "${namespace_a}"
  python3 "${PROJECT_ROOT}/test/e2e/cross_team_assertions.py" revoked --url "${url}" \
    --credentials "${credentials_b}" --host "${host}" --protocol "${MCP_PROTOCOL_VERSION}" --mcp-session "${mcp_session}"

  deadline=$((SECONDS+120))
  while true; do
    curl -fsS -H "Authorization: Bearer ${admin_token}" \
      "http://127.0.0.1:${PLATFORM_PORT}/api/v1/events?namespace=${namespace_a}&server=${server_name}&limit=100" >"${WORKDIR}/cross-team-events.json"
    if python3 "${PROJECT_ROOT}/test/e2e/cross_team_assertions.py" attribution --server "${server_name}" --session "${session_b}" \
      --subject-team "${team_b_id}" --resource-team "${team_a_id}" <"${WORKDIR}/cross-team-events.json"; then break; fi
    ((SECONDS<deadline)) || { echo "cross-team actor/authority analytics did not arrive" >&2; return 1; }
    sleep 2
  done
  log_line policy "three-team certificate delegation, denial, revocation and persisted attribution passed"
)
