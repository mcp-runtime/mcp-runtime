#!/usr/bin/env bash
# Live-cluster authz matrix probe via Traefik gateway (split API services).
# Usage: bash hack/validate-authz-matrix.sh [base-url]
set -euo pipefail

BASE="${1:-http://127.0.0.1:18083}"
MATRIX="${MATRIX:-docs/security/authz-matrix.json}"

if [ ! -f "$MATRIX" ]; then
  echo "FAIL missing $MATRIX"
  exit 1
fi

PLATFORM_NS="${PLATFORM_NAMESPACE:-mcp-platform}"
OBSERVABILITY_NS="${OBSERVABILITY_NAMESPACE:-mcp-observability}"
if ! curl -fsS -o /dev/null "${BASE}/" 2>/dev/null; then
  echo "Port-forward Traefik gateway: kubectl -n $PLATFORM_NS port-forward svc/mcp-platform-gateway 18083:8083"
  kubectl -n "$PLATFORM_NS" port-forward svc/mcp-platform-gateway 18083:8083 >/tmp/pf-gateway-authz.log 2>&1 &
  sleep 2
fi

ADMIN_KEY=$(kubectl -n "$PLATFORM_NS" get secret mcp-platform-api-credentials -o jsonpath='{.data.ADMIN_API_KEYS}' | base64 -d | cut -d, -f1)
UI_KEY=$(kubectl -n "$PLATFORM_NS" get secret mcp-ui-credentials -o jsonpath='{.data.UI_API_KEY}' | base64 -d)
INGEST_KEY=$(kubectl -n "$OBSERVABILITY_NS" get secret mcp-ingest-credentials -o jsonpath='{.data.INGEST_API_KEYS}' | base64 -d | cut -d, -f1)

pass=0
fail=0

while IFS= read -r row; do
  path=$(echo "$row" | jq -r .path)
  method=$(echo "$row" | jq -r .method)
  role=$(echo "$row" | jq -r .role)
  want=$(echo "$row" | jq -r .expect)
  body=$(echo "$row" | jq -r '.body // empty')
  headers=()
  case "$role" in
    anon) ;;
    user-key) headers=(-H "x-api-key: $UI_KEY") ;;
    admin-key) headers=(-H "x-api-key: $ADMIN_KEY") ;;
    ingest-key) headers=(-H "x-api-key: $INGEST_KEY") ;;
    *) echo "SKIP unknown role $role for $method $path"; continue ;;
  esac
  request_args=()
  if [ -n "$body" ]; then
    request_args=(-H "content-type: application/json" --data "$body")
  fi
  got=$(curl -sS -o /dev/null -w '%{http_code}' -X "$method" "${headers[@]}" "${request_args[@]}" "${BASE}${path}" || echo "000")
  if [ "$got" = "$want" ]; then
    echo "PASS $method $path role=$role"
    pass=$((pass + 1))
  else
    echo "FAIL $method $path role=$role expected=$want got=$got"
    fail=$((fail + 1))
  fi
done < <(jq -c '.[]' "$MATRIX")

echo "=== authz-matrix SUMMARY pass=$pass fail=$fail ==="
[ "$fail" -eq 0 ]
