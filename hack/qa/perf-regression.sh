#!/usr/bin/env bash
# Kind-only relative performance signals; never load-test production.
# See .codex/skills/cluster-ops/references/performance.md for inputs and limits.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"
export KUBECONFIG="${TEST_KUBECONFIG:-${KUBECONFIG:?Set an explicit contributor KUBECONFIG}}"
export BASE_URL="${BASE_URL:-http://localhost:18080}"
export ADAPTER_URL="${ADAPTER_URL:-http://127.0.0.1:8099/mcp}"
export PERF_GATEWAY_URL="${PERF_GATEWAY_URL:-${BASE_URL}/${PERF_SERVER_NAME:-policy-mcp-server}/mcp}"
export PERF_SAMPLES="${PERF_SAMPLES:-100}"
export PERF_CONCURRENCY="${PERF_CONCURRENCY:-8}"
export PERF_REGRESSION_PCT="${PERF_REGRESSION_PCT:-25}"
SCENARIOS="${SCENARIOS:-S3}"
PERF_OUT_DIR="${PERF_OUT_DIR:-/tmp/mcp-runtime-perf/$(git rev-parse --short HEAD)}"
PERF_BASELINE_DIR="${PERF_BASELINE_DIR:-${HOME}/.cache/mcp-runtime-perf/baseline}"
IFS=',' read -r -a selected_scenarios <<<"$SCENARIOS"
for scenario in "${selected_scenarios[@]}"; do
  [[ "$scenario" =~ ^S([1-3]|[5-7])$ ]] || { echo "Unknown scenario: $scenario" >&2; exit 2; }
done
for tool in kubectl jq python3; do
  command -v "$tool" >/dev/null || { echo "Missing command: $tool" >&2; exit 1; }
done
# shellcheck source=hack/lib/test-cluster.sh
source "$ROOT/hack/lib/test-cluster.sh"
require_test_cluster
require_test_url "$BASE_URL"
require_test_url "$ADAPTER_URL"
require_test_url "$PERF_GATEWAY_URL"
if [[ ",${SCENARIOS}," == *,S5,* ]]; then
  require_test_url "${PERF_DIRECT_URL:?S5 requires a port-forward to the same MCP server upstream}"
fi
if [[ ",${SCENARIOS}," == *,S3,* || ",${SCENARIOS}," == *,S7,* ]]; then
  : "${PERF_API_KEY:?Set the existing disposable Kind admin analytics API key in PERF_API_KEY}"
fi
mkdir -p "$PERF_OUT_DIR" "$PERF_BASELINE_DIR"
python3 "$ROOT/hack/qa/perf_suite.py" fingerprint "$PERF_OUT_DIR"
for scenario in "${selected_scenarios[@]}"; do
  rm -f "${PERF_OUT_DIR}/${scenario}.json"
  echo "=== ${scenario} measurement ==="
  python3 "$ROOT/hack/qa/perf_suite.py" run "$PERF_OUT_DIR" "$scenario"
done
regressions=0
for scenario in "${selected_scenarios[@]}"; do
  if ! python3 "$ROOT/hack/qa/perf_suite.py" compare "$PERF_OUT_DIR" "$PERF_BASELINE_DIR" "$scenario"; then
    regressions=$((regressions + 1))
  fi
done
echo "=== perf-regression SUMMARY out=${PERF_OUT_DIR} failed-comparisons=${regressions} ==="
[[ "$regressions" -eq 0 ]]
