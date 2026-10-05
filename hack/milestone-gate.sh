#!/usr/bin/env bash
# Full milestone gate: live Kind cluster checks + Trivy image scans for split API services.
# Usage: bash hack/milestone-gate.sh [validate-api-split-milestone.sh args]
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

SCOPE="${1:-all}"

echo "=== Step 1/3: Trivy platform API images ==="
bash hack/trivy-platform-images.sh platform-api analytics-api runtime-api

echo "=== Step 2/3: OpenAPI breaking-change check (optional) ==="
bash hack/oasdiff-openapi.sh

echo "=== Step 3/3: Live cluster validation ($SCOPE) ==="
bash hack/validate-api-split-milestone.sh "$SCOPE"

echo "=== MILESTONE GATE OK ==="
