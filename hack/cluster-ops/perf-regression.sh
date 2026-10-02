#!/usr/bin/env bash
# Live Kind performance regression vs a local baseline (deterministic).
# Prefer this over replaying .codex/skills/cluster-ops/references/performance.md.
#
# Prerequisites:
#   - contributor-cluster healthy (context test-mcp-runtime)
#   - For S3: Traefik on localhost:18080
#   - For S1/S2: adapter proxy at ADAPTER_URL (default http://127.0.0.1:8099/mcp)
#
# Usage:
#   KUBECONFIG=$HOME/.kube/test-mcp-runtime-config bash hack/cluster-ops/perf-regression.sh
#   SCENARIOS=S3,S4 bash hack/cluster-ops/perf-regression.sh
#   PERF_SAMPLES=50 PERF_REGRESSION_PCT=25 bash hack/cluster-ops/perf-regression.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

TEST_KUBECONFIG="${TEST_KUBECONFIG:-${KUBECONFIG:-$HOME/.kube/test-mcp-runtime-config}}"
export KUBECONFIG="$TEST_KUBECONFIG"
BASE_URL="${BASE_URL:-http://localhost:18080}"
ADAPTER_URL="${ADAPTER_URL:-http://127.0.0.1:8099/mcp}"
SCENARIOS="${SCENARIOS:-S3,S4}"
PERF_SAMPLES="${PERF_SAMPLES:-100}"
PERF_CONCURRENCY="${PERF_CONCURRENCY:-8}"
PERF_REGRESSION_PCT="${PERF_REGRESSION_PCT:-25}"
PERF_OUT_DIR="${PERF_OUT_DIR:-/tmp/mcp-runtime-perf/$(git rev-parse --short HEAD 2>/dev/null || echo local)}"
PERF_BASELINE_DIR="${PERF_BASELINE_DIR:-${HOME}/.cache/mcp-runtime-perf/baseline}"
NS_PLATFORM="${NS_PLATFORM:-mcp-platform}"
MCP_NS="${MCP_NS:-mcp-servers}"
MCP_NAME="${MCP_NAME:-oauth-example-go-2025-11-25-gateway}"

mkdir -p "$PERF_OUT_DIR" "$PERF_BASELINE_DIR"

log() { printf '%s\n' "$*"; }

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || { log "FAIL missing command: $1"; exit 1; }
}
need_cmd kubectl
need_cmd curl
need_cmd python3

ctx="$(kubectl config current-context 2>/dev/null || true)"
if [[ "$ctx" != "test-mcp-runtime" ]]; then
  log "FAIL expected kube context test-mcp-runtime, got: ${ctx:-none}"
  exit 1
fi

suite_enabled() {
  [[ ",${SCENARIOS}," == *",$1,"* ]]
}

adapter_up() {
  curl -fsS -o /dev/null --connect-timeout 2 "${ADAPTER_URL%/mcp}/" 2>/dev/null \
    || curl -fsS -o /dev/null --connect-timeout 2 -X POST \
      -H "content-type: application/json" \
      -H "accept: application/json, text/event-stream" \
      -H "Mcp-Protocol-Version: 2025-06-18" \
      -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' \
      "$ADAPTER_URL" 2>/dev/null
}

log "env cpu=$(sysctl -n hw.ncpu 2>/dev/null || nproc 2>/dev/null || echo unknown)"
log "env out=${PERF_OUT_DIR} baseline=${PERF_BASELINE_DIR} samples=${PERF_SAMPLES} threshold=${PERF_REGRESSION_PCT}%"

