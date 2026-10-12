#!/usr/bin/env bash
# Explicit Kind-only authz matrix probe; no implicit port-forwards or admin-key stand-ins.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=hack/lib/test-cluster.sh
source "$ROOT/hack/lib/test-cluster.sh"
require_test_cluster
: "${AUTHZ_PLATFORM_URL:?Set the platform-api test URL}"
: "${AUTHZ_RUNTIME_URL:?Set the runtime-api test URL}"
: "${AUTHZ_ANALYTICS_URL:?Set the analytics-api test URL}"
# A real non-admin key must be supplied; UI_API_KEY is an install admin key.
: "${AUTHZ_USER_KEY:?Supply a real non-admin test user API key}"
AUTHZ_ADMIN_KEY="$(kubectl -n "${PLATFORM_NAMESPACE:-mcp-platform}" get secret mcp-platform-api-credentials -o jsonpath='{.data.ADMIN_API_KEYS}' | base64 -d | cut -d, -f1)"
AUTHZ_INGEST_KEY="$(kubectl -n "${OBSERVABILITY_NAMESPACE:-mcp-observability}" get secret mcp-ingest-credentials -o jsonpath='{.data.INGEST_API_KEYS}' | base64 -d | cut -d, -f1)"
export AUTHZ_ADMIN_KEY AUTHZ_INGEST_KEY
python3 "$ROOT/hack/lib/authz-matrix.py" "${MATRIX:-$ROOT/docs/security/authz-matrix.json}"
