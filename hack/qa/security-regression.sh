#!/usr/bin/env bash
# Live Kind security regression (deterministic). Prefer this over replaying
# .codex/skills/cluster-ops/references/security-regression.md by hand.
#
# Prerequisites:
#   - contributor-cluster healthy (context test-mcp-runtime)
#   - Traefik port-forward: kubectl -n traefik port-forward svc/traefik 18080:8000
#
# Usage:
#   KUBECONFIG=$HOME/.kube/test-mcp-runtime-config bash hack/qa/security-regression.sh
#   SUITES=A,E,I bash hack/qa/security-regression.sh
#   BASE_URL=http://localhost:18080 bash hack/qa/security-regression.sh
#
# Suites:
#   A  backend auth enforcement
#   E  UI security headers
#   F  login + lockout (fresh test account; selected check must pass)
#   G  UI session proxy vs direct API
#   I  live-log secret scan (always recommended)
#   matrix  explicit loopback service URLs and non-admin credentials
set -euo pipefail
umask 077
scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

TEST_KUBECONFIG="${TEST_KUBECONFIG:-${KUBECONFIG:-$HOME/.kube/test-mcp-runtime-config}}"
export KUBECONFIG="$TEST_KUBECONFIG"
BASE_URL="${BASE_URL:-http://localhost:18080}"
SUITES="${SUITES:-A,E,F,G,I}"
IFS=',' read -r -a selected_suites <<<"$SUITES"
for suite in "${selected_suites[@]}"; do
  case "$suite" in A|E|F|G|I|matrix) ;; *) echo "Unknown suite: $suite" >&2; exit 2 ;; esac
done
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

# shellcheck source=hack/lib/test-cluster.sh
source "$ROOT/hack/lib/test-cluster.sh"
require_test_cluster
require_test_url "$BASE_URL"


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
  if [[ "$c" == "401" ]]; then ok "anon dashboard=$c"; else bad "anon dashboard want 401 got $c"; fi

  c="$(code GET "${BASE_URL}/api/v1/analytics/usage")"
  if [[ "$c" == "401" ]]; then ok "anon analytics=$c"; else bad "anon analytics want 401 got $c"; fi

  c="$(code GET "${BASE_URL}/api/v1/dashboard/summary" -H "x-api-key: NOPE")"
  if [[ "$c" == "401" ]]; then ok "bad-key dashboard=$c"; else bad "bad-key dashboard want 401 got $c"; fi

  c="$(code GET "${BASE_URL}/api/v1/dashboard/summary" -H "x-api-key: ${INGEST_KEY}")"
  if [[ "$c" == "401" || "$c" == "403" ]]; then ok "ingest-only dashboard=$c"; else bad "ingest-only dashboard want 401/403 got $c"; fi

  c="$(code GET "${BASE_URL}/api/v1/dashboard/summary" -H "x-api-key: ${ADMIN_KEY}")"
  if [[ "$c" == "200" ]]; then ok "admin dashboard=$c"; else bad "admin dashboard want 200 got $c"; fi

  c="$(code POST "${BASE_URL}/api/v1/runtime/grants" \
    -H "x-api-key: ${INGEST_KEY}" -H "content-type: application/json" -d '{}')"
  if [[ "$c" == "401" || "$c" == "403" ]]; then ok "ingest-write grants=$c"; else bad "ingest-write grants want 401/403 got $c"; fi
fi

# --- E: UI security headers ---
if suite_enabled E; then
  log "=== suite E: UI security headers ==="
  hdr="$scratch/headers"
  curl -sSI "${BASE_URL}/" | tr -d '\r' >"$hdr"
  if grep -q '^X-Content-Type-Options: nosniff$' "$hdr"; then ok "nosniff"; else bad "missing X-Content-Type-Options: nosniff"; fi
  if grep -qi "Content-Security-Policy:.*frame-ancestors 'none'" "$hdr"; then ok "csp frame-ancestors"; else bad "missing CSP frame-ancestors none"; fi
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
  if grep -qi '^Strict-Transport-Security: max-age=' "$hdr"; then ok "HSTS on forwarded HTTPS"; else bad "missing HSTS on X-Forwarded-Proto https"; fi
  curl -sSI -H "x-api-key: ${ADMIN_KEY}" "${BASE_URL}/api/v1/dashboard/summary" | tr -d '\r' >"$hdr"
  if grep -qi '^Cache-Control:.*no-store' "$hdr"; then ok "api Cache-Control no-store"; else bad "missing Cache-Control no-store on /api/v1"; fi
  rm -f "$hdr"
fi

