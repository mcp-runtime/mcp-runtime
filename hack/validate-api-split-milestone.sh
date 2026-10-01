#!/usr/bin/env bash
# Live-cluster gate for api-service-split milestones (kind-mcp-runtime).
# Usage: bash hack/validate-api-split-milestone.sh [m13|m14|m15|m16|m2|all]
set -euo pipefail

PLATFORM_NS="${PLATFORM_NAMESPACE:-mcp-platform}"
OBSERVABILITY_NS="${OBSERVABILITY_NAMESPACE:-mcp-observability}"
SCOPE="${1:-all}"

pass=0
fail=0

check() {
  local name="$1" expect="$2" got="$3"
  if [ "$got" = "$expect" ]; then
    echo "PASS $name"
    pass=$((pass + 1))
  else
    echo "FAIL $name expected=$expect got=$got"
    fail=$((fail + 1))
  fi
}

ADMIN_KEY=$(kubectl -n "$PLATFORM_NS" get secret mcp-platform-api-credentials -o jsonpath='{.data.ADMIN_API_KEYS}' | base64 -d | cut -d, -f1)
API_KEY=$(kubectl -n "$PLATFORM_NS" get secret mcp-platform-api-credentials -o jsonpath='{.data.API_KEYS}' | base64 -d | cut -d, -f1)
INTERNAL_TOKEN=$(kubectl -n "$PLATFORM_NS" get secret mcp-platform-api-credentials -o jsonpath='{.data.INTERNAL_AUTH_TOKEN}' | base64 -d)

pkill -f 'port-forward svc/mcp-' 2>/dev/null || true
sleep 1

if [ "$SCOPE" = "m15" ] || [ "$SCOPE" = "m16" ] || [ "$SCOPE" = "m2" ] || [ "$SCOPE" = "all" ]; then
  kubectl -n "$PLATFORM_NS" port-forward svc/mcp-platform-api 18080:8080 >/tmp/pf-platform.log 2>&1 &
fi
if [ "$SCOPE" = "m13" ] || [ "$SCOPE" = "m15" ] || [ "$SCOPE" = "m16" ] || [ "$SCOPE" = "m2" ] || [ "$SCOPE" = "all" ]; then
  kubectl -n "$OBSERVABILITY_NS" port-forward svc/mcp-analytics-api 18095:8085 >/tmp/pf-analytics.log 2>&1 &
fi
if [ "$SCOPE" = "m14" ] || [ "$SCOPE" = "m15" ] || [ "$SCOPE" = "m16" ] || [ "$SCOPE" = "m2" ] || [ "$SCOPE" = "all" ]; then
  kubectl -n "$PLATFORM_NS" port-forward svc/mcp-runtime-api 18084:8084 >/tmp/pf-runtime.log 2>&1 &
fi
sleep 3

JWT=""
if [ "$SCOPE" = "m14" ] || [ "$SCOPE" = "m15" ] || [ "$SCOPE" = "m16" ] || [ "$SCOPE" = "m2" ] || [ "$SCOPE" = "all" ]; then
  JWT=$(curl -sS -X POST "http://127.0.0.1:18080/api/v1/auth/login" \
    -H 'content-type: application/json' \
    -d '{"email":"admin@mcpruntime.org","password":"admin@123"}' \
    | python3 -c 'import json,sys; print(json.load(sys.stdin)["access_token"])')
fi

if [ "$SCOPE" = "m13" ] || [ "$SCOPE" = "all" ]; then
  echo "=== M1.3 analytics-api ==="
  check analytics-health 200 "$(curl -sS -o /dev/null -w '%{http_code}' http://127.0.0.1:18095/health)"
  check analytics-ready 200 "$(curl -sS -o /dev/null -w '%{http_code}' http://127.0.0.1:18095/ready)"
  check analytics-stats-admin 200 "$(curl -sS -o /dev/null -w '%{http_code}' -H "x-api-key: $ADMIN_KEY" http://127.0.0.1:18095/api/v1/stats)"
  check analytics-events 200 "$(curl -sS -o /dev/null -w '%{http_code}' -H "x-api-key: $ADMIN_KEY" 'http://127.0.0.1:18095/api/v1/events?limit=1')"
  if [ -n "$JWT" ]; then
    check analytics-stats-jwt 200 "$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $JWT" http://127.0.0.1:18095/api/v1/stats)"
  fi
  check analytics-stats-nonadmin-403 403 "$(curl -sS -o /dev/null -w '%{http_code}' -H "x-api-key: $API_KEY" http://127.0.0.1:18095/api/v1/stats)"
fi

if [ "$SCOPE" = "m14" ] || [ "$SCOPE" = "all" ]; then
  echo "=== M1.4 runtime-api ==="
  check runtime-health 200 "$(curl -sS -o /dev/null -w '%{http_code}' http://127.0.0.1:18084/health)"
  check runtime-ready 200 "$(curl -sS -o /dev/null -w '%{http_code}' http://127.0.0.1:18084/ready)"
  check runtime-servers-v1 200 "$(curl -sS -o /dev/null -w '%{http_code}' -H "x-api-key: $ADMIN_KEY" http://127.0.0.1:18084/api/v1/runtime/servers)"
  if [ -n "$JWT" ]; then
    check runtime-servers-jwt 200 "$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $JWT" http://127.0.0.1:18084/api/v1/runtime/servers)"
  fi
fi

