#!/usr/bin/env bash
set -euo pipefail

NAMESPACE=${NAMESPACE:-mcp-sentinel}
GATEWAY_PORT=${GATEWAY_PORT:-8083}
API_KEY=${API_KEY:-changeme}
REQ_START=${REQ_START:-20}
REQ_END=${REQ_END:-230}
KIND_CLUSTER_NAME=${KIND_CLUSTER_NAME:-mcp-sentinel}
PROMETHEUS_PORT=${PROMETHEUS_PORT:-9090}
TEMPO_PORT=${TEMPO_PORT:-3200}
LOKI_PORT=${LOKI_PORT:-3100}
LOKI_ROLLOUT_TIMEOUT=${LOKI_ROLLOUT_TIMEOUT:-300s}

if ! command -v kind >/dev/null 2>&1; then
  echo "kind is required but not found in PATH" >&2
  exit 1
fi

if ! command -v kubectl >/dev/null 2>&1; then
  echo "kubectl is required but not found in PATH" >&2
  exit 1
fi

if ! kind get clusters | grep -qx "$KIND_CLUSTER_NAME"; then
  kind create cluster --name "$KIND_CLUSTER_NAME"
fi

kubectl config use-context "kind-${KIND_CLUSTER_NAME}" >/dev/null

docker build -t mcp-platform-api:latest -f services/platform-api/Dockerfile .
docker build -t mcp-runtime-api:latest -f services/runtime-api/Dockerfile .
docker build -t mcp-analytics-api:latest -f services/analytics-api/Dockerfile .
docker build -t mcp-sentinel-ingest:latest -f services/ingest/Dockerfile .
docker build -t mcp-sentinel-processor:latest -f services/processor/Dockerfile .
docker build -t mcp-sentinel-ui:latest -f services/ui/Dockerfile .
docker build -t oauth-example-go-2025-11-25:latest examples/oauth-example-go-2025-11-25
docker build -t mcp-sentinel-mcp-gateway:latest -f services/mcp-gateway/Dockerfile .

kind load docker-image mcp-platform-api:latest --name "$KIND_CLUSTER_NAME"
kind load docker-image mcp-runtime-api:latest --name "$KIND_CLUSTER_NAME"
kind load docker-image mcp-analytics-api:latest --name "$KIND_CLUSTER_NAME"
kind load docker-image mcp-sentinel-ingest:latest --name "$KIND_CLUSTER_NAME"
kind load docker-image mcp-sentinel-processor:latest --name "$KIND_CLUSTER_NAME"
kind load docker-image mcp-sentinel-ui:latest --name "$KIND_CLUSTER_NAME"
kind load docker-image oauth-example-go-2025-11-25:latest --name "$KIND_CLUSTER_NAME"
kind load docker-image mcp-sentinel-mcp-gateway:latest --name "$KIND_CLUSTER_NAME"

kubectl apply -f k8s

kubectl -n "$NAMESPACE" rollout restart deployment/mcp-platform-api
kubectl -n "$NAMESPACE" rollout restart deployment/mcp-runtime-api
kubectl -n "$NAMESPACE" rollout restart deployment/mcp-analytics-api
kubectl -n "$NAMESPACE" rollout restart deployment/mcp-sentinel-ingest
kubectl -n "$NAMESPACE" rollout restart deployment/mcp-sentinel-processor
kubectl -n "$NAMESPACE" rollout restart deployment/mcp-sentinel-ui
kubectl -n "$NAMESPACE" rollout restart deployment/mcp-sentinel-gateway
kubectl -n "$NAMESPACE" rollout restart deployment/workspace-assistant-server
kubectl -n "$NAMESPACE" rollout restart deployment/workspace-assistant-sidecar