# --- G: UI proxy vs direct API ---
if suite_enabled G; then
  log "=== suite G: UI session proxy ==="
  if curl -sS "${BASE_URL}/config.js" | grep -qi 'apiKey'; then
    bad "api key leaked via /config.js"
  else
    ok "no apiKey in /config.js"
  fi
  jar="$scratch/cookies"
  curl -sS -c "$jar" -H "content-type: application/json" \
    -d '{"email":"test@mcpruntime.org","password":"test@123"}' \
    "${BASE_URL}/auth/login" >/dev/null
  if grep -q 'mcp_ui_session' "$jar"; then ok "session proxy test logged in"; else bad "session proxy login failed"; fi
  c="$(code GET "${BASE_URL}/api/ui/v1/runtime/servers" -b "$jar")"
  if [[ "$c" == 200 ]]; then ok "cookie-backed UI proxy=$c"; else bad "cookie-backed UI proxy want 200 got $c"; fi
  c="$(code POST "${BASE_URL}/api/ui/v1/runtime/grants" -b "$jar" -H 'content-type: application/json' -d '{}')"
  if [[ "$c" == 403 ]]; then ok "UI proxy rejects write without CSRF"; else bad "UI proxy missing CSRF guard, got $c"; fi
  c="$(code GET "${BASE_URL}/api/v1/dashboard/summary" -b "$jar")"
  if [[ "$c" == "401" ]]; then ok "cookie alone on direct api=$c"; else bad "cookie alone on direct api want 401 got $c"; fi
  c="$(code GET "${BASE_URL}/api/v1/dashboard/summary" -H "x-api-key: ${ADMIN_KEY}")"
  if [[ "$c" == "200" ]]; then ok "direct admin key=$c"; else bad "direct admin key want 200 got $c"; fi
  rm -f "$jar"
fi

# --- F: login + lockout ---
if suite_enabled F; then
  log "=== suite F: login + lockout ==="
  jar="$scratch/cookies"
  resp="$scratch/response"
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
  for _attempt in 1 2 3 4 5 6; do
    c="$(code POST "${BASE_URL}/auth/login" -H "content-type: application/json" \
      -d '{"email":"test@mcpruntime.org","password":"WRONG"}')"
    case "$c" in
      429|403) saw_lock=1 ;;
      5*) saw_5xx=1 ;;
    esac
  done
  if [[ "$saw_5xx" -eq 0 ]]; then ok "wrong-password loop no 5xx"; else bad "wrong-password loop returned 5xx"; fi
  # A selected lockout check must enforce the expected behavior.
  if [[ "$saw_lock" -eq 1 ]]; then
    ok "saw lockout status on wrong password"
  else
    bad "lockout status not observed; use a fresh disposable account/cluster"
  fi
  rm -f "$jar" "$resp"
fi

# --- I: live-log secret scan ---
if suite_enabled I; then
  log "=== suite I: live-log secret scan ==="
  hits=0
  scanned=0
  pattern='Bearer [A-Za-z0-9._-]{20,}|sk-[A-Za-z0-9]{16,}|eyJ[A-Za-z0-9._-]{20,}|x-api-key:[[:space:]]*[A-Za-z0-9_-]{12,}'
  for ns in mcp-runtime mcp-platform mcp-observability mcp-log-collector mcp-servers traefik registry; do
    if ! kubectl get ns "$ns" >/dev/null 2>&1; then
      continue
    fi
    while IFS= read -r pod; do
      [[ -z "$pod" ]] && continue
      if ! kubectl logs -n "$ns" "$pod" --all-containers --since=10m >"$scratch/pod.log"; then
        bad "could not read logs ns=$ns pod=$pod"; continue
      fi
      scanned=$((scanned + 1))
      if grep -aE "$pattern" "$scratch/pod.log" >/dev/null; then
        bad "secret-like pattern in logs ns=${ns} pod=${pod} (value redacted)"
        hits=$((hits + 1))
      fi
    done < <(kubectl get pods -n "$ns" -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null || true)
  done
  [[ "$scanned" -gt 0 ]] || bad "no pod logs were inspected"
  if [[ "$hits" -eq 0 && "$scanned" -gt 0 ]]; then ok "no secret-like patterns in $scanned inspected pod logs"; fi
fi

# --- optional authz matrix ---
if suite_enabled matrix; then
  log "=== suite matrix: authz-matrix ==="
  if bash "${ROOT}/hack/qa/validate-authz-matrix.sh"; then
    ok "validate-authz-matrix"
  else
    bad "validate-authz-matrix"
  fi
fi

log "=== security-regression SUMMARY pass=${pass} fail=${fail} suites=${SUITES} ==="
[[ "$fail" -eq 0 ]]
