#!/usr/bin/env bash
# Loaded only by the guarded Kind QA credential-broker scenario.
#
# NOT RUN AGAINST A CLUSTER when authored (the authoring VM cannot run Kind).
# Treat the first CI/Staging result as its first real validation.
#
# Supported provider: Kubernetes Secret only. The seeded backing Secret stands in
# for the external secret manager that owns the value; it is not product state.
# All product state (source, binding, server, grant, session) uses the CLI.
#
# Needs a verified HTTPS platform origin that gateway system roots trust, passed
# as E2E_CREDENTIAL_BROKER_URL. Default Kind test-mode has no such origin (HTTP
# ingress, no trusted CA wiring for the gateway), so without it the scenario
# prints a loud SKIPPED notice and succeeds. Run it where that origin exists.
#
# Proves, through the real CLI and gateway:
#   1. the brokered value reaches only the bound server and tool, in the
#      reserved header, same-Pod hop (checked by SHA-256 fingerprint);
#   2. it never appears in client responses, CLI output, audit rows or logs;
#   3. a client-forged reserved header is stripped (bound and unbound tools);
#   4. a reserved response header from the server is stripped;
#   5. an unbound tool and an unbound second server receive nothing;
#   6. a deleted binding fails closed, then a revoked session is denied;
#   7. metadata-only audit rows exist for source/binding writes and runtime use.

CREDENTIAL_BROKER_ASSERT="${PROJECT_ROOT}/test/e2e/credential_broker_assertions.py"
CREDENTIAL_BROKER_HEADER="X-Mcp-Credential-Token"

broker_call() {
  # broker_call CREDS URL HOST SESSION OUT HEADERS BODY [extra curl args...]
  local creds="$1" url="$2" host="$3" session="$4" out="$5" headers="$6" body="$7"
  shift 7
  curl -ksS --max-time 10 --cert "${creds}/client.crt" --key "${creds}/client.key" \
    -H "Host: ${host}" -H 'content-type: application/json' -H 'accept: application/json, text/event-stream' \
    -H "Mcp-Protocol-Version: ${MCP_PROTOCOL_VERSION}" ${session:+-H "Mcp-Session-Id: ${session}"} "$@" \
    -D "${headers}" -o "${out}" -w '%{http_code}' --data "${body}" "${url}"
}

broker_tool_body() {
  printf '{"jsonrpc":"2.0","id":%s,"method":"tools/call","params":{"name":"%s","arguments":{}}}' "$1" "$2"
}

broker_deploy_fixture() {
  # broker_deploy_fixture NAME TOKEN TEAM NAMESPACE METADATA_DIR BINDINGS(yes|no)
  local name="$1" token="$2" team="$3" namespace="$4" dir="$5" bound="$6"
  "${PROJECT_ROOT}/bin/mcp-runtime" server init "${name}" --metadata-dir "${dir}" --image "${name}" \
    --tag "${E2E_WORKLOAD_TAG}" --port 8088 --policy-mode allow-list --default-decision deny \
    --session-required=true --tool fingerprint --tool leak-response --tool other --force
  if [[ "${bound}" == "yes" ]]; then
    # Documented workflow: add credentialBindings to the server's entry.
    sed -i "0,/^  - name: ${name}\$/s//&\n    credentialBindings: [upstream-token]/" "${dir}/servers.yaml"
    grep -q 'credentialBindings: \[upstream-token\]' "${dir}/servers.yaml"
  fi
  "${PROJECT_ROOT}/bin/mcp-runtime" server validate --metadata-dir "${dir}"
  "${PROJECT_ROOT}/bin/mcp-runtime" server build image "${name}" --metadata-dir "${dir}" \
    --dockerfile "${PROJECT_ROOT}/test/e2e/fixtures/credential-echo/Dockerfile" \
    --registry registry.registry.svc.cluster.local:5000 --tag "${E2E_WORKLOAD_TAG}" \
    --context "${PROJECT_ROOT}/test/e2e/fixtures/credential-echo"
  load_image_into_kind "registry.registry.svc.cluster.local:5000/${name}:${E2E_WORKLOAD_TAG}"
  cross_team_cli "${token}" server push --scope tenant \
    --image "registry.registry.svc.cluster.local:5000/${name}:${E2E_WORKLOAD_TAG}" --name "${name}"
  cross_team_cli "${token}" server deploy "${name}" --scope tenant --team "${team}" --metadata-dir "${dir}"
  wait_for_named_server_ready "${name}" "${namespace}"
}

