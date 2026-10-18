#!/usr/bin/env bash
# Focused real-Cilium acceptance. Product writes use the CLI; the one temporary
# network probe Pod explicitly tests Kubernetes NetworkPolicy enforcement.

scoped_observability_cli() {
  local token="$1"; shift
  env MCP_PLATFORM_API_URL="http://127.0.0.1:${PLATFORM_PORT}" MCP_PLATFORM_API_TOKEN="${token}" \
    MCP_RUNTIME_CONFIG_DIR="${WORKDIR}/scoped-cli" "${PROJECT_ROOT}/bin/mcp-runtime" "$@"
}

run_scoped_observability_acceptance() (
  set -euo pipefail
  local context owner_token namespace server_name team foreign_team owner member foreign password target probe
  context="$(kubectl --kubeconfig "${KUBECONFIG_FILE}" config current-context)"
  [[ "${context}" == "kind-${CLUSTER_NAME}" && "${CLUSTER_NAME}" == mcp-scope-e2e-* ]] || {
    echo "Scoped observability acceptance requires its own named Kind context" >&2; exit 1;
  }
  [[ "$(kubectl get configmap mcp-shared-config -n mcp-platform -o jsonpath='{.data.GRAFANA_SCOPED_USER_ACCESS}')" == "true" ]] || {
    echo "Supported setup did not verify/activate scoped telemetry isolation" >&2; exit 1;
  }
  team="scope-owner"; foreign_team="scope-foreign"; server_name="scoped-history-server"
  owner="scope-owner@mcpruntime.org"; member="scope-member@mcpruntime.org"; foreign="scope-foreign@mcpruntime.org"
  password="scope-test-password-123"; probe="scope-network-probe"; target="${WORKDIR}/scoped-target.json"
  scoped_observability_cli "${API_KEY}" team create "${team}" --name 'Scoped observability owner' >/dev/null
  scoped_observability_cli "${API_KEY}" team create "${foreign_team}" --name 'Unrelated observability tenant' >/dev/null
  printf '%s\n' "${password}" | scoped_observability_cli "${API_KEY}" team user create "${team}" --email "${owner}" --password-stdin --role owner >/dev/null
  printf '%s\n' "${password}" | scoped_observability_cli "${API_KEY}" team user create "${team}" --email "${member}" --password-stdin --role member >/dev/null
  printf '%s\n' "${password}" | scoped_observability_cli "${API_KEY}" team user create "${foreign_team}" --email "${foreign}" --password-stdin --role owner >/dev/null
  owner_token="$(SCOPED_EMAIL="${owner}" SCOPED_PASSWORD="${password}" SCOPED_API="http://127.0.0.1:${API_SERVICE_PORT}" python3 -c '
import json,os,urllib.request
r=urllib.request.Request(os.environ["SCOPED_API"]+"/api/v1/auth/login",data=json.dumps({"email":os.environ["SCOPED_EMAIL"],"password":os.environ["SCOPED_PASSWORD"]}).encode(),headers={"Content-Type":"application/json"})
with urllib.request.urlopen(r,timeout=10) as response: print(json.load(response)["access_token"])
')"
  ./bin/mcp-runtime server init "${server_name}" --metadata-dir "${WORKDIR}/scoped-server" \
    --image "${server_name}" --tag "${E2E_WORKLOAD_TAG}" --port 8088 --policy-mode observe \
    --default-decision allow --session-required=false --tool aaa-ping --force
  ./bin/mcp-runtime server validate --metadata-dir "${WORKDIR}/scoped-server"
  scoped_observability_cli "${owner_token}" server push --scope tenant --image "${SERVER_IMAGE}" --name "${server_name}"
  scoped_observability_cli "${owner_token}" server deploy "${server_name}" --scope tenant --team "${team}" --metadata-dir "${WORKDIR}/scoped-server"
  curl -fsS -H "Authorization: Bearer ${owner_token}" "http://127.0.0.1:${PLATFORM_PORT}/api/v1/runtime/teams/${team}" > "${target}"
  namespace="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["team"]["namespace"])' < "${target}")"
  [[ "${namespace}" == mcp-* && "${namespace}" != mcp-platform && "${namespace}" != mcp-observability ]] || exit 1
  trap 'kubectl delete pod "${probe}" -n "${namespace}" --ignore-not-found --wait=false >/dev/null 2>&1 || true; scoped_observability_cli "${owner_token}" server delete "${server_name}" --namespace "${namespace}" >/dev/null' EXIT
  wait_for_named_server_ready "${server_name}" "${namespace}"
  kubectl get mcpserver "${server_name}" -n "${namespace}" -o json > "${target}"
  SCOPED_UI_BASE="http://localhost:${TRAEFIK_PORT}" SCOPED_API_KEY="${API_KEY}" \
    SCOPED_OWNER="${owner}" SCOPED_MEMBER="${member}" SCOPED_FOREIGN="${foreign}" SCOPED_PASSWORD="${password}" \
    SCOPED_TARGET="${target}" SCOPED_PROTOCOL="${MCP_PROTOCOL_VERSION}" SCOPED_REPORT="${WORKDIR}/scoped-observability.json" \
    python3 "${PROJECT_ROOT}/test/e2e/scoped_observability.py"

  # Security-specific fixture exception: a read-only uncredentialed Pod probes
  # the tenant network, with no product resources hand-written or bypassed.
  kubectl run "${probe}" -n "${namespace}" --image=nginx:1.27-alpine --restart=Never \
    --overrides='{"spec":{"automountServiceAccountToken":false,"securityContext":{"runAsNonRoot":true,"runAsUser":65534,"seccompProfile":{"type":"RuntimeDefault"}},"containers":[{"name":"scope-network-probe","image":"nginx:1.27-alpine","command":["/bin/sh","-c","sleep 300"],"resources":{"requests":{"cpu":"10m","memory":"16Mi"},"limits":{"memory":"32Mi"}},"securityContext":{"allowPrivilegeEscalation":false,"readOnlyRootFilesystem":true,"capabilities":{"drop":["ALL"]}}}]}}' \
    --command -- /bin/sh -c 'sleep 300' >/dev/null
  kubectl wait pod "${probe}" -n "${namespace}" --for=condition=Ready --timeout=120s >/dev/null
  SCOPED_KUBECONFIG="${KUBECONFIG_FILE}" SCOPED_NAMESPACE="${namespace}" SCOPED_PROBE="${probe}" \
    SCOPED_REPORT="${WORKDIR}/scoped-observability.json" python3 "${PROJECT_ROOT}/test/e2e/scoped_observability.py" --network
)
