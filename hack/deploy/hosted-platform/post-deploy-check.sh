#!/usr/bin/env bash
set -euo pipefail

# Runs only on the disposable release runner with the production kubeconfig.
# The temporary MCPServer is removed even when a probe fails.
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

kubectl -n mcp-servers get mcpservers -o json | jq -e \
  'all(.items[]; .status.phase == "Ready")' >/dev/null
echo 'Existing MCP servers are ready'

qa_name="qa-audit-${GITHUB_RUN_ID}-${GITHUB_RUN_ATTEMPT}"
qa_image="registry.mcpruntime.org/qa-audit:${RELEASE_TAG}-${GITHUB_RUN_ID}-${GITHUB_RUN_ATTEMPT}"
cleanup() {
  kubectl -n mcp-servers delete mcpserver "$qa_name" --ignore-not-found --wait=false >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker build --platform linux/amd64 -t "$qa_image" examples/oauth-example-go-2025-11-25
docker push "$qa_image"

kubectl apply -f - <<YAML
apiVersion: mcpruntime.org/v1alpha1
kind: MCPServer
metadata:
  name: $qa_name
  namespace: mcp-servers
spec:
  description: Temporary post-deployment MCP protocol audit.
  image: $qa_image
  port: 8088
  publicPathPrefix: $qa_name
  gateway:
    enabled: true
  policy:
    mode: observe
    defaultDecision: allow
  session:
    required: false
  tools:
    - name: aaa-ping
      description: Check that the temporary audit server is reachable.
      requiredTrust: low
      sideEffect: read
YAML

ready=false
for attempt in $(seq 1 60); do
  phase="$(kubectl -n mcp-servers get mcpserver "$qa_name" -o jsonpath='{.status.phase}' 2>/dev/null || true)"
  if [[ "$phase" == Ready ]]; then
    ready=true
    break
  fi
  sleep 5
done
if [[ "$ready" != true ]]; then
  kubectl -n mcp-servers describe mcpserver "$qa_name"
  echo 'Temporary qa-audit MCP server did not become ready' >&2
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
