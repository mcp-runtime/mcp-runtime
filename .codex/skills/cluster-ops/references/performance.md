# Performance regression QA

For sizing reports, use `E2E_CAPTURE_RESOURCES=1` with an explicit named Kind
target and a fresh supported test-mode setup. Resource history uses an exact
projected Prometheus identity and verified kubelet Summary API trust. Inspect
`resource_collection_success`, same-window kubelet comparisons, collection wall
time and per-PVC missing coverage in `grafana.json`. Missing driver statistics
and local `du` fallback remain separate; PVC capacity is never consumption.
Refresh setup for new nodes or trust changes. See
`docs/platform-services.md#resource-history-for-sizing`.

Use the maintained Kind-only script. Select an explicit contributor kubeconfig;
the harness checks both context and actual node provider IDs.

```bash
export KUBECONFIG=/path/to/contributor-kubeconfig
PERF_API_KEY="$KIND_ADMIN_API_KEY" SCENARIOS=S3 bash hack/qa/perf-regression.sh
# Adapter path, with a running session-bound adapter:
PERF_API_KEY="$KIND_ADMIN_API_KEY" SCENARIOS=S1,S2,S3 bash hack/qa/perf-regression.sh
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

## Defined benchmark suite

Existing CI's **Benchmarks** job now records three short samples from root
policy/bearer/certificate/operator benchmarks and gateway session/audit emission
benchmarks. Raw text is retained as `microbenchmark-samples`; CI smoke samples
do not establish statistically significant performance changes. For a repeated
comparison on the same host, Go version, GOMAXPROCS and fixture revision:

```bash
export KUBECONFIG="$(mktemp)" GOMAXPROCS=4 GOFLAGS=-p=4
hack/ci/go-test.sh . ./pkg/policy ./pkg/platformauth ./pkg/certauth \
  -run '^$' -bench 'Benchmark(Authorize|SessionLookup|VerifyPlatformBearer|SessionCertificateChain|SessionMTLSTransport)' \
  -benchmem -count=10 > before-root.txt
hack/ci/go-test.sh services/mcp-gateway ./... -run '^$' \
  -bench 'Benchmark(SessionCertificateAuthorization|EmitAnalyticsEvent)' \
  -benchmem -count=10 > before-gateway.txt
# Repeat the identical commands after the change, writing after-*.txt.
go run golang.org/x/perf/cmd/benchstat@v0.0.0-20261009192801-be2c69fb417e \
  before-root.txt after-root.txt
