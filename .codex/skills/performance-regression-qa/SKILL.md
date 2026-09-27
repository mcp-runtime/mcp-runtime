---
name: performance-regression-qa
description: Real-cluster performance regression QA — MCP tools/call latency (p50/p95/p99), concurrent throughput against the gateway, /api/v1/analytics/usage latency under load, and operator burst-reconcile latency. Compares against a stored per-branch baseline and flags regressions above a configurable threshold (default +25 percent on p95). Use when Codex is asked to perf-check a change to gateway/proxy/operator/api hot paths, before a release, or when investigating a "feels slower" report. Assumes contributor-cluster-setup has run.
---

# Performance Regression QA

## Overview

Performance regressions hide between unit tests (no concurrency, no real
gateway) and full load tests (rare, expensive). This skill runs a short,
deterministic perf matrix against the **live** contributor cluster and
compares results to a stored baseline. It is not a load test — it does not
saturate the cluster; it measures whether a change moved the curve.

Numbers measured on a Kind cluster are not production numbers. The point is
**relative**: did this branch regress vs `main` or vs the last recorded
baseline.

Regression evidence contract: do not report a perf pass from a single request
or current-branch-only sample. A pass requires baseline comparison, warmup,
scenario sample counts, p50/p95/p99, and the configured regression threshold;
missing baseline or live cluster access is **blocked** unless the user accepts
baseline creation as the task.

## Step 1 — Confirm precondition

```bash
TEST_KUBECONFIG="${TEST_KUBECONFIG:-$HOME/.kube/test-mcp-runtime-config}"
kubectl --kubeconfig "$TEST_KUBECONFIG" config current-context \
  | grep -qx test-mcp-runtime \
  || { echo "Run contributor-cluster-setup first"; exit 1; }
export KUBECONFIG="$TEST_KUBECONFIG"
./bin/mcp-runtime cluster doctor
# Quiet the cluster: drain any leftover concurrent traffic from prior skills.
sleep 5
```

Record the environment (CPU, mem, kernel, kind node count, image SHAs) into
the report. Perf numbers without an environment line are not comparable.

```bash
sysctl -n hw.ncpu 2>/dev/null || nproc
kubectl get nodes -o wide
kubectl -n mcp-sentinel get deploy -o jsonpath='{range .items[*]}{.metadata.name}={.spec.template.spec.containers[0].image}{"\n"}{end}'
```

## Step 2 — Choose mode

- **head-only**. Run all four scenarios.
- **git-range** (`BASE=<merge-base>`, default `origin/main`). Trim by diff:

| Diff touches | Scenarios to run |
|---|---|
| `services/mcp-gateway/**`, `pkg/access/**` | S1, S2 (gateway hot path) |
| `internal/operator/**` | S4 (reconcile burst) |
| `services/platform-api/**`, `services/runtime-api/**`, `services/analytics-api/**`, `services/processor/**`, `services/ingest/**` | S3 (analytics) |
| `services/ui/**` static only | Skip — UI is not the hot path |
| `config/ingress/**` | S1, S2 (Traefik path-routing) |

## Step 3 — Tunables and baseline storage

```bash
PERF_OUT_DIR="${PERF_OUT_DIR:-/tmp/mcp-runtime-perf/$(git rev-parse --short HEAD)}"
PERF_BASELINE_DIR="${PERF_BASELINE_DIR:-/tmp/mcp-runtime-perf/baseline}"
PERF_REGRESSION_PCT="${PERF_REGRESSION_PCT:-25}"   # flag if p95 worsens by > N%
PERF_SAMPLES="${PERF_SAMPLES:-200}"
PERF_CONCURRENCY="${PERF_CONCURRENCY:-16}"
mkdir -p "$PERF_OUT_DIR"
```

`PERF_BASELINE_DIR` is a per-developer baseline; if missing, the first run
**records** rather than compares. CI baselines should be checked in elsewhere
or stored under `~/.cache/mcp-runtime-perf/` per branch.

## Step 4 — Scenario S1: tools/call latency (p50/p95/p99)

Sequential single-session calls. Measures gateway + proxy + app overhead
without contention.