kubectl -n "$NAMESPACE" rollout status statefulset/clickhouse --timeout=180s
kubectl -n "$NAMESPACE" rollout status statefulset/kafka --timeout=180s
kubectl -n "$NAMESPACE" wait --for=condition=complete job/kafka-topic-init --timeout=180s
kubectl -n "$NAMESPACE" rollout status deployment/mcp-sentinel-ingest --timeout=180s
kubectl -n "$NAMESPACE" rollout status deployment/mcp-sentinel-processor --timeout=180s
kubectl -n "$NAMESPACE" rollout status deployment/mcp-platform-api --timeout=180s
kubectl -n "$NAMESPACE" rollout status deployment/mcp-runtime-api --timeout=180s
kubectl -n "$NAMESPACE" rollout status deployment/mcp-analytics-api --timeout=180s
kubectl -n "$NAMESPACE" rollout status deployment/mcp-sentinel-ui --timeout=180s
kubectl -n "$NAMESPACE" rollout status deployment/mcp-sentinel-gateway --timeout=180s
kubectl -n "$NAMESPACE" rollout status deployment/workspace-assistant-server --timeout=180s
kubectl -n "$NAMESPACE" rollout status deployment/workspace-assistant-sidecar --timeout=180s
kubectl -n "$NAMESPACE" rollout status deployment/otel-collector --timeout=180s
kubectl -n "$NAMESPACE" rollout status statefulset/tempo --timeout=180s
kubectl -n "$NAMESPACE" rollout status statefulset/loki --timeout="$LOKI_ROLLOUT_TIMEOUT"
kubectl -n "$NAMESPACE" rollout status daemonset/promtail --timeout=180s

PIDS=()
kubectl -n "$NAMESPACE" port-forward svc/mcp-sentinel-gateway "${GATEWAY_PORT}:8083" >/tmp/mcp-pf.log 2>&1 &
PIDS+=("$!")
kubectl -n "$NAMESPACE" port-forward svc/prometheus "${PROMETHEUS_PORT}:9090" >/tmp/mcp-pf-prom.log 2>&1 &
PIDS+=("$!")
kubectl -n "$NAMESPACE" port-forward svc/tempo "${TEMPO_PORT}:3200" >/tmp/mcp-pf-tempo.log 2>&1 &
PIDS+=("$!")
kubectl -n "$NAMESPACE" port-forward svc/loki "${LOKI_PORT}:3100" >/tmp/mcp-pf-loki.log 2>&1 &
PIDS+=("$!")

trap 'for pid in "${PIDS[@]}"; do kill "$pid" 2>/dev/null || true; done' EXIT

wait_port() {
  local port="$1"
  local tries=60
  local i
  for i in $(seq 1 "$tries"); do
    if (echo >/dev/tcp/127.0.0.1/"$port") >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "timed out waiting for localhost:${port}" >&2
  return 1
}

wait_http() {
  local url="$1"
  local header="$2"
  local tries=60
  local i
  for i in $(seq 1 "$tries"); do
    echo "waiting for ${url} (attempt ${i}/${tries})"
    if curl -fsS ${header:+-H "$header"} "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  echo "timed out waiting for $url" >&2
  return 1
}

wait_port "$GATEWAY_PORT"
wait_port "$PROMETHEUS_PORT"
wait_port "$TEMPO_PORT"
wait_port "$LOKI_PORT"

wait_http "http://127.0.0.1:${GATEWAY_PORT}/api/v1/events?limit=1" "x-api-key: ${API_KEY}"
wait_http "http://127.0.0.1:${PROMETHEUS_PORT}/api/v1/status/buildinfo" ""
wait_http "http://127.0.0.1:${TEMPO_PORT}/ready" ""
wait_http "http://127.0.0.1:${LOKI_PORT}/ready" ""
wait_http "http://127.0.0.1:${LOKI_PORT}/loki/api/v1/status/buildinfo" ""

echo "ingest: skipped (using MCP traffic only)"

echo -e "\noauth-example-go-2025-11-25 (MCP JSON-RPC):"
env GATEWAY_PORT="$GATEWAY_PORT" python3 <<'PY'
import json
import os
import sys
import urllib.error
import urllib.request

base = f"http://127.0.0.1:{os.environ.get('GATEWAY_PORT', '8083')}/mcp"

def post(msg, session_id=None, timeout=5):
    headers = {
        "content-type": "application/json",
        "accept": "application/json, text/event-stream",
        "Mcp-Protocol-Version": "2025-06-18",
    }
    if session_id:
        headers["Mcp-Session-Id"] = session_id
    req = urllib.request.Request(base, data=json.dumps(msg).encode(), headers=headers)
    try:
        resp = urllib.request.urlopen(req, timeout=timeout)
        body = resp.read().decode()
        return resp.headers.get("Mcp-Session-Id") or session_id, body
    except urllib.error.HTTPError as exc:
        body = exc.read().decode()
        raise RuntimeError(f"HTTP {exc.code}: {body}") from exc
    except Exception as exc:
        raise RuntimeError(f"request failed: {exc}") from exc

sid, body = post({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {}})
if not sid:
    print("missing Mcp-Session-Id from initialize")
    sys.exit(1)
