#!/usr/bin/env bash
# Offline repository checks. Cluster QA belongs in test/e2e/qa-e2e.sh.
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$root"
for tool in go python3 bash; do command -v "$tool" >/dev/null || { echo "Missing $tool" >&2; exit 1; }; done
hack/ci/check-scripts.sh
for module in . services/platform-api services/runtime-api services/analytics-api; do
  hack/ci/go-test.sh "$module" -short -count=1 ./...
done
echo 'PASS: automation regression tests, shell syntax and root/split-service unit tests'
echo 'Cluster, image vulnerability and browser checks are separate gates; see hack/README.md.'