```

Benchstat is the Go project's tool; the pinned module/version and ten-sample
guidance were checked against its
[official documentation](https://pkg.go.dev/golang.org/x/perf/cmd/benchstat)
and [module metadata](https://proxy.golang.org/golang.org/x/perf/@v/v0.0.0-20261009192801-be2c69fb417e.info)
on 2026-10-17. `go run` leaves the installed host tool version unchanged. Compare
matching names within each package, with raw results and confidence intervals;
do not compare a full-handshake benchmark directly to a different request path
and present that ratio as a gateway improvement.

`BenchmarkAuthorizeMatchingRules` holds the principal constant while increasing
matching grants (1/10/100) and exact tool rules (1/32/128), testing both allow and
last-grant explicit deny. Existing scaling benchmarks cover indexed versus
linear distinct-subject and embedded-session lookup. `BenchmarkVerifyPlatformBearer`
verifies real signed HS256 tokens, with 0/10/100 team claims and valid/foreign
audiences; it does not measure database principal resolution.

`BenchmarkSessionCertificateAuthorization` runs the gateway's actual per-request
trusted-ingress SPIFFE/session-cache path at 1/100/10000 platform sessions for
live, revoked, expired, stale-cache and foreign-identity cases. Its TLS state is
already authenticated, matching that application boundary; it does not time a
handshake. `BenchmarkSessionCertificateChain` verifies a real ephemeral P256
chain and session URI. `BenchmarkSessionMTLSTransport` performs real loopback
mutual TLS HTTP requests with verified peer chains, distinguishing a full
handshake from a warmed reused connection. TLS resumption is disabled and the
connection invariants are checked. These synthetic VM measurements do not
include ingress, gateway policy, enrollment, Kafka or Postgres latency.

## Kind request and pipeline measurements

The maintained script supports these bounded scenarios:

| Scenario | Measurement |
|---|---|
| S1 | Sequential adapter tools/call p50/p95/p99 after five warmups |
| S2 | Exactly `PERF_SAMPLES` successful calls distributed across distinct initialized client connections |
| S3 | Admin analytics usage latency, with a parsed successful response |
| S5 | Alternating gateway/direct calls to the same deterministic upstream tool; both raw latency sets and percentile differences |
| S6 | Increasing concurrent clients with separate MCP initialization, fixed requests per client, throughput/latency and a bounded plateau observation |
| S7 | Real gateway tools/call to exact trace/current-server-UID event visibility through the analytics API |

Use an existing healthy disposable Kind installation and supported CLI
server/grant/session/adapter flows from `access-governance`. Keep existing
platform management intact. Point `PERF_DIRECT_URL` at a read-only port-forward
to that **same server's upstream port**, and `PERF_GATEWAY_URL` at its gateway
route. Record the actual server with `PERF_SERVER_NAME` and
`PERF_SERVER_NAMESPACE`; the harness reads its current UID/image/policy.
The tool must return a deterministic result, since S5 verifies gateway/direct
result equality. The default tool is `aaa-ping`; select another using
`PERF_TOOL` and a JSON-object `PERF_TOOL_ARGS` (maximum 64KiB).

Example configuration, after the existing test credentials and port-forwards
are ready:

```bash
export KUBECONFIG=/path/to/contributor-kubeconfig
export PERF_SERVER_NAME=policy-mcp-server PERF_SERVER_NAMESPACE=mcp-servers
export PERF_GATEWAY_URL=http://127.0.0.1:18080/policy-mcp-server/mcp
export PERF_DIRECT_URL=http://127.0.0.1:18095/mcp
export PERF_AUTH_LABEL=same-enrolled-adapter-fixture
export PERF_API_KEY="$KIND_ADMIN_API_KEY"
SCENARIOS=S5,S6,S7 PERF_SAMPLES=100 PERF_CONCURRENCY_LEVELS=1,10,100 \
  PERF_RECORD_BASELINE=1 PERF_OUT_DIR=/private/perf/before \
  PERF_BASELINE_DIR=/private/perf/baseline bash hack/qa/perf-regression.sh
# Same environment and traffic, after the source/image change:
SCENARIOS=S5,S6,S7 PERF_SAMPLES=100 PERF_CONCURRENCY_LEVELS=1,10,100 \
  PERF_OUT_DIR=/private/perf/after PERF_BASELINE_DIR=/private/perf/baseline \
  bash hack/qa/perf-regression.sh
