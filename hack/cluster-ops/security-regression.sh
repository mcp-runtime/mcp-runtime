#!/usr/bin/env bash
# Live Kind security regression (deterministic). Prefer this over replaying
# .codex/skills/cluster-ops/references/security-regression.md by hand.
#
# Prerequisites:
#   - contributor-cluster healthy (context test-mcp-runtime)
#   - Traefik port-forward: kubectl -n traefik port-forward svc/traefik 18080:8000
#
# Usage:
#   KUBECONFIG=$HOME/.kube/test-mcp-runtime-config bash hack/cluster-ops/security-regression.sh
#   SUITES=A,E,I bash hack/cluster-ops/security-regression.sh
#   BASE_URL=http://localhost:18080 bash hack/cluster-ops/security-regression.sh
#
# Suites:
#   A  backend auth enforcement
#   E  UI security headers
#   F  login + lockout (best-effort; lockout may need cool-down)
#   G  UI session proxy vs direct API
#   I  live-log secret scan (always recommended)
#   matrix  also run hack/validate-authz-matrix.sh (uses gateway port-forward)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

TEST_KUBECONFIG="${TEST_KUBECONFIG:-${KUBECONFIG:-$HOME/.kube/test-mcp-runtime-config}}"
export KUBECONFIG="$TEST_KUBECONFIG"
BASE_URL="${BASE_URL:-http://localhost:18080}"
SUITES="${SUITES:-A,E,F,G,I}"
NS_PLATFORM="${NS_PLATFORM:-mcp-platform}"
NS_OBSERVABILITY="${NS_OBSERVABILITY:-mcp-observability}"

pass=0
fail=0

log() { printf '%s\n' "$*"; }
ok() { log "PASS $*"; pass=$((pass + 1)); }
bad() { log "FAIL $*"; fail=$((fail + 1)); }

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || { log "FAIL missing command: $1"; exit 1; }
}

need_cmd kubectl
need_cmd curl
need_cmd jq
need_cmd base64

ctx="$(kubectl config current-context 2>/dev/null || true)"
if [[ "$ctx" != "test-mcp-runtime" ]]; then
  log "FAIL expected kube context test-mcp-runtime, got: ${ctx:-none}"
  log "hint: export KUBECONFIG=\$HOME/.kube/test-mcp-runtime-config after contributor-cluster"
  exit 1
fi

if ! curl -fsS -o /dev/null --connect-timeout 2 "${BASE_URL}/" 2>/dev/null; then
  log "FAIL ${BASE_URL} is not reachable"
  log "hint: kubectl -n traefik port-forward svc/traefik 18080:8000"
  exit 1
fi

ADMIN_KEY="$(kubectl get secret mcp-platform-api-credentials -n "$NS_PLATFORM" \
  -o jsonpath='{.data.ADMIN_API_KEYS}' | base64 -d | cut -d, -f1)"
INGEST_KEY="$(kubectl get secret mcp-ingest-credentials -n "$NS_OBSERVABILITY" \
  -o jsonpath='{.data.INGEST_API_KEYS}' | base64 -d | cut -d, -f1)"
if [[ -z "$ADMIN_KEY" || -z "$INGEST_KEY" ]]; then
  log "FAIL could not read ADMIN_API_KEYS / INGEST_API_KEYS from owner Secrets"
  exit 1
fi

code() {
  local method="${1:-GET}"
  local url="$2"
  shift 2
  curl -sS -o /dev/null -w '%{http_code}' -X "$method" "$@" "$url" || echo "000"
}

suite_enabled() {
  [[ ",${SUITES}," == *",$1,"* ]]
}

# --- A: backend auth ---
if suite_enabled A; then
  log "=== suite A: backend auth ==="
  c="$(code GET "${BASE_URL}/api/v1/dashboard/summary")"
  [[ "$c" == "401" ]] && ok "anon dashboard=$c" || bad "anon dashboard want 401 got $c"

  c="$(code GET "${BASE_URL}/api/v1/analytics/usage")"
  [[ "$c" == "401" ]] && ok "anon analytics=$c" || bad "anon analytics want 401 got $c"

  c="$(code GET "${BASE_URL}/api/v1/dashboard/summary" -H "x-api-key: NOPE")"
  [[ "$c" == "401" ]] && ok "bad-key dashboard=$c" || bad "bad-key dashboard want 401 got $c"

  c="$(code GET "${BASE_URL}/api/v1/dashboard/summary" -H "x-api-key: ${INGEST_KEY}")"
  [[ "$c" == "401" || "$c" == "403" ]] && ok "ingest-only dashboard=$c" || bad "ingest-only dashboard want 401/403 got $c"

  c="$(code GET "${BASE_URL}/api/v1/dashboard/summary" -H "x-api-key: ${ADMIN_KEY}")"
  [[ "$c" == "200" ]] && ok "admin dashboard=$c" || bad "admin dashboard want 200 got $c"

  c="$(code POST "${BASE_URL}/api/v1/runtime/grants" \
    -H "x-api-key: ${INGEST_KEY}" -H "content-type: application/json" -d '{}')"
  [[ "$c" == "401" || "$c" == "403" ]] && ok "ingest-write grants=$c" || bad "ingest-write grants want 401/403 got $c"
fi

