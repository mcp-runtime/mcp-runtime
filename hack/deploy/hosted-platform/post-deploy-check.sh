#!/usr/bin/env bash
set -euo pipefail
umask 077

# Runs only on the disposable release runner with the production kubeconfig.
# The temporary server is published and removed through the customer CLI.
test -n "${KUBECONFIG:-}"
test -n "${RELEASE_TAG:-}"
test -n "${GITHUB_RUN_ID:-}"
test -n "${GITHUB_RUN_ATTEMPT:-}"
: "${RUNNER_TEMP:?Requires a disposable runner directory}"
# Verify actual cluster identity independently of a renamed context.
cluster_uid="$(kubectl get namespace kube-system -o jsonpath='{.metadata.uid}')"
: "${MCP_EXPECTED_CLUSTER_UID:?Set the hosted cluster UID}"
[[ "$cluster_uid" == "$MCP_EXPECTED_CLUSTER_UID" ]] || { echo 'Unexpected hosted cluster' >&2; exit 1; }
[[ "$GITHUB_RUN_ID" =~ ^[0-9]+$ && "$GITHUB_RUN_ATTEMPT" =~ ^[0-9]+$ && "$RELEASE_TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || exit 2
registry_status="$(curl --max-time 10 -sS -o /dev/null -w '%{http_code}' https://registry.mcpruntime.org/v2/)"
[[ "$registry_status" == 401 || "$registry_status" == 403 ]] || { echo 'Anonymous registry access did not fail closed' >&2; exit 1; }

for namespace in mcp-runtime mcp-platform registry cert-manager; do
  kubectl -n "$namespace" get deployments -o json | jq -e \
    '(.items | length) > 0 and all(.items[]; (.status.availableReplicas // 0) >= (.spec.replicas // 1))' \
    >/dev/null
  echo "Deployments ready in $namespace"
done

qa_name="qa-audit-${GITHUB_RUN_ID}-${GITHUB_RUN_ATTEMPT}"
qa_tag="${RELEASE_TAG}-${GITHUB_RUN_ID}-${GITHUB_RUN_ATTEMPT}"
qa_dir="$RUNNER_TEMP/qa-server/.mcp"
export MCP_RUNTIME_CONFIG_DIR="$RUNNER_TEMP/qa-profile"
agent_id=""; proxy_pid=""; session_name=""; grant_created=0; server_created=0
cleanup() {
  local status=$? failed=0
  trap - EXIT
  if [[ -n "$proxy_pid" ]]; then
    kill "$proxy_pid" 2>/dev/null || true
    wait "$proxy_pid" 2>/dev/null || true
    session_name="$(sed -n 's|.*session/\([^ ]*\) (expires.*|\1|p' "$RUNNER_TEMP/qa-adapter.log" | tail -n 1)"
  fi
  if [[ -n "$session_name" ]]; then ./bin/mcp-runtime access session delete "$session_name" --namespace mcp-team-ait || failed=1; fi
  if [[ "$grant_created" == 1 ]]; then ./bin/mcp-runtime access grant delete "$qa_name" --namespace mcp-team-ait || failed=1; fi
  if [[ "$server_created" == 1 ]]; then ./bin/mcp-runtime server delete "$qa_name" --namespace mcp-team-ait || failed=1; fi
  if [[ -n "$agent_id" ]]; then ./bin/mcp-runtime agent deactivate "$agent_id" || failed=1; fi
  ./bin/mcp-runtime auth logout >/dev/null 2>&1 || failed=1
  [[ "$failed" == 0 ]] || { echo 'Acceptance cleanup failed; inspect this run before retrying' >&2; status=1; }
  exit "$status"
}
trap cleanup EXIT

mkdir -m 700 "$MCP_RUNTIME_CONFIG_DIR"
mkdir -p "$qa_dir"
admin_email="$(kubectl -n mcp-platform get secret mcp-platform-api-credentials -o jsonpath='{.data.PLATFORM_ADMIN_EMAIL}' | base64 -d)"
admin_password="$(kubectl -n mcp-platform get secret mcp-platform-api-credentials -o jsonpath='{.data.PLATFORM_ADMIN_PASSWORD}' | base64 -d)"
test -n "$admin_email"
test -n "$admin_password"
./bin/mcp-runtime auth login --api-url https://platform.mcpruntime.org \
  --email "$admin_email" --password "$admin_password" --profile qa-audit >/dev/null
unset admin_email admin_password

./bin/mcp-runtime server init "$qa_name" --metadata-dir "$qa_dir" \
  --scope tenant --image "ait/$qa_name" --tag "$qa_tag" --port 8088 \
  --tool aaa-ping --tool add --policy-mode allow-list --default-decision deny \
  --session-required=true --force
./bin/mcp-runtime server validate --metadata-dir "$qa_dir"
./bin/mcp-runtime server build image "$qa_name" --metadata-dir "$qa_dir" \
  --dockerfile examples/oauth-example-go-2025-11-25/Dockerfile \
  --context examples/oauth-example-go-2025-11-25 --tag "$qa_tag"
qa_image="$(awk '$1=="image:"{i=$2} $1=="imageTag:"{t=$2} END{if(i==""||t=="")exit 1; print i ":" t}' "$qa_dir/servers.yaml")"
./bin/mcp-runtime server push --scope tenant --image "$qa_image"
# Mark before deployment: a failed readiness wait may still leave the server resource.
server_created=1
if ! ./bin/mcp-runtime server deploy "$qa_name" --scope tenant --team ait --metadata-dir "$qa_dir"; then
  kubectl -n mcp-team-ait get mcpserver "$qa_name" -o json \
    | jq -c '{phase:.status.phase,message:.status.message,conditions:.status.conditions}' || true
  exit 1
fi

# An allowed call must carry a managed agent, bounded grant and issued session.
agent_output="$(./bin/mcp-runtime agent create ait --name "$qa_name")"
agent_id="$(printf '%s' "$agent_output" | grep -oE 'agt_[a-z0-9]+' | head -n 1)"
test -n "$agent_id"
team_id="$(./bin/mcp-runtime agent get "$agent_id" | sed -n 's/^Team: .* (\([^)]*\))$/\1/p')"
test -n "$team_id"
./bin/mcp-runtime access grant init "$qa_name" --server "$qa_name" \
  --namespace mcp-team-ait --server-namespace mcp-team-ait --agent-id "$agent_id" --team-id "$team_id" \
  --expires-in 30m --side-effect read --trust low --tool-rule aaa-ping:allow:low \
  --output "$RUNNER_TEMP/qa-grant.yaml"
grant_created=1
./bin/mcp-runtime access grant apply --file "$RUNNER_TEMP/qa-grant.yaml"
# A free local port; no existing process is killed.
port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
./bin/mcp-runtime adapter proxy --platform-url https://platform.mcpruntime.org \
  --runtime-url "https://mcp.mcpruntime.org/$qa_name/mcp" --server "$qa_name" \
  --namespace mcp-team-ait --agent "$agent_id" --listen "127.0.0.1:$port" \
  >"$RUNNER_TEMP/qa-adapter.log" 2>&1 &
proxy_pid=$!
for _attempt in {1..90}; do
  kill -0 "$proxy_pid"
  if grep -q 'proxy listening' "$RUNNER_TEMP/qa-adapter.log"; then break; fi
  sleep 2
done
grep -q 'proxy listening' "$RUNNER_TEMP/qa-adapter.log"
python3 hack/deploy/hosted-platform/check-calls.py "http://127.0.0.1:$port" "https://mcp.mcpruntime.org/$qa_name/mcp"
echo 'Hosted acceptance passed: publish, deploy, managed agent, grant, certificate session, allowed/denied calls and anonymous denial'
