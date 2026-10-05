# Fault injection for #533, sourced only by the disposable Kind QA harness.
# Apply desired MCPServer changes through the supported CLI; kubectl is used
# for observation and the deliberate operator restart.
run_e2e_port_transition_scenario() {
  [[ "$(kubectl config current-context)" == "kind-${CLUSTER_NAME}" ]] || {
    echo '[port-transition] requires the QA Kind context' >&2; return 1;
  }
  local directory="${WORKDIR}/port-transition" old_port candidate_port deadline gateway_image
  mkdir -p "${directory}"
  kubectl get mcpserver "${SERVER_NAME}" -n mcp-servers -o json >"${directory}/original.json"
  kubectl get service "${SERVER_NAME}" -n mcp-servers -o json >"${directory}/service-before.json"
  kubectl get deployment "${SERVER_NAME}" -n mcp-servers -o json >"${directory}/deployment-before.json"
  old_port="$(jq -r '.spec.ports[0].targetPort' "${directory}/service-before.json")"
  candidate_port=$((old_port + 10))
  gateway_image="$(jq -er '.spec.template.spec.containers[] | select(.name == "mcp-gateway") | .image' "${directory}/deployment-before.json")"
  jq --arg image 'registry.registry.svc.cluster.local:5000/mcp-gateway:missing-port-transition-fixture' \
    --argjson port "${candidate_port}" \
    'del(.status, .metadata.managedFields, .metadata.resourceVersion, .metadata.uid, .metadata.creationTimestamp) | .spec.gateway.port=$port | .spec.gateway.image=$image' \
    "${directory}/original.json" >"${directory}/broken.json"
  ./bin/mcp-runtime server --use-kube apply --file "${directory}/broken.json"
  deadline=$((SECONDS + 120))
  while true; do
    kubectl get mcpserver "${SERVER_NAME}" -n mcp-servers -o json >"${directory}/status.json"
    if jq -e '.metadata.generation as $g | any(.status.conditions[]?; .type=="DeploymentReady" and .status=="False" and .observedGeneration==$g)' "${directory}/status.json" >/dev/null; then break; fi
    ((SECONDS < deadline)) || { echo '[port-transition] broken candidate was not reported pending' >&2; return 1; }
    sleep 2
  done
  port_transition_assert_old_route "${directory}"
  # A restarted operator must recover the persisted Service route, rather than
  # treating the declared candidate port as the previously serving port.
  kubectl rollout restart deployment/mcp-runtime-operator-controller-manager -n mcp-runtime >/dev/null
  rollout_status_with_logs mcp-runtime deploy mcp-runtime-operator-controller-manager 180s
  for _ in 1 2 3 4 5; do
    port_transition_assert_old_route "${directory}"
    wait_for_mcp_tool_result "${MCP_SESSION_URL}" aaa-ping '{}' 200 pong 1 '' port-transition-retained
    sleep 2
  done
  jq --argjson port "${candidate_port}" --arg image "${gateway_image}" \
    'del(.status, .metadata.managedFields, .metadata.resourceVersion, .metadata.uid, .metadata.creationTimestamp) | .spec.gateway.port=$port | .spec.gateway.image=$image' \
    "${directory}/original.json" >"${directory}/fixed.json"
  ./bin/mcp-runtime server --use-kube apply --file "${directory}/fixed.json"
  deadline=$((SECONDS + 180))
  while [[ "$(kubectl get service "${SERVER_NAME}" -n mcp-servers -o jsonpath='{.spec.ports[0].targetPort}')" != "${candidate_port}" ]]; do
    ((SECONDS < deadline)) || { echo '[port-transition] ready candidate was not promoted' >&2; return 1; }
    sleep 2
  done
  wait_for_mcp_tool_result "${MCP_SESSION_URL}" aaa-ping '{}' 200 pong "${MCP_POLICY_WAIT_TRIES}" '' port-transition-promoted
  jq 'del(.status, .metadata.managedFields, .metadata.resourceVersion, .metadata.uid, .metadata.creationTimestamp)' \
    "${directory}/original.json" >"${directory}/restore.json"
  ./bin/mcp-runtime server --use-kube apply --file "${directory}/restore.json"
  deadline=$((SECONDS + 180))
  while [[ "$(kubectl get service "${SERVER_NAME}" -n mcp-servers -o jsonpath='{.spec.ports[0].targetPort}')" != "${old_port}" ]]; do
    ((SECONDS < deadline)) || { echo '[port-transition] original route was not restored' >&2; return 1; }
    sleep 2
  done
}

port_transition_assert_old_route() {
  local directory="$1"
  kubectl get service "${SERVER_NAME}" -n mcp-servers -o json >"${directory}/service-current.json"
  jq -e --slurpfile before "${directory}/service-before.json" \
    '.spec.ports == $before[0].spec.ports and .spec.selector == $before[0].spec.selector' \
    "${directory}/service-current.json" >/dev/null || {
    echo '[port-transition] failed candidate changed the serving Service' >&2; return 1;
  }
}