if [ "$SCOPE" = "m15" ] || [ "$SCOPE" = "all" ]; then
  echo "=== M1.5 platform-api ==="
  check platform-health 200 "$(curl -sS -o /dev/null -w '%{http_code}' http://127.0.0.1:18080/health)"
  check platform-ready 200 "$(curl -sS -o /dev/null -w '%{http_code}' http://127.0.0.1:18080/ready)"
  check platform-login-v1 200 "$(curl -sS -o /dev/null -w '%{http_code}' -X POST http://127.0.0.1:18080/api/v1/auth/login -H 'content-type: application/json' -d '{"email":"admin@mcpruntime.org","password":"admin@123"}')"
  check platform-auth-me-v1 200 "$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $JWT" http://127.0.0.1:18080/api/v1/auth/me)"
  RESOLVE_CODE=$(curl -sS -o /dev/null -w '%{http_code}' -X POST http://127.0.0.1:18080/internal/auth/resolve \
    -H "Authorization: Bearer $INTERNAL_TOKEN" \
    -H 'content-type: application/json' \
    -d "{\"api_key\":\"$ADMIN_KEY\"}")
  check platform-internal-resolve 200 "$RESOLVE_CODE"
fi

if [ "$SCOPE" = "m16" ] || [ "$SCOPE" = "all" ]; then
  echo "=== M1.6 /api/v1 only ==="
  check platform-no-legacy-login 404 "$(curl -sS -o /dev/null -w '%{http_code}' -X POST http://127.0.0.1:18080/api/auth/login -H 'content-type: application/json' -d '{"email":"admin@mcpruntime.org","password":"admin@123"}')"
  check platform-no-legacy-me 404 "$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $JWT" http://127.0.0.1:18080/api/auth/me)"
  check platform-no-foreign-runtime 404 "$(curl -sS -o /dev/null -w '%{http_code}' -H "x-api-key: $ADMIN_KEY" http://127.0.0.1:18080/api/v1/runtime/servers)"
  check runtime-no-legacy-servers 404 "$(curl -sS -o /dev/null -w '%{http_code}' -H "x-api-key: $ADMIN_KEY" http://127.0.0.1:18084/api/runtime/servers)"
  check runtime-no-foreign-login 404 "$(curl -sS -o /dev/null -w '%{http_code}' -X POST http://127.0.0.1:18084/api/v1/auth/login -H 'content-type: application/json' -d '{"email":"admin@mcpruntime.org","password":"admin@123"}')"
  check analytics-no-legacy-stats 404 "$(curl -sS -o /dev/null -w '%{http_code}' -H "x-api-key: $ADMIN_KEY" http://127.0.0.1:18095/api/stats)"
  check analytics-no-foreign-runtime 404 "$(curl -sS -o /dev/null -w '%{http_code}' -H "x-api-key: $ADMIN_KEY" http://127.0.0.1:18095/api/v1/runtime/servers)"
fi

if [ "$SCOPE" = "m2" ] || [ "$SCOPE" = "all" ]; then
  echo "=== M2 split cutover ==="
  MONOLITH_REPLICAS=$(kubectl -n "$PLATFORM_NS" get deploy mcp-api -o jsonpath='{.spec.replicas}' 2>/dev/null || echo "absent")
  if [ "$MONOLITH_REPLICAS" = "absent" ] || [ "${MONOLITH_REPLICAS:-0}" = "0" ]; then
    check monolith-scaled-to-zero 0 "${MONOLITH_REPLICAS:-0}"
  else
    check monolith-scaled-to-zero 0 "$MONOLITH_REPLICAS"
  fi

  pkill -f 'port-forward svc/mcp-platform-gateway' 2>/dev/null || true
  kubectl -n "$PLATFORM_NS" port-forward svc/mcp-platform-gateway 18083:8083 >/tmp/pf-gateway.log 2>&1 &
  sleep 2
  echo "=== M2.3 Traefik gateway /api/v1 routing ==="
  check gateway-v1-login 200 "$(curl -sS -o /dev/null -w '%{http_code}' -X POST http://127.0.0.1:18083/api/v1/auth/login -H 'content-type: application/json' -d '{"email":"admin@mcpruntime.org","password":"admin@123"}')"
  check gateway-v1-stats 200 "$(curl -sS -o /dev/null -w '%{http_code}' -H "x-api-key: $ADMIN_KEY" http://127.0.0.1:18083/api/v1/stats)"
  check gateway-v1-runtime-servers 200 "$(curl -sS -o /dev/null -w '%{http_code}' -H "x-api-key: $ADMIN_KEY" http://127.0.0.1:18083/api/v1/runtime/servers)"
  check gateway-legacy-api 404 "$(curl -sS -o /dev/null -w '%{http_code}' -H "x-api-key: $ADMIN_KEY" http://127.0.0.1:18083/api/stats)"
  check platform-openapi 200 "$(curl -sS -o /dev/null -w '%{http_code}' http://127.0.0.1:18080/api/v1/openapi.yaml)"
  check runtime-openapi 200 "$(curl -sS -o /dev/null -w '%{http_code}' http://127.0.0.1:18084/api/v1/openapi.yaml)"
  check analytics-openapi 200 "$(curl -sS -o /dev/null -w '%{http_code}' http://127.0.0.1:18095/api/v1/openapi.yaml)"
  if [ -n "$ADMIN_KEY" ]; then
    BODY=$(curl -sS -H "x-api-key: $ADMIN_KEY" 'http://127.0.0.1:18083/api/v1/events?limit=1')
    echo "$BODY" | python3 -c 'import json,sys; d=json.load(sys.stdin); assert "meta" in d, d' >/dev/null 2>&1 \
      && check analytics-events-meta present "present" \
      || check analytics-events-meta present "missing"
  fi
fi

pkill -f 'port-forward svc/mcp-' 2>/dev/null || true
echo "=== SUMMARY pass=$pass fail=$fail ==="
[ "$fail" -eq 0 ]