```bash
python3 - <<'PY' "$PERF_OUT_DIR" "$PERF_SAMPLES"
import json, time, urllib.request, sys
out_dir, n = sys.argv[1], int(sys.argv[2])
BASE="http://localhost:18080/oauth-example-go-2025-11-25-gateway/mcp"; PROTO="2025-06-18"
H={"content-type":"application/json","accept":"application/json, text/event-stream",
   "Mcp-Protocol-Version":PROTO,
   "X-MCP-Human-ID":"local-user","X-MCP-Agent-ID":"local-agent",
   "X-MCP-Agent-Session":"local-session"}
def post(p, sess=None):
    h=dict(H);
    if sess: h["Mcp-Session-Id"]=sess
    req=urllib.request.Request(BASE, data=json.dumps(p).encode(), headers=h)
    with urllib.request.urlopen(req, timeout=10) as r: return r.status, r.headers.get("Mcp-Session-Id", sess)
_, sess = post({"jsonrpc":"2.0","id":1,"method":"initialize","params":{}})
post({"jsonrpc":"2.0","method":"notifications/initialized"}, sess)
lats=[]
for i in range(n):
    t=time.perf_counter()
    post({"jsonrpc":"2.0","id":i+2,"method":"tools/call","params":{"name":"add","arguments":{"a":1,"b":2}}}, sess)
    lats.append((time.perf_counter()-t)*1000)
lats.sort()
def p(q): return lats[int(q*(len(lats)-1))]
res={"scenario":"S1","n":n,"p50":p(.5),"p95":p(.95),"p99":p(.99),"min":lats[0],"max":lats[-1]}
print(json.dumps(res))
open(f"{out_dir}/S1.json","w").write(json.dumps(res))
PY
```

## Step 5 — Scenario S2: concurrent throughput (gateway under load)

N goroutine-style workers in Python threads. Records throughput and tail
latency under contention.

```bash
python3 - <<'PY' "$PERF_OUT_DIR" "$PERF_SAMPLES" "$PERF_CONCURRENCY"
import json, time, threading, urllib.request, sys
out_dir, n, c = sys.argv[1], int(sys.argv[2]), int(sys.argv[3])
BASE="http://localhost:18080/oauth-example-go-2025-11-25-gateway/mcp"; PROTO="2025-06-18"
H={"content-type":"application/json","accept":"application/json, text/event-stream",
   "Mcp-Protocol-Version":PROTO,
   "X-MCP-Human-ID":"local-user","X-MCP-Agent-ID":"local-agent",
   "X-MCP-Agent-Session":"local-session"}
def post(p, sess=None):
    h=dict(H);
    if sess: h["Mcp-Session-Id"]=sess
    req=urllib.request.Request(BASE, data=json.dumps(p).encode(), headers=h)
    with urllib.request.urlopen(req, timeout=15) as r: return r.status, r.headers.get("Mcp-Session-Id", sess), r.read()
_, sess, _ = post({"jsonrpc":"2.0","id":1,"method":"initialize","params":{}})
post({"jsonrpc":"2.0","method":"notifications/initialized"}, sess)
lats=[]; errors=[]; lock=threading.Lock(); per_thread=n//c
def work(tid):
    worker_errors=0
    local=[]
    for i in range(per_thread):
        t=time.perf_counter()
        try:
            _, _, body = post({"jsonrpc":"2.0","id":100+i,"method":"tools/call","params":{"name":"add","arguments":{"a":tid,"b":i}}}, sess)
            if b'"error"' in body: worker_errors+=1
        except Exception: worker_errors+=1
        local.append((time.perf_counter()-t)*1000)
    with lock:
        lats.extend(local)
        errors.append(worker_errors)
threads=[threading.Thread(target=work,args=(i,)) for i in range(c)]
t0=time.perf_counter()
[t.start() for t in threads]; [t.join() for t in threads]
elapsed=time.perf_counter()-t0
lats.sort()
def p(q): return lats[int(q*(len(lats)-1))] if lats else float('nan')
res={"scenario":"S2","n":len(lats),"concurrency":c,"elapsed_s":elapsed,
     "throughput_rps":len(lats)/elapsed,"p50":p(.5),"p95":p(.95),"p99":p(.99),"errors":sum(errors)}
print(json.dumps(res))
open(f"{out_dir}/S2.json","w").write(json.dumps(res))
PY
```

Any non-zero `errors` against the bundled `add` tool is itself a finding
(typically a session-state regression under contention).

## Step 6 — Scenario S3: /api/v1/analytics/usage latency under load

This scenario depends on ClickHouse being healthy. If it has been down for
a while (reused long-lived cluster), latency numbers here are meaningless —
check `cluster-troubleshooting/reference.md` for the ClickHouse/Kafka
PVC-corruption signature and recover it first rather than recording a
baseline against a 500/502-erroring endpoint.