print("initialize:", body)
post({"jsonrpc": "2.0", "method": "notifications/initialized"}, sid)

sid, body = post({"jsonrpc": "2.0", "id": 2, "method": "tools/list"}, sid)
print("tools/list:", body)

sid, body = post({"jsonrpc": "2.0", "id": 3, "method": "tools/call",
                  "params": {"name": "echo", "arguments": {"message": "hello"}}}, sid)
print("tools/call:", body)

sid, body = post({"jsonrpc": "2.0", "id": 4, "method": "resources/list"}, sid)
print("resources/list:", body)

sid, body = post({"jsonrpc": "2.0", "id": 5, "method": "resources/read",
                  "params": {"uri": "embedded:readme"}}, sid)
print("resources/read:", body)

sid, body = post({"jsonrpc": "2.0", "id": 6, "method": "prompts/list"}, sid)
print("prompts/list:", body)

sid, body = post({"jsonrpc": "2.0", "id": 7, "method": "prompts/get",
                  "params": {"name": "summarize", "arguments": {"text": "MCP analytics collects tool calls."}}}, sid)
print("prompts/get:", body)

for i in range(1, 201):
    post({"jsonrpc": "2.0", "id": 1000 + i, "method": "tools/call",
          "params": {"name": "add", "arguments": {"a": i, "b": i + 1}}}, sid)
print("sent 200 mcp calls")
PY

echo -e "\nproxy-mcp-server (MCP JSON-RPC):"
env GATEWAY_PORT="$GATEWAY_PORT" python3 <<'PY'
import json
import os
import sys
import urllib.error
import urllib.request

base = f"http://127.0.0.1:{os.environ.get('GATEWAY_PORT', '8083')}/mcp-auto"

def post(msg, session_id=None, timeout=5):
    headers = {
        "content-type": "application/json",
        "accept": "application/json, text/event-stream",
        "Mcp-Protocol-Version": "2025-06-18",
    }
    if session_id:
        headers["Mcp-Session-Id"] = session_id
    req = urllib.request.Request(base, data=json.dumps(msg).encode(), headers=headers)
    try:
        resp = urllib.request.urlopen(req, timeout=timeout)
        body = resp.read().decode()
        return resp.headers.get("Mcp-Session-Id") or session_id, body
    except urllib.error.HTTPError as exc:
        body = exc.read().decode()
        raise RuntimeError(f"HTTP {exc.code}: {body}") from exc
    except Exception as exc:
        raise RuntimeError(f"request failed: {exc}") from exc

sid, body = post({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {}})
if not sid:
    print("missing Mcp-Session-Id from initialize (proxy)")
    sys.exit(1)
print("initialize:", body)
post({"jsonrpc": "2.0", "method": "notifications/initialized"}, sid)

sid, body = post({"jsonrpc": "2.0", "id": 2, "method": "tools/list"}, sid)
print("tools/list:", body)

sid, body = post({"jsonrpc": "2.0", "id": 3, "method": "tools/call",
                  "params": {"name": "upper", "arguments": {"message": "proxy"}}}, sid)
print("tools/call:", body)
PY

echo -e "\nsummary (analytics + observability):"
GATEWAY_PORT="$GATEWAY_PORT" API_KEY="$API_KEY" PROMETHEUS_PORT="$PROMETHEUS_PORT" TEMPO_PORT="$TEMPO_PORT" LOKI_PORT="$LOKI_PORT" python3 - <<'PY'
import json
import os
import time
import urllib.parse
import urllib.request
import urllib.error
from collections import Counter

gateway_port = os.environ.get("GATEWAY_PORT", "8083")
api_key = os.environ.get("API_KEY", "changeme")
prom_port = os.environ.get("PROMETHEUS_PORT", "9090")
tempo_port = os.environ.get("TEMPO_PORT", "3200")
loki_port = os.environ.get("LOKI_PORT", "3100")

def get_json(url, headers=None, retries=3, delay=1):
    last = None
    for _ in range(retries):
        try:
            req = urllib.request.Request(url, headers=headers or {})
            return json.loads(urllib.request.urlopen(req).read().decode())
        except Exception as exc:
            last = exc
            time.sleep(delay)
    raise last

rows = []

