#!/usr/bin/env bash
set -euo pipefail

# Runs only on the disposable release runner with the production kubeconfig.
# The temporary server is published and removed through the customer CLI.
test -n "${KUBECONFIG:-}"
test -n "${RELEASE_TAG:-}"
test -n "${GITHUB_RUN_ID:-}"
test -n "${GITHUB_RUN_ATTEMPT:-}"

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
cleanup() {
  ./bin/mcp-runtime server delete "$qa_name" --namespace mcp-team-ait >/dev/null 2>&1 || true
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
  --tool aaa-ping --policy-mode observe --default-decision allow \
  --session-required=false --force
./bin/mcp-runtime server validate --metadata-dir "$qa_dir"
./bin/mcp-runtime server build image "$qa_name" --metadata-dir "$qa_dir" \
  --dockerfile examples/oauth-example-go-2025-11-25/Dockerfile \
  --context examples/oauth-example-go-2025-11-25 --tag "$qa_tag"
qa_image="$(awk '$1=="image:"{i=$2} $1=="imageTag:"{t=$2} END{if(i==""||t=="")exit 1; print i ":" t}' "$qa_dir/servers.yaml")"
./bin/mcp-runtime server push --scope tenant --image "$qa_image"
if ! ./bin/mcp-runtime server deploy "$qa_name" --scope tenant --team ait --metadata-dir "$qa_dir"; then
  kubectl -n mcp-team-ait get mcpserver "$qa_name" -o json \
    | jq -c '{phase:.status.phase,message:.status.message,conditions:.status.conditions}' || true
  exit 1
fi

QA_AUDIT_URL="https://mcp.mcpruntime.org/${qa_name}/mcp" python3 - <<'PY'
import json
import os
import time
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

url = os.environ['QA_AUDIT_URL']

def call(method, params=None, session=''):
    headers = {
        'Accept': 'application/json, text/event-stream',
        'Content-Type': 'application/json',
    }
    if session:
        headers['Mcp-Session-Id'] = session
    body = json.dumps({
        'jsonrpc': '2.0', 'id': 1, 'method': method, 'params': params or {},
    }).encode()
    with urlopen(Request(url, data=body, headers=headers, method='POST'), timeout=10) as response:
        raw = response.read().decode()
        if 'text/event-stream' in response.headers.get('Content-Type', ''):
            events = [line.removeprefix('data: ').strip() for line in raw.splitlines()
                      if line.startswith('data: ')]
            if not events:
                raise ValueError('MCP response contained no SSE data')
            payload = json.loads(events[-1])
        else:
            payload = json.loads(raw)
        if payload.get('error'):
            raise ValueError(f'MCP {method} returned an error: {payload["error"]}')
        return payload.get('result') or {}, response.headers.get('Mcp-Session-Id', session)

for attempt in range(24):
    try:
        result, session = call('initialize', {
            'protocolVersion': '2025-06-18',
            'capabilities': {},
            'clientInfo': {'name': 'qa-audit-release', 'version': '1'},
        })
        if not result.get('protocolVersion'):
            raise ValueError('MCP initialize has no protocol version')
        result, _ = call('tools/list', session=session)
        names = {tool.get('name') for tool in result.get('tools', [])}
        if 'aaa-ping' not in names:
            raise ValueError('qa-audit tool is absent from tools/list')
        print('Temporary qa-audit MCP initialize and tools/list passed')
        break
    except (HTTPError, URLError, TimeoutError, ValueError) as error:
        if attempt == 23:
            raise SystemExit(f'qa-audit MCP protocol check failed: {error}') from error
        time.sleep(5)
PY