```bash
UI_KEY="$(kubectl get secret mcp-sentinel-secrets -n mcp-sentinel \
  -o jsonpath='{.data.UI_API_KEY}' | base64 -d)"
python3 - <<'PY' "$PERF_OUT_DIR" "$PERF_SAMPLES" "$UI_KEY"
import json, time, urllib.request, sys
out_dir, n, key = sys.argv[1], int(sys.argv[2]), sys.argv[3]
URL="http://localhost:18080/api/v1/analytics/usage?limit=50"
lats=[]
for i in range(n):
    t=time.perf_counter()
    req=urllib.request.Request(URL, headers={"x-api-key":key})
    with urllib.request.urlopen(req, timeout=10) as r: r.read()
    lats.append((time.perf_counter()-t)*1000)
lats.sort()
def p(q): return lats[int(q*(len(lats)-1))]
res={"scenario":"S3","n":n,"p50":p(.5),"p95":p(.95),"p99":p(.99)}
print(json.dumps(res))
open(f"{out_dir}/S3.json","w").write(json.dumps(res))
PY
```

## Step 7 — Scenario S4: operator burst-to-ready time

This sends ten rapid metadata updates to one MCPServer and measures until it
is Ready again. The controller may coalesce updates, so report this as
submission-to-ready wall time rather than ten separately completed reconciles.

```bash
START="$(python3 -c 'import time; print(int(time.time() * 1000))')"
for i in $(seq 1 10); do
  kubectl annotate mcpserver -n mcp-servers oauth-example-go-2025-11-25-gateway \
    qa.mcpruntime.org/ping="$START-$i" --overwrite >/dev/null
done
kubectl wait --for=condition=Ready=true mcpserver/oauth-example-go-2025-11-25-gateway \
  -n mcp-servers --timeout=120s >/dev/null
END="$(python3 -c 'import time; print(int(time.time() * 1000))')"
python3 -c "import json,sys; print(json.dumps({'scenario':'S4','burst':10,'wall_ms':int(sys.argv[1])-int(sys.argv[2])}))" \
  "$END" "$START" | tee "$PERF_OUT_DIR/S4.json"

# Operator log slice during the burst — any 'reconcile failed' is a finding.
kubectl logs -n mcp-runtime deploy/mcp-runtime-operator-controller-manager \
  --since=1m | grep -Ei 'error|reconcile failed' || true
```

## Step 8 — Compare to baseline

```bash
mkdir -p "$PERF_BASELINE_DIR"
for s in S1 S2 S3 S4; do
  cur="$PERF_OUT_DIR/$s.json"
  base="$PERF_BASELINE_DIR/$s.json"
  [ -f "$cur" ] || continue
  if [ ! -f "$base" ]; then
    cp "$cur" "$base"
    echo "$s: recorded new baseline"
    continue
  fi
  python3 - <<PY "$cur" "$base" "$PERF_REGRESSION_PCT"
import json, sys
cur=json.load(open(sys.argv[1])); base=json.load(open(sys.argv[2])); pct=float(sys.argv[3])
for k in ("p95","p99","throughput_rps","wall_ms"):
    if k in cur and k in base and base[k]:
        delta = (cur[k]-base[k])/base[k]*100.0
        worse = (delta > pct) if k != "throughput_rps" else (delta < -pct)
        flag  = " REGRESSION" if worse else ""
        print(f"{cur['scenario']} {k}: {base[k]:.2f} -> {cur[k]:.2f} ({delta:+.1f}%){flag}")
PY
done
```

## Step 9 — When a regression fires

Do not auto-revert. Capture context so the author can act:

```bash
# CPU profile of the proxy sidecar (if pprof is enabled on the build).
POD="$(kubectl get pods -n mcp-servers -l app=oauth-example-go-2025-11-25-gateway -o jsonpath='{.items[0].metadata.name}')"
kubectl exec -n mcp-servers "$POD" -c mcp-gateway -- \
  wget -qO- http://127.0.0.1:6060/debug/pprof/profile?seconds=10 > /tmp/proxy-cpu.pprof 2>/dev/null \
  || echo "pprof not enabled on mcp-gateway"

kubectl top pods -n mcp-sentinel
kubectl top pods -n mcp-servers
kubectl logs -n mcp-sentinel deploy/mcp-sentinel-processor --since=2m | tail -40
```

Record the suspected hot path with file:line references in
`services/mcp-gateway/**`, `pkg/access/**`, `services/platform-api/**`, `services/runtime-api/**`, `services/analytics-api/**`, or
`internal/operator/**` based on which scenario regressed.

## Step 10 — Report

- One table per scenario: baseline value, current value, delta, regression flag.
- Environment line (CPU, mem, Kind nodes, image SHAs).
- Caveat: numbers are on Kind, not production — flagged regressions still
  matter, absolute numbers do not.
- Include `PERF_OUT_DIR` so the next run can compare to **this** branch.
- Cross-link to `cluster-operations-qa` if reconcile regressed, `security-regression-qa`
  if errors-under-load look like policy failures, and `simplify` /
  `code-review` style follow-up for hot-path code.