# --- E: UI security headers ---
if suite_enabled E; then
  log "=== suite E: UI security headers ==="
  hdr="$(mktemp)"
  curl -sSI "${BASE_URL}/" | tr -d '\r' >"$hdr"
  grep -q '^X-Content-Type-Options: nosniff$' "$hdr" && ok "nosniff" || bad "missing X-Content-Type-Options: nosniff"
  grep -qi "Content-Security-Policy:.*frame-ancestors 'none'" "$hdr" && ok "csp frame-ancestors" || bad "missing CSP frame-ancestors none"
  if grep -qi "script-src.*unsafe-inline" "$hdr"; then
    bad "CSP allows unsafe-inline"
  else
    ok "CSP has no unsafe-inline"
  fi
  if grep -qi '^Strict-Transport-Security' "$hdr"; then
    bad "HSTS present on plain HTTP"
  else
    ok "no HSTS on plain HTTP"
  fi
  curl -sSI -H "X-Forwarded-Proto: https" "${BASE_URL}/" | tr -d '\r' >"$hdr"
  grep -qi '^Strict-Transport-Security: max-age=' "$hdr" && ok "HSTS on forwarded HTTPS" || bad "missing HSTS on X-Forwarded-Proto https"
  curl -sSI -H "x-api-key: ${ADMIN_KEY}" "${BASE_URL}/api/v1/dashboard/summary" | tr -d '\r' >"$hdr"
  grep -qi '^Cache-Control:.*no-store' "$hdr" && ok "api Cache-Control no-store" || bad "missing Cache-Control no-store on /api/v1"
  rm -f "$hdr"
fi

# --- F: login + lockout ---
if suite_enabled F; then
  log "=== suite F: login + lockout ==="
  jar="$(mktemp)"
  resp="$(mktemp)"
  code_line="$(curl -sS -i -c "$jar" -H "content-type: application/json" \
    -d '{"email":"test@mcpruntime.org","password":"test@123"}' \
    "${BASE_URL}/auth/login" | tee "$resp" | head -1 | tr -d '\r')"
  if grep -q 'mcp_ui_session' "$jar"; then
    ok "login sets mcp_ui_session (${code_line})"
  else
    bad "login did not set mcp_ui_session (${code_line})"
  fi
  saw_lock=0
  saw_5xx=0
  for i in 1 2 3 4 5 6; do
    c="$(code POST "${BASE_URL}/auth/login" -H "content-type: application/json" \
      -d '{"email":"test@mcpruntime.org","password":"WRONG"}')"
    case "$c" in
      429|403) saw_lock=1 ;;
      5*) saw_5xx=1 ;;
    esac
  done
  [[ "$saw_5xx" -eq 0 ]] && ok "wrong-password loop no 5xx" || bad "wrong-password loop returned 5xx"
  # Lockout is best-effort across shared Kind clusters; warn but do not fail the suite alone.
  if [[ "$saw_lock" -eq 1 ]]; then
    ok "saw lockout status on wrong password"
  else
    log "WARN lockout status not observed (cluster may already be locked or threshold differs)"
  fi
  rm -f "$jar" "$resp"
fi

# --- G: UI proxy vs direct API ---
if suite_enabled G; then
  log "=== suite G: UI session proxy ==="
  if curl -sS "${BASE_URL}/config.js" | grep -qi 'apiKey'; then
    bad "api key leaked via /config.js"
  else
    ok "no apiKey in /config.js"
  fi
  jar="$(mktemp)"
  curl -sS -c "$jar" -H "content-type: application/json" \
    -d '{"email":"test@mcpruntime.org","password":"test@123"}' \
    "${BASE_URL}/auth/login" >/dev/null || true
  c="$(code GET "${BASE_URL}/api/v1/dashboard/summary" -b "$jar")"
  [[ "$c" == "401" ]] && ok "cookie alone on direct api=$c" || bad "cookie alone on direct api want 401 got $c"
  c="$(code GET "${BASE_URL}/api/v1/dashboard/summary" -H "x-api-key: ${ADMIN_KEY}")"
  [[ "$c" == "200" ]] && ok "direct admin key=$c" || bad "direct admin key want 200 got $c"
  rm -f "$jar"
fi

# --- I: live-log secret scan ---
if suite_enabled I; then
  log "=== suite I: live-log secret scan ==="
  hits=0
  pattern='Bearer [A-Za-z0-9._-]{20,}|sk-[A-Za-z0-9]{16,}|eyJ[A-Za-z0-9._-]{20,}|x-api-key:[[:space:]]*[A-Za-z0-9_-]{12,}'
  for ns in mcp-runtime mcp-platform mcp-observability mcp-log-collector mcp-servers traefik registry; do
    if ! kubectl get ns "$ns" >/dev/null 2>&1; then
      continue
    fi
    while IFS= read -r pod; do
      [[ -z "$pod" ]] && continue
      if kubectl logs -n "$ns" "$pod" --all-containers --since=10m 2>/dev/null | grep -aE "$pattern" >/dev/null; then
        bad "secret-like pattern in logs ns=${ns} pod=${pod} (value redacted)"
        hits=$((hits + 1))
      fi
    done < <(kubectl get pods -n "$ns" -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null || true)
  done
  [[ "$hits" -eq 0 ]] && ok "no secret-like patterns in recent logs"
fi

# --- optional authz matrix ---
if suite_enabled matrix; then
  log "=== suite matrix: authz-matrix ==="
  if bash "${ROOT}/hack/validate-authz-matrix.sh" "${AUTHZ_BASE:-http://127.0.0.1:18083}"; then
    ok "validate-authz-matrix"
  else
    bad "validate-authz-matrix"
  fi
fi

log "=== security-regression SUMMARY pass=${pass} fail=${fail} suites=${SUITES} ==="
[[ "$fail" -eq 0 ]]
