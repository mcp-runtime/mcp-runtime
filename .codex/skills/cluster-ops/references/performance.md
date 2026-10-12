# Performance regression QA

Use the maintained Kind-only script. Select an explicit contributor kubeconfig;
the harness checks both context and actual node provider IDs.

```bash
export KUBECONFIG=/path/to/contributor-kubeconfig
SCENARIOS=S3 bash hack/cluster-ops/perf-regression.sh
# Adapter path, with a running session-bound adapter:
SCENARIOS=S1,S2,S3 bash hack/cluster-ops/perf-regression.sh
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