```

If the gateway requires request credentials, provide the existing fixture's
headers through `PERF_MCP_HEADERS_JSON`, or `PERF_CLIENT_CERT`,
`PERF_CLIENT_KEY` and `PERF_CA_FILE` for verified HTTPS. Do not print credentials
or put them in command-line arguments. S3/S7 require an existing admin analytics
key in `PERF_API_KEY`; this replaces automatic UI-key extraction. Shared
authentication headers apply to both gateway/direct routes, which must be the
same disposable fixture. The non-secret `PERF_AUTH_LABEL` describes that
authentication setup. Only header names and argument hashes enter the report.
The helper accepts only loopback targets, verifies the Kind context and actual
node provider IDs even when invoked directly, and never follows redirects.

S6 defaults to 1/10/100 clients, five timed requests per client, maximum 128
clients and a 120-second batch budget. Raise `PERF_MAX_CLIENTS` explicitly for
a bounded 1000-client experiment; its hard ceiling is 1000. There are at most
16 increasing levels, 2000 initialized clients and 10000 timed calls in a
sweep. `PERF_REQUESTS_PER_CLIENT` is bounded to 1–100 and
`PERF_MAX_SECONDS` to 300. Clients use separate connections and, when the server
issues them, distinct MCP session IDs. They share the selected authentication
fixture; this is not a benchmark of 1000 separately enrolled agent identities.
Two consecutive throughput gains below 10% report an observed plateau. If none
occurs, saturation remains **not observed within bounds**. Failed or rejected
calls fail the measurement; they never count as successful throughput or a
usable baseline.

S7 defaults to five samples, 500ms polling and a 60-second per-event deadline,
bounded to 20 samples/120 seconds per event and the batch wall budget above.
Each real tools/call sends a fresh sampled W3C trace ID, requires the gateway's
matching `X-Trace-ID`, and waits for an `mcp.request` event matching that trace,
namespace/server, live UID, tools/call method and HTTP 200. Reported lag runs
from request submission to first successful analytics visibility; it includes
request latency and polling/query delay. It is a polling upper bound, not
independent timing of each ingest/Kafka/processor/ClickHouse stage.

S5 gates both total gateway p95/p99 latency and the recorded gateway-minus-direct
p95/p99 differences against `PERF_REGRESSION_PCT`. A faster direct upstream must
not hide increased gateway overhead. The overhead baseline must be positive:
zero/negative differences have no meaningful relative denominator, so comparison
fails explicitly and requires deliberate recapture of a stable positive baseline.
Raw noisy measurements remain evidence; a negative candidate difference is a
valid reduction against a positive baseline, not a negative request duration.

Every 2xx MCP result must be valid JSON-RPC with the exact response ID and no
RPC/tool error. Responses and tool arguments are bounded; timeouts, missing
counts, non-finite values, inconsistent percentiles, unrelated events and
partial concurrency coverage fail. The fingerprint covers host architecture,
kernel/CPU affinity, node versions/capacity, workload resource budgets, upstream
image/policy, protocol/traffic configuration, authentication label and transports.
Code/image revisions and ephemeral UIDs remain provenance so matching fresh
clusters can be compared. Baselines missing matching environment evidence or
using different coverage fail; `PERF_RECORD_BASELINE=1` creates missing files
only, preserving existing reviewed baselines. Old pre-fingerprint baselines need
deliberate recapture. Recording is not regression acceptance.

## Historical proxy comparison and remaining live evidence

The repository renamed `services/mcp-proxy` to `services/mcp-gateway` in
[`1c3be5ea`](https://github.com/mcp-runtime/mcp-runtime/commit/1c3be5ea).
The current service implements the policy-enforcement role; this checkout has
no separately benchmarked `mcp-pep` binary. No historical mcp-proxy-versus-PEP
latency, saturation or pipeline-lag result was recorded for #692, and this
change makes no performance improvement claim for that transition.

An eventual paired historical run must pin both source/image revisions and
match MCP protocol, tool/result, upstream image, policy/authentication semantics,
TLS/connection reuse, topology and resource budgets. Preserve both fingerprints
and raw S5/S6/S7 output. Older revisions lacking matching protocol, session or
trace/current-UID support are not comparable by disabling the new checks;
record that gap explicitly instead of manufacturing an A/B result.

This source checkpoint has offline/unit coverage and short repeated local
microbenchmark execution only. Recorded Kind gateway/direct baselines, actual
saturation and real pipeline visibility lag remain **unrun**. Additional E2E/
Staging infrastructure and live dispatches are deferred. No staging load tests
or CPU/heap profiles are part of these checks. Other proposed #692 areas such
as streaming payload-size TTFB, enrollment/control-plane scale and slow-ingest
capacity still need separate bounded fixtures and measured evidence.

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

Upstream capacity rejects before dispatch with HTTP 429 / `upstream_backpressure`;
an open circuit returns HTTP 503 / `upstream_unavailable`. Policy decision counters
retain the authorization result for these operational failures. GET session streams
and upgrades have a separate capacity budget; POST SSE responses retain an active
RPC slot. Actual SSE responses have no unary body deadline. A stalled unary body
can time out after HTTP 200 headers, so check the audit `upstream_timeout` reason
and partial-response failure rather than looking only for HTTP 504. See the
gateway upstream bounds in `docs/platform-services.md`.

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