try:
    events = get_json(
        f"http://127.0.0.1:{gateway_port}/api/v1/events?limit=400",
        headers={"x-api-key": api_key},
    ).get("events", [])
    stats = get_json(
        f"http://127.0.0.1:{gateway_port}/api/v1/stats",
        headers={"x-api-key": api_key},
    )
    total = stats.get("events_total", len(events))
    recent = events[0]["timestamp"] if events else "n/a"
    sources = Counter([e.get("source", "") for e in events])
    types = Counter([e.get("event_type", "") for e in events])
    mcp_events = [
        e for e in events
        if (e.get("source") in ("mcp-gateway",)
            and e.get("event_type") in ("tool.call", "resource.read", "prompt.render"))
    ]
    mcp_sources = Counter([e.get("source", "") for e in mcp_events])
    mcp_types = Counter([e.get("event_type", "") for e in mcp_events])
    top_sources = ", ".join([f"{k}:{v}" for k, v in sources.most_common(3)]) or "n/a"
    top_types = ", ".join([f"{k}:{v}" for k, v in types.most_common(3)]) or "n/a"
    rows.append(("analytics.events_total", str(total)))
    rows.append(("analytics.recent_event", recent))
    rows.append(("analytics.top_sources", top_sources))
    rows.append(("analytics.top_types", top_types))
    rows.append(("analytics.mcp_events_total", str(len(mcp_events))))
    rows.append(("analytics.mcp_top_sources", ", ".join([f"{k}:{v}" for k, v in mcp_sources.most_common(3)]) or "n/a"))
    rows.append(("analytics.mcp_top_types", ", ".join([f"{k}:{v}" for k, v in mcp_types.most_common(3)]) or "n/a"))
except Exception as exc:
    rows.append(("analytics.error", str(exc)))

try:
    jobs = ["mcp-platform-api", "mcp-runtime-api", "mcp-analytics-api", "mcp-sentinel-ingest", "mcp-sentinel-processor"]
    up_values = []
    for job in jobs:
        query = urllib.parse.urlencode({"query": f'up{{job=\"{job}\"}}'})
        url = f"http://127.0.0.1:{prom_port}/api/v1/query?{query}"
        data = get_json(url)
        result = data.get("data", {}).get("result", [])
        value = result[0]["value"][1] if result else "0"
        up_values.append(f"{job}={value}")
    rows.append(("metrics.prometheus_up", ", ".join(up_values)))
except Exception as exc:
    rows.append(("metrics.error", str(exc)))

try:
    params = urllib.parse.urlencode({"limit": "5"})
    url = f"http://127.0.0.1:{tempo_port}/api/search?{params}"
    data = get_json(url)
    traces = data.get("traces", [])
    rows.append(("traces.tempo_found", str(len(traces))))
except Exception as exc:
    rows.append(("traces.error", str(exc)))

try:
    query = '{namespace="mcp-sentinel"}'
    end_ns = int(time.time() * 1e9)
    start_ns = end_ns - int(10 * 60 * 1e9)
    params = urllib.parse.urlencode({
        "query": query,
        "limit": "5",
        "start": str(start_ns),
        "end": str(end_ns),
    })
    url = f"http://127.0.0.1:{loki_port}/loki/api/v1/query_range?{params}"
    data = get_json(url, retries=60, delay=2)
    streams = data.get("data", {}).get("result", [])
    rows.append(("logs.loki_streams", str(len(streams))))
    total_entries = sum(len(s.get("values", [])) for s in streams)
    rows.append(("logs.loki_entries", str(total_entries)))
    if streams:
        labels = streams[0].get("stream", {})
        filename = os.path.basename(labels.get("filename", ""))
        label_bits = []
        if labels.get("job"):
            label_bits.append(f"job={labels.get('job')}")
        if labels.get("namespace"):
            label_bits.append(f"namespace={labels.get('namespace')}")
        if filename:
            label_bits.append(f"file={filename}")
        if label_bits:
            rows.append(("logs.loki_sample_stream", ",".join(label_bits)))
        values = streams[0].get("values", [])
        if values:
            rows.append(("logs.loki_sample_line", values[-1][1]))
except Exception as exc:
    rows.append(("logs.error", str(exc)))

key_width = max(len(k) for k, _ in rows) if rows else 10
val_width = min(80, max(len(v) for _, v in rows)) if rows else 10
print(f"{'check':{key_width}}  {'value':{val_width}}")
print("-" * (key_width + val_width + 2))
for key, value in rows:
    trimmed = value if len(value) <= val_width else value[: val_width - 3] + "..."
    print(f"{key:{key_width}}  {trimmed:{val_width}}")
PY