# --- S1 / S2 via adapter ---
run_s1_s2() {
  local scenario="$1"
  if ! adapter_up; then
    log "SKIP ${scenario}: adapter not reachable at ${ADAPTER_URL}"
    log "hint: start mcp-runtime adapter proxy (access-governance) on 127.0.0.1:8099"
    return 0
  fi
  python3 - "$PERF_OUT_DIR" "$PERF_SAMPLES" "$PERF_CONCURRENCY" "$ADAPTER_URL" "$scenario" <<'PY'
import json, sys, threading, time, urllib.request
out_dir, n, c, base, scenario = sys.argv[1], int(sys.argv[2]), int(sys.argv[3]), sys.argv[4], sys.argv[5]
PROTO = "2025-06-18"
H = {
    "content-type": "application/json",
    "accept": "application/json, text/event-stream",
    "Mcp-Protocol-Version": PROTO,
}

def post(payload, sess=None):
    h = dict(H)
    if sess:
        h["Mcp-Session-Id"] = sess
    req = urllib.request.Request(base, data=json.dumps(payload).encode(), headers=h)
    with urllib.request.urlopen(req, timeout=15) as r:
        return r.status, r.headers.get("Mcp-Session-Id", sess), r.read()

_, sess, _ = post({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {}})
post({"jsonrpc": "2.0", "method": "notifications/initialized"}, sess)

if scenario == "S1":
    lats = []
    for i in range(n):
        t = time.perf_counter()
        post({"jsonrpc": "2.0", "id": i + 2, "method": "tools/call",
              "params": {"name": "add", "arguments": {"a": 1, "b": 2}}}, sess)
        lats.append((time.perf_counter() - t) * 1000)
    lats.sort()
    def p(q):
        return lats[int(q * (len(lats) - 1))]
    res = {"scenario": "S1", "n": n, "p50": p(0.5), "p95": p(0.95), "p99": p(0.99),
           "min": lats[0], "max": lats[-1]}
else:
    lats = []
    errors = []
    lock = threading.Lock()
    per_thread = max(1, n // c)

    def work(tid):
        worker_errors = 0
        local = []
        for i in range(per_thread):
            t = time.perf_counter()
            try:
                _, _, body = post(
                    {"jsonrpc": "2.0", "id": 100 + i, "method": "tools/call",
                     "params": {"name": "add", "arguments": {"a": tid, "b": i}}},
                    sess,
                )
                if b'"error"' in body:
                    worker_errors += 1
            except Exception:
                worker_errors += 1
            local.append((time.perf_counter() - t) * 1000)
        with lock:
            lats.extend(local)
            errors.append(worker_errors)

    threads = [threading.Thread(target=work, args=(i,)) for i in range(c)]
    t0 = time.perf_counter()
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    elapsed = time.perf_counter() - t0
    lats.sort()
    def p(q):
        return lats[int(q * (len(lats) - 1))] if lats else float("nan")
    res = {
        "scenario": "S2",
        "n": len(lats),
        "concurrency": c,
        "elapsed_s": elapsed,
        "throughput_rps": (len(lats) / elapsed) if elapsed else 0.0,
        "p50": p(0.5),
        "p95": p(0.95),
        "p99": p(0.99),
        "errors": sum(errors),
    }

path = f"{out_dir}/{scenario}.json"
open(path, "w").write(json.dumps(res))
print(json.dumps(res))
PY
}

if suite_enabled S1; then
  log "=== S1 tools/call latency ==="
  run_s1_s2 S1
fi
if suite_enabled S2; then
  log "=== S2 concurrent throughput ==="
  run_s1_s2 S2
fi

# --- S3 analytics ---
if suite_enabled S3; then
  log "=== S3 analytics/usage latency ==="
  if ! curl -fsS -o /dev/null --connect-timeout 2 "${BASE_URL}/" 2>/dev/null; then
    log "SKIP S3: ${BASE_URL} not reachable"
  else
    UI_KEY="$(kubectl get secret mcp-ui-credentials -n "$NS_PLATFORM" \
      -o jsonpath='{.data.UI_API_KEY}' | base64 -d)"
    python3 - "$PERF_OUT_DIR" "$PERF_SAMPLES" "$UI_KEY" "$BASE_URL" <<'PY'
import json, sys, time, urllib.request
out_dir, n, key, base = sys.argv[1], int(sys.argv[2]), sys.argv[3], sys.argv[4]
url = f"{base}/api/v1/analytics/usage?limit=50"
lats = []
for _ in range(n):
    t = time.perf_counter()
    req = urllib.request.Request(url, headers={"x-api-key": key})
    with urllib.request.urlopen(req, timeout=15) as r:
        r.read()
    lats.append((time.perf_counter() - t) * 1000)
lats.sort()
def p(q):
    return lats[int(q * (len(lats) - 1))]
res = {"scenario": "S3", "n": n, "p50": p(0.5), "p95": p(0.95), "p99": p(0.99)}
open(f"{out_dir}/S3.json", "w").write(json.dumps(res))
print(json.dumps(res))
PY
  fi
fi

# --- S4 operator burst ---
if suite_enabled S4; then
  log "=== S4 operator burst-to-ready ==="
  if ! kubectl get mcpserver -n "$MCP_NS" "$MCP_NAME" >/dev/null 2>&1; then
    log "SKIP S4: MCPServer ${MCP_NS}/${MCP_NAME} missing"
  else
    START_MS="$(python3 -c 'import time; print(int(time.time() * 1000))')"
    for i in $(seq 1 10); do
      kubectl annotate mcpserver -n "$MCP_NS" "$MCP_NAME" \
        "qa.mcpruntime.org/ping=${START_MS}-${i}" --overwrite >/dev/null
    done
    kubectl wait --for=condition=Ready=true "mcpserver/${MCP_NAME}" \
      -n "$MCP_NS" --timeout=120s >/dev/null
    END_MS="$(python3 -c 'import time; print(int(time.time() * 1000))')"
    python3 -c 'import json,sys; print(json.dumps({"scenario":"S4","burst":10,"wall_ms":int(sys.argv[1])-int(sys.argv[2])}))' \
      "$END_MS" "$START_MS" | tee "${PERF_OUT_DIR}/S4.json"
  fi
fi

# --- compare ---
log "=== baseline compare ==="
regressions=0
for s in S1 S2 S3 S4; do
  cur="${PERF_OUT_DIR}/${s}.json"
  base="${PERF_BASELINE_DIR}/${s}.json"
  [[ -f "$cur" ]] || continue
  if [[ ! -f "$base" ]]; then
    cp "$cur" "$base"
    log "${s}: recorded new baseline at ${base}"
    continue
  fi
  out="$(python3 - "$cur" "$base" "$PERF_REGRESSION_PCT" <<'PY'
import json, sys
cur = json.load(open(sys.argv[1]))
base = json.load(open(sys.argv[2]))
pct = float(sys.argv[3])
lines = []
flags = 0
for k in ("p95", "p99", "throughput_rps", "wall_ms"):
    if k in cur and k in base and base[k]:
        delta = (cur[k] - base[k]) / base[k] * 100.0
        worse = (delta > pct) if k != "throughput_rps" else (delta < -pct)
        flag = " REGRESSION" if worse else ""
        if worse:
            flags += 1
        lines.append(f"{cur['scenario']} {k}: {base[k]:.2f} -> {cur[k]:.2f} ({delta:+.1f}%){flag}")
print("\n".join(lines))
sys.exit(1 if flags else 0)
PY
)" || {
    log "$out"
    regressions=$((regressions + 1))
    continue
  }
  log "$out"
done

log "=== perf-regression SUMMARY out=${PERF_OUT_DIR} regressions=${regressions} ==="
[[ "$regressions" -eq 0 ]]
