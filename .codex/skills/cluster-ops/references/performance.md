# Performance regression QA

Use the maintained Kind-only script. Select an explicit contributor kubeconfig;
the harness checks both context and actual node provider IDs.

```bash
export KUBECONFIG=/path/to/contributor-kubeconfig
SCENARIOS=S3 bash hack/qa/perf-regression.sh
# Adapter path, with a running session-bound adapter:
SCENARIOS=S1,S2,S3 bash hack/qa/perf-regression.sh
```

S1 measures sequential tool latency, S2 concurrent tool traffic, and S3 analytics
request latency. Required endpoints, successful responses, sample counts and
baseline files must be present. Missing coverage, application errors, failed
requests and invalid numeric results fail. Create a baseline deliberately with
`PERF_RECORD_BASELINE=1`; this is baseline creation, not regression acceptance.

Kind numbers are relative development measurements, not production throughput.
Compare on comparable CPU, memory, node/image configuration and traffic. Record
sample count, concurrency, warmup, p50/p95/p99, error count and the configured
`PERF_REGRESSION_PCT` threshold. Use `PERF_OUT_DIR` and `PERF_BASELINE_DIR` to
retain candidate/baseline evidence without replacing reviewed baselines.

The historical S4 annotation timer was removed. An already-Ready resource could
satisfy its wait before a new reconcile completed, so it did not prove operator
latency. Use operator integration and deployment E2E for functional reconciliation
coverage. A future latency benchmark must prove completion of its submitted
revision before recording a duration.

When a comparison fails, retain both JSON measurements and environment details.
Inspect gateway CPU/memory, analytics query load, response errors and pod
restarts before attributing a slowdown to code. Do not automatically revert or
replace a baseline to make a result green. Missing cluster/baseline access is
blocked coverage unless baseline creation was the requested task.

Gateway per-tool metrics distinguish allowed/denied calls and HTTP status. Use `mcp_gateway_tool_call_duration_seconds` for tool latency; configured names are bounded to 128 and unknown/overflow names use `__other__`. A sampled observation can link to Tempo via an exemplar. HTTP 200 does not prove a successful MCP tool result; inspect the server execution span for `isError` failures.

## Policy lookup microbenchmarks

Run without any cluster credentials, from the repository root:

```bash
export KUBECONFIG="$(mktemp)"
GOMAXPROCS=4 go test ./pkg/policy -run '^$' \
  -bench 'Benchmark(Authorize|SessionLookup)Scaling' -benchmem -count=5
GOMAXPROCS=4 go test ./internal/operator -run '^$' \
  -bench '^BenchmarkGrantTargetLookup$' -benchmem -count=5
go test -race ./pkg/policy
(cd services/mcp-gateway && go test -race ./...)
```

`Compile` copies policy data and constructs immutable name/subject indexes at
reload. Request code must never mutate the compiled document. Matching human,
agent and team fields remain conjunctive, with omitted fields as wildcards;
empty subjects never match. At most seven subject buckets can apply. Required
and optional session gates, first-match semantics, expiry, revocation, tool
metadata, monitor isolation and grant attribution remain unchanged. Broad grants
matching the same principal still cost proportional to those matching grants
and their rules. Compilation and retained indexes cost memory proportional to
policy size; request latency is not a measurement of reload cost.

Measured 2026-10-17 on Linux/amd64, Go 1.26.9, QEMU Virtual CPU 2.5+, GOMAXPROCS=4.
Medians of three 100 ms samples, no race detector. Before uses the original
evaluator at `554181fa` via Go's overlay support with identical benchmark
fixtures; after uses compiled snapshots. One request selects the last tool,
grant and embedded session among distinct subjects; missing selects an absent
session. These are synthetic lookup measurements, not network latency,
throughput, saturation or production capacity. Larger fixtures can exceed the
operator's existing 700 KiB publication guard.

| Entries | Allow before (ns/op) | Allow indexed (ns/op) | Missing before (ns/op) | Missing indexed (ns/op) |
|---|---:|---:|---:|---:|
|100|30,471|1,341|22,552|269|
|1,000|467,283|1,571|482,435|257|
|10,000|5,748,269|1,227|5,410,918|252|

The old evaluator partitions all grants even without monitor grants, explaining
its allocation growth. Indexed allow uses 448 B/four allocations per request;
indexed missing session uses zero allocations. Standalone indexed embedded
session lookup is 42–51 ns for a hit and 20–22 ns for a miss across these sizes,
with zero allocations. Production platform session storage was already separate
and indexed; these legacy-binding numbers are not a session-sync improvement.

Existing operator `BenchmarkGrantTargetLookup` measures informer selection and
deep copies only, retaining three relevant grants among unrelated ones:

| Unrelated grants | List/filter (ns/op) | Existing index (ns/op) |
|---|---:|---:|
|100|45,599|2,163|
|1,000|428,171|1,866|
|10,000|4,447,691|1,946|

The effective-target index already ships in the operator (#657); runtime sessions
are platform-owned, so the operator no longer scans Kubernetes sessions. These
results do not measure full reconciliation or API I/O. Full reconcile latency
needs the submitted-revision acceptance described above.

Preserve raw Go benchmark output and compare matching environments and names
with the Go project's [benchstat](https://pkg.go.dev/golang.org/x/perf/cmd/benchstat).
The `linear` subbenchmarks remain an in-process control. To reproduce the old
evaluator comparison, overlay only `pkg/policy/evaluator.go` from `554181fa` while
keeping the current benchmark/compilation fixtures, then run the same
`BenchmarkAuthorizeScaling/indexed/` cases. The old evaluator ignores the private
indexes. Never infer performance acceptance from a single noisy sample.

## Upstream capacity and timeouts

Upstream capacity rejects before dispatch with HTTP 429 / `upstream_backpressure`;
an open circuit returns HTTP 503 / `upstream_unavailable`. Policy decision counters
retain the authorization result for these operational failures. GET session streams
and upgrades have a separate capacity budget; POST SSE responses retain an active
RPC slot. Actual SSE responses have no unary body deadline. A stalled unary body
can time out after HTTP 200 headers, so check the audit `upstream_timeout` reason
and partial-response failure rather than looking only for HTTP 504. See the
gateway upstream bounds in `docs/platform-services.md`.