broker_initialize() {
  # broker_initialize CREDS URL HOST PREFIX -> prints the MCP session id
  local creds="$1" url="$2" host="$3" prefix="$4" body
  body="$(CB_PROTOCOL="${MCP_PROTOCOL_VERSION}" python3 -c 'import json,os; print(json.dumps({"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":os.environ["CB_PROTOCOL"],"capabilities":{},"clientInfo":{"name":"credential-broker-e2e","version":"1"}}}))')"
  cross_team_wait_for_initialize "${creds}" "${url}" "${host}" "${body}" "${prefix}-init.json" "${prefix}-init.headers"
  local session
  session="$(awk 'BEGIN{IGNORECASE=1} /^Mcp-Session-Id:/{sub(/\r$/,"",$2);print $2}' "${prefix}-init.headers")"
  broker_call "${creds}" "${url}" "${host}" "${session}" "${prefix}-note.json" "${prefix}-note.headers" \
    '{"jsonrpc":"2.0","method":"notifications/initialized"}' >/dev/null
  printf '%s' "${session}"
}

run_e2e_credential_broker_scenario() (
  set -euo pipefail
  if [[ -z "${E2E_CREDENTIAL_BROKER_URL:-}" ]]; then
    echo "[skip] credential-broker: SKIPPED, NOT TESTED. Set E2E_CREDENTIAL_BROKER_URL to a verified HTTPS platform origin trusted by gateway system roots (default Kind test-mode has none)." >&2
    echo "::warning::credential-broker scenario skipped: no verified HTTPS broker origin" >&2
    return 0
  fi
  [[ "${E2E_CREDENTIAL_BROKER_URL}" == https://* ]] || { echo "E2E_CREDENTIAL_BROKER_URL must be https://" >&2; return 1; }
  local context_name
  context_name="$(KUBECONFIG="${KUBECONFIG_FILE}" kubectl config current-context)"
  [[ "${context_name}" == "kind-${CLUSTER_NAME}" ]] || { echo "credential-broker E2E requires its explicit Kind target" >&2; return 1; }
  ensure_api_port_forward
  ensure_gateway_port_forward
  ensure_traefik_tls_port_forward
  qa_platform_cli auth status >/dev/null

  local admin_token="${ADAPTER_PLATFORM_TOKEN}" run_id team email password="test-password-123"
  run_id="$(date +%s)"
  team="e2e-broker-${run_id}"
  email="${team}@mcpruntime.org"
  local bound="e2e-broker-${run_id}" other="e2e-broker-other-${run_id}"
  local dir="${WORKDIR}/credential-broker" secret_file="${WORKDIR}/credential-broker.secret"
  mkdir -p "${dir}"
  (umask 077; openssl rand -hex 24 >"${secret_file}")

  cross_team_cli "${admin_token}" team create "${team}" --name "Credential broker ${team}" >/dev/null
  printf '%s\n' "${password}" | cross_team_cli "${admin_token}" team user create "${team}" --email "${email}" --password-stdin --role owner >/dev/null
  local token namespace team_id subject agent
  token="$(cross_team_login "${email}" "${password}")"
  curl -fsS -H "Authorization: Bearer ${token}" "http://127.0.0.1:${PLATFORM_PORT}/api/v1/runtime/teams/${team}" >"${dir}/team.json"
  namespace="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["team"]["namespace"])' <"${dir}/team.json")"
  team_id="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["team"]["id"])' <"${dir}/team.json")"
  subject="$(CB_TOKEN="${token}" python3 -c 'import base64,json,os; part=os.environ["CB_TOKEN"].split(".")[1]; print(json.loads(base64.urlsafe_b64decode(part+"="*((4-len(part)%4)%4)))["sub"])')"
  agent="$(cross_team_cli "${admin_token}" agent create "${team}" --name 'Credential broker agent' | python3 -c 'import re,sys; m=re.search(r"\bagt_[0-9a-z]{26}\b",sys.stdin.read()); assert m; print(m.group(0))')"
  trap 'for s in "${bound}" "${other}"; do cross_team_cli "${admin_token}" server delete "${s}" --namespace "${namespace}" || true; done' EXIT

  log_line policy "credential broker: configure the verified origin through the supported CLI"
  "${PROJECT_ROOT}/bin/mcp-runtime" credential configure --broker-url "${E2E_CREDENTIAL_BROKER_URL}" \
    --kubeconfig "${KUBECONFIG_FILE}" --context "${context_name}" --yes >"${dir}/configure.out" 2>&1

  # Backing value, owned by the "external secret manager" (here: a Secret).
  kubectl -n "${namespace}" create secret generic upstream-api-token --from-file=token="${secret_file}" >/dev/null

  broker_deploy_fixture "${bound}" "${token}" "${team}" "${namespace}" "${dir}/bound" yes
  broker_deploy_fixture "${other}" "${token}" "${team}" "${namespace}" "${dir}/other" no

  local uid
  uid="$(cross_team_cli "${token}" server get "${bound}" --namespace "${namespace}" | python3 -c 'import re,sys; m=re.search(r"uid\W+([0-9a-fA-F-]{36})",sys.stdin.read()); assert m,"server uid missing from server get"; print(m.group(1))')"

  python3 - "${dir}" "${namespace}" "${team_id}" "${subject}" "${agent}" "${uid}" "${bound}" <<'PY'
import json, sys
d, ns, team, human, agent, uid, server = sys.argv[1:]
json.dump({"namespace": ns, "name": "upstream-provider", "provider": "kubernetes", "resource": "upstream-api-token", "key": "token", "team_id": team, "revision": 0}, open(d + "/source.json", "w"))
json.dump({"namespace": ns, "name": "upstream-token", "source": "upstream-provider", "server": server, "server_uid": uid, "tools": ["fingerprint", "leak-response"], "human_id": human, "agent_id": agent, "team_id": team, "header": "X-Mcp-Credential-Token", "revision": 0}, open(d + "/binding.json", "w"))
PY
  log_line policy "credential broker: source and binding through the supported CLI (metadata only)"
  cross_team_cli "${admin_token}" credential source apply --file "${dir}/source.json" >"${dir}/source-apply.out" 2>&1
  "${PROJECT_ROOT}/bin/mcp-runtime" credential source authorize-kubernetes upstream-provider --namespace "${namespace}" \
    --kubeconfig "${KUBECONFIG_FILE}" --context "${context_name}" --yes >"${dir}/authorize.out" 2>&1
  cross_team_cli "${token}" credential binding apply --file "${dir}/binding.json" >"${dir}/binding-apply.out" 2>&1
  cross_team_cli "${token}" credential binding list --namespace "${namespace}" >"${dir}/binding-list.out" 2>&1
  cross_team_cli "${token}" credential binding get upstream-token --namespace "${namespace}" >"${dir}/binding-get.out" 2>&1
  cross_team_cli "${admin_token}" credential source get upstream-provider --namespace "${namespace}" >"${dir}/source-get.out" 2>&1

  # A binding for a server UID that does not exist must be refused.
  python3 - "${dir}" <<'PY'
import json, sys
d = sys.argv[1]
b = json.load(open(d + "/binding.json"))
b.update({"name": "wrong-server", "server_uid": "00000000-0000-0000-0000-000000000000", "revision": 0})
json.dump(b, open(d + "/wrong-binding.json", "w"))
PY
  if cross_team_cli "${token}" credential binding apply --file "${dir}/wrong-binding.json" >"${dir}/wrong-binding.out" 2>&1; then
    echo "binding for a non-existent server UID was accepted" >&2; return 1
  fi
  # Cloud providers are not supported: a gcp source must be refused readably.
  python3 - "${dir}" "${namespace}" "${team_id}" <<'PY'
import json, sys
d, ns, team = sys.argv[1:]
json.dump({"namespace": ns, "name": "cloud", "provider": "gcp", "resource": "projects/p/secrets/s", "team_id": team, "revision": 0}, open(d + "/cloud.json", "w"))
PY
  if cross_team_cli "${admin_token}" credential source apply --file "${dir}/cloud.json" >"${dir}/cloud.out" 2>&1; then
    echo "cloud provider source was accepted" >&2; return 1
  fi
  grep -q 'not yet supported' "${dir}/cloud.out"

  log_line policy "credential broker: grants, certificate enrollment and the data-plane calls"
  local grant
  for s in "${bound}" "${other}"; do
    grant="${dir}/${s}-grant.yaml"
    "${PROJECT_ROOT}/bin/mcp-runtime" access grant init "${s}-grant" --namespace "${namespace}" --server "${s}" \
      --server-namespace "${namespace}" --human-id "${subject}" --agent-id "${agent}" --team-id "${team_id}" \
      --trust low --side-effect read --tool-rule fingerprint:allow:low --tool-rule leak-response:allow:low \
      --tool-rule other:allow:low --expires-in 1h --output "${grant}" --force
    cross_team_cli "${token}" access grant apply --file "${grant}"
  done
  local enroll creds_bound creds_other
  enroll="$(CROSS_TEAM_CLI_CONFIG_DIR="${dir}/cert-bound" cross_team_cli "${token}" adapter enroll --platform-url "http://127.0.0.1:${PLATFORM_PORT}" --server "${bound}" --namespace "${namespace}" --agent "${agent}")"
  creds_bound="$(parse_adapter_enroll_credential_dir "${enroll}")"
  enroll="$(CROSS_TEAM_CLI_CONFIG_DIR="${dir}/cert-other" cross_team_cli "${token}" adapter enroll --platform-url "http://127.0.0.1:${PLATFORM_PORT}" --server "${other}" --namespace "${namespace}" --agent "${agent}")"
  creds_other="$(parse_adapter_enroll_credential_dir "${enroll}")"

  local host_bound host_other url_bound url_other sess_bound sess_other status
  host_bound="$(kubectl get mcpserver "${bound}" -n "${namespace}" -o jsonpath='{.spec.ingressHost}')"
  host_other="$(kubectl get mcpserver "${other}" -n "${namespace}" -o jsonpath='{.spec.ingressHost}')"
  url_bound="https://127.0.0.1:${TRAEFIK_TLS_PORT}/${bound}/mcp"
  url_other="https://127.0.0.1:${TRAEFIK_TLS_PORT}/${other}/mcp"
  sess_bound="$(broker_initialize "${creds_bound}" "${url_bound}" "${host_bound}" "${dir}/b")"
  sess_other="$(broker_initialize "${creds_other}" "${url_other}" "${host_other}" "${dir}/o")"

  call() { # call NAME CREDS URL HOST SESSION TOOL [curl args]
    local name="$1" creds="$2" url="$3" host="$4" session="$5" tool="$6"
    shift 6
    CB_STATUS="$(broker_call "${creds}" "${url}" "${host}" "${session}" "${dir}/${name}.json" "${dir}/${name}.headers" "$(broker_tool_body 7 "${tool}")" "$@")"
  }
  local forged=(-H "${CREDENTIAL_BROKER_HEADER}: attacker-supplied" -H "X-Mcp-Credential-Extra: attacker-supplied")
  local assert=(python3 "${CREDENTIAL_BROKER_ASSERT}")

  call bound-ok "${creds_bound}" "${url_bound}" "${host_bound}" "${sess_bound}" fingerprint
  [[ "${CB_STATUS}" == 200 ]] || { echo "bound call failed: HTTP ${CB_STATUS}" >&2; return 1; }
  "${assert[@]}" delivered --body "${dir}/bound-ok.json" --headers "${dir}/bound-ok.headers" --secret-file "${secret_file}" --expect bound

  call bound-forged "${creds_bound}" "${url_bound}" "${host_bound}" "${sess_bound}" fingerprint "${forged[@]}"
  "${assert[@]}" delivered --body "${dir}/bound-forged.json" --headers "${dir}/bound-forged.headers" --secret-file "${secret_file}" --expect bound

  call bound-leak "${creds_bound}" "${url_bound}" "${host_bound}" "${sess_bound}" leak-response
  "${assert[@]}" no-reserved-headers --headers "${dir}/bound-leak.headers"
  "${assert[@]}" absent --secret-file "${secret_file}" "${dir}/bound-leak.json" "${dir}/bound-leak.headers"

  call unbound-tool "${creds_bound}" "${url_bound}" "${host_bound}" "${sess_bound}" other "${forged[@]}"
  "${assert[@]}" delivered --body "${dir}/unbound-tool.json" --headers "${dir}/unbound-tool.headers" --secret-file "${secret_file}" --expect none

  call unbound-server "${creds_other}" "${url_other}" "${host_other}" "${sess_other}" fingerprint "${forged[@]}"
  "${assert[@]}" delivered --body "${dir}/unbound-server.json" --headers "${dir}/unbound-server.headers" --secret-file "${secret_file}" --expect none

  log_line policy "credential broker: deleted binding fails closed, then session revocation denies"
  local revision
  revision="$(python3 -c 'import json,re,sys; m=re.search(r"\"revision\"\s*:\s*([0-9]+)",sys.stdin.read()); assert m; print(m.group(1))' <"${dir}/binding-get.out")"
  cross_team_cli "${token}" credential binding delete upstream-token --namespace "${namespace}" --revision "${revision}" --yes >"${dir}/binding-delete.out" 2>&1
  call no-binding "${creds_bound}" "${url_bound}" "${host_bound}" "${sess_bound}" fingerprint
  "${assert[@]}" denied --status "${CB_STATUS}" --body "${dir}/no-binding.json" --headers "${dir}/no-binding.headers" --secret-file "${secret_file}"
  cross_team_cli "${token}" access grant revoke-sessions "${bound}-grant" --namespace "${namespace}"
  local deadline=$((SECONDS+60))
  while true; do
    call revoked "${creds_bound}" "${url_bound}" "${host_bound}" "${sess_bound}" fingerprint
    [[ "${CB_STATUS}" == 401 || "${CB_STATUS}" == 403 ]] && break
    ((SECONDS<deadline)) || { echo "revoked session still served: HTTP ${CB_STATUS}" >&2; return 1; }
    sleep 2
  done
  "${assert[@]}" denied --status "${CB_STATUS}" --body "${dir}/revoked.json" --headers "${dir}/revoked.headers" --secret-file "${secret_file}"

  log_line policy "credential broker: audit rows are metadata only; no value in CLI output or logs"
  curl -fsS -H "Authorization: Bearer ${admin_token}" "http://127.0.0.1:${PLATFORM_PORT}/api/v1/admin/audit?limit=500" >"${dir}/audit.json"
  "${assert[@]}" audit --file "${dir}/audit.json" --namespace "${namespace}" --secret-file "${secret_file}" \
    --require credential_source_write --require credential_binding_write --require credential_binding_delete \
    --require credential_access:success --require credential_access:denied
  kubectl -n "${namespace}" logs "deploy/${bound}" --all-containers --tail=500 >"${dir}/bound-pod.log" 2>&1 || true
  kubectl -n mcp-platform logs deploy/mcp-platform-api --tail=2000 >"${dir}/platform-api.log" 2>&1 || true
  "${assert[@]}" absent --secret-file "${secret_file}" "${dir}"/*.out "${dir}"/*.log "${dir}"/*.headers "${dir}"/*.json
  log_line policy "credential broker scenario passed"
)
