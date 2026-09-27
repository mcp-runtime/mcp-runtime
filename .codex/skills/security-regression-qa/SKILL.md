---
name: security-regression-qa
description: Real-cluster security regression QA — backend auth enforcement, grant/session policy, gateway deny paths, audit emission, trust escalation, UI/API security headers, HTTPS redirect modes, and secret-leak scanning in live logs. Use when Codex is asked to verify a change did not regress auth, governance, gateway policy, audit, or UI security headers on a live cluster; or for pre-release security-regression sweeps. Complements static-only change-security-audit/platform-security-audit with real traffic. Assumes contributor-cluster-setup has run.
---

# Security Regression QA

## Overview

`change-security-audit` and `platform-security-audit` are **static** reviews — diff,
code paths, scanner output. This skill is the **dynamic** counterpart: it
fires real requests at the live cluster and confirms the security
invariants actually hold at runtime. It catches policy-materialization
regressions, header middleware regressions, and audit-event regressions that
static review and unit tests miss.

Threat profiles in scope:

- **anonymous → user** (no key, bad key, expired session)
- **user → admin** (UI key vs admin key vs ingest-only key)
- **agent A → agent B** (grant scoping by `humanID`/`agentID`/`serverRef`)
- **low trust → high trust** tool (consent gating)
- **internal pod → secret** (reach API/proxy/operator secrets unprivileged)
  → for cluster RBAC/PSS coverage, defer to `kubernetes-hardening-audit`.

Regression evidence contract: for each touched security surface, capture at
least one live allow path and one live deny path, plus audit/log/header evidence
where that invariant applies. Static code review can explain a finding, but it
cannot close this skill as passed.

## Step 1 — Confirm precondition

```bash
TEST_KUBECONFIG="${TEST_KUBECONFIG:-$HOME/.kube/test-mcp-runtime-config}"
kubectl --kubeconfig "$TEST_KUBECONFIG" config current-context \
  | grep -qx test-mcp-runtime \
  || { echo "Run contributor-cluster-setup first"; exit 1; }
export KUBECONFIG="$TEST_KUBECONFIG"
./bin/mcp-runtime cluster doctor
```

Pull admin and ingest keys separately; do not assume a UI key is authorized
as an admin key. Never echo keys into the report.

```bash
ADMIN_KEY="$(kubectl get secret mcp-sentinel-secrets -n mcp-sentinel \
  -o jsonpath='{.data.ADMIN_API_KEYS}' | base64 -d | cut -d, -f1)"
INGEST_KEY="$(kubectl get secret mcp-sentinel-secrets -n mcp-sentinel \
  -o jsonpath='{.data.INGEST_API_KEYS}' | base64 -d | cut -d, -f1)"
test -n "$ADMIN_KEY" || { echo "Failed to retrieve ADMIN_API_KEYS"; exit 1; }
test -n "$INGEST_KEY" || { echo "Failed to retrieve INGEST_KEY"; exit 1; }
```

## Step 2 — Choose mode

- **head-only**. Run the full backend + UI security matrix.
- **git-range** (`BASE=<merge-base>`, default `origin/main`). Trim by diff.

Sub-suites by changed paths:

| Diff touches | Required sub-suites |
|---|---|
| `services/platform-api/**`, `services/runtime-api/**`, `services/analytics-api/**`, `pkg/access/**` | A. Backend auth, B. Grants/sessions, C. Audit |
| `services/mcp-gateway/**`, `internal/operator/**` (policy/render) | B. Grants/sessions, D. Trust escalation, C. Audit |
| `services/ui/**` (middleware, login, proxy) | E. UI security headers, F. Login + lockout, G. UI→API proxy |
| `config/ingress/**`, `traefik-plugins/**` | H. Ingress + PII redactor, E (re-run) |
| `k8s/**` Secrets / SA / RBAC | Hand off to `kubernetes-hardening-audit` |

Always run **I. Live-log secret scan** regardless of diff.

## Step 3 — Sub-suite A: Backend auth enforcement

```bash
# Anonymous → 401 on admin paths.
curl -sS -o /dev/null -w "anon=%{http_code}\n" \
  http://localhost:18080/api/v1/dashboard/summary           # want 401
curl -sS -o /dev/null -w "anon=%{http_code}\n" \
  http://localhost:18080/api/v1/analytics/usage             # want 401

# Bad key → 401.
curl -sS -o /dev/null -w "bad=%{http_code}\n" \
  -H "x-api-key: NOPE" http://localhost:18080/api/v1/dashboard/summary

# Ingest-only key on admin → 401/403 (must NOT be admin).
curl -sS -o /dev/null -w "ingest_only=%{http_code}\n" \
  -H "x-api-key: $INGEST_KEY" http://localhost:18080/api/v1/dashboard/summary

# Admin key → 200.
curl -sS -o /dev/null -w "admin=%{http_code}\n" \
  -H "x-api-key: $ADMIN_KEY" http://localhost:18080/api/v1/dashboard/summary

# Mutating admin endpoints require admin (not just user). Try a write with
# only the ingest key and confirm 401/403:
curl -sS -o /dev/null -w "ingest_write=%{http_code}\n" -X POST \
  -H "x-api-key: $INGEST_KEY" -H "content-type: application/json" \
  -d '{}' http://localhost:18080/api/v1/runtime/grants
```

Any admin response code other than 200 with `$ADMIN_KEY` is a finding. Any non-401/403
on the anonymous / bad-key / ingest-only paths is a **High** severity finding —
matches `RequireRole` enforcement in each split service `routes.go`.

## Step 4 — Sub-suite B: Grants & sessions enforce on the gateway

Baseline traffic (should succeed):

```bash
BASE=http://localhost:18080/oauth-example-go-2025-11-25-gateway/mcp
PROTO=2025-06-18
H=(-H "content-type: application/json" -H "accept: application/json, text/event-stream"
   -H "Mcp-Protocol-Version: $PROTO"
   -H "X-MCP-Human-ID: local-user" -H "X-MCP-Agent-ID: local-agent"
   -H "X-MCP-Agent-Session: local-session")
init() {
  SESSION="$(curl -si "${H[@]}" \
    -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' "$BASE" \
    | awk -F': ' 'tolower($1)=="mcp-session-id"{print $2}' | tr -d '\r')"
  curl -sS "${H[@]}" -H "Mcp-Session-Id: $SESSION" \
    -d '{"jsonrpc":"2.0","method":"notifications/initialized"}' "$BASE" >/dev/null
}
call() {
  curl -sS "${H[@]}" -H "Mcp-Session-Id: $SESSION" \
    -d "{\"jsonrpc\":\"2.0\",\"id\":9,\"method\":\"tools/call\",\"params\":$1}" "$BASE"
}

init
call '{"name":"add","arguments":{"a":2,"b":3}}'      # want allow
```

Toggle the grant off → expect deny within a few seconds (sidecar reload):

```bash
kubectl patch mcpaccessgrant workspace-assistant-local -n mcp-servers \
  --type=merge -p '{"spec":{"disabled":true}}'

sleep 8
init
RESP="$(call '{"name":"add","arguments":{"a":2,"b":3}}')"
echo "$RESP" | grep -qiE 'tool_not_granted|denied|forbidden|policy' \
  || { echo "FAIL: disabled grant still allowed"; echo "$RESP"; }
```

Re-enable to leave the cluster in a working state for downstream skills:

```bash
kubectl patch mcpaccessgrant workspace-assistant-local -n mcp-servers \
  --type=merge -p '{"spec":{"disabled":false}}'
kubectl apply -f /tmp/workspace-assistant-access.yaml
sleep 8
```

Revoke the session → expect deny:

```bash
# Apply a Revoked-state session and re-test.
kubectl patch mcpagentsession local-session -n mcp-servers --type=merge \
  -p '{"spec":{"revoked":true}}' 2>/dev/null \
  || kubectl annotate mcpagentsession local-session -n mcp-servers \
       qa.mcpruntime.org/revoke="$(date +%s)" --overwrite
sleep 8
init
call '{"name":"add","arguments":{"a":2,"b":3}}' | grep -qiE 'session_revoked|session_not_found|denied' \
  || echo "FAIL: revoked session still allowed"
kubectl patch mcpagentsession local-session -n mcp-servers --type=merge \
  -p '{"spec":{"revoked":false}}' 2>/dev/null || true
sleep 8
```

Cross-tenant scoping — try the call with a different `humanID`/`agentID` that
has no grant:

```bash
H2=("${H[@]/X-MCP-Human-ID: local-user/X-MCP-Human-ID: other-user}")
SESSION_OTHER="$(curl -si "${H2[@]}" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' "$BASE" \
  | awk -F': ' 'tolower($1)=="mcp-session-id"{print $2}' | tr -d '\r')"
curl -sS "${H2[@]}" -H "Mcp-Session-Id: $SESSION_OTHER" \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized"}' "$BASE" >/dev/null
RESP="$(curl -sS "${H2[@]}" -H "Mcp-Session-Id: $SESSION_OTHER" \
  -d '{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"add","arguments":{"a":1,"b":1}}}' "$BASE")"
echo "$RESP" | grep -qiE 'denied|forbidden|no.*grant' \
  || echo "FAIL: ungranted subject allowed"
```

## Step 5 — Sub-suite C: Audit emission for allow + deny

Allow + deny paths must both emit audit events; missing audit on deny is a
common regression. `/api/v1/events` reads from ClickHouse — a `502` here can
mean the audit path regressed, or it can mean the analytics stack itself is
down (ClickHouse/Kafka `CrashLoopBackOff` from local PVC corruption is a real,
previously-seen failure, unrelated to any code change). Check
`cluster-troubleshooting/reference.md` for that signature before treating
a `502` as an audit-path finding.

```bash
BEFORE="$(curl -sS -H "x-api-key: $ADMIN_KEY" \
  "http://localhost:18080/api/v1/events?server=oauth-example-go-2025-11-25-gateway&limit=100" \
  | jq '.events | length // length // 0')"
# fire one allow + one deny (tool not in policy)
init
call '{"name":"add","arguments":{"a":1,"b":1}}'               >/dev/null
call '{"name":"definitely-not-a-tool","arguments":{}}'        >/dev/null
sleep 3
AFTER="$(curl -sS -H "x-api-key: $ADMIN_KEY" \
  "http://localhost:18080/api/v1/events?server=oauth-example-go-2025-11-25-gateway&limit=100" \
  | jq '.events | length // length // 0')"
[ "$AFTER" -ge "$((BEFORE + 2))" ] || echo "FAIL: missing audit events"
```

## Step 6 — Sub-suite D: Trust escalation

`upper` is configured `requiredTrust: medium`. Try it with a session
`consentedTrust: low` and expect deny:

```bash
kubectl patch mcpagentsession local-session -n mcp-servers --type=merge \
  -p '{"spec":{"consentedTrust":"low"}}'
sleep 8
init
RESP="$(call '{"name":"upper","arguments":{"message":"hi"}}')"
echo "$RESP" | grep -qiE 'trust|denied|forbidden' \
  || echo "FAIL: low-trust session called medium-trust tool"
kubectl patch mcpagentsession local-session -n mcp-servers --type=merge \
  -p '{"spec":{"consentedTrust":"high"}}'
sleep 8
```

## Step 7 — Sub-suite E: UI security headers

The UI middleware in `services/ui/main.go` sets baseline security headers
on every response, HSTS only on forwarded HTTPS, and Cache-Control on `/api`.

```bash
# Dashboard (HTTP path) — must have nosniff + frame-ancestors none, no HSTS.
curl -sSI http://localhost:18080/ | tr -d '\r' > /tmp/h-root.txt
grep -q '^X-Content-Type-Options: nosniff$' /tmp/h-root.txt    || echo "FAIL: nosniff"
grep -qi "Content-Security-Policy:.*frame-ancestors 'none'" /tmp/h-root.txt \
  || echo "FAIL: CSP frame-ancestors"
grep -i "script-src.*unsafe-inline" /tmp/h-root.txt && echo "FAIL: CSP allows unsafe-inline"
grep -i '^Strict-Transport-Security' /tmp/h-root.txt && echo "FAIL: HSTS on plain HTTP"

# Simulate TLS terminator → HSTS must appear.
curl -sSI -H "X-Forwarded-Proto: https" http://localhost:18080/ \
  | tr -d '\r' | grep -qi '^Strict-Transport-Security: max-age=' \
  || echo "FAIL: HSTS missing on forwarded HTTPS"

# /api/v1 responses must be uncacheable.
curl -sSI -H "x-api-key: $ADMIN_KEY" http://localhost:18080/api/v1/dashboard/summary \
  | tr -d '\r' | grep -qi '^Cache-Control:.*no-store' \
  || echo "FAIL: /api/v1 Cache-Control"
```

## Step 8 — Sub-suite F: Login + lockout

`PLATFORM_DEV_LOGIN` seeds `test@mcpruntime.org`/`test@123` and
`admin@mcpruntime.org`/`admin@123`. Verify success and lockout. The login
route is `/auth/login` (`services/ui/main.go` → `mux.HandleFunc("/auth/login",
...)`), not `/login` — read the handler before asserting shape; do not invent
fields. A successful login sets the `mcp_ui_session` cookie
(`HttpOnly; SameSite=Strict`).

```bash
# Correct creds → 200 + session cookie.
curl -sS -i -c /tmp/c.txt -H "content-type: application/json" \
  -d '{"email":"test@mcpruntime.org","password":"test@123"}' \
  http://localhost:18080/auth/login | head -1
grep -q 'mcp_ui_session' /tmp/c.txt || echo "FAIL: session cookie not set"

# Wrong password 6× → lockout (handler increments per-IP failure counter).
for i in 1 2 3 4 5 6; do
  curl -sS -o /dev/null -w "$i=%{http_code}\n" -H "content-type: application/json" \
    -d '{"email":"test@mcpruntime.org","password":"WRONG"}' \
    http://localhost:18080/auth/login
done
# At least one of those should be a lockout/429 response; never a 5xx.
```

## Step 9 — Sub-suite G: UI→API proxy

The browser session (cookie) and direct API-key clients are two **separate**
trust paths that must never merge. The browser never receives a bearer token
or API key: it only holds the `HttpOnly` session cookie, and the UI service
(`services/ui/session_proxy.go`) translates that cookie to the stored
upstream credential server-side, only for the explicit GET allowlist at
`/api/ui/v1/*` (`sessionProxyRuntimePrefixes` / `sessionProxyAnalyticsPrefixes`
in `session_proxy.go`). The cookie has **no** access to the direct
`/api/v1/*` backend routes — those require an `x-api-key`/bearer header, which
only a non-browser client (CLI, curl) presents.

```bash
# Confirm the runtime-config script does NOT include an API key.
# It is JavaScript (window.MCP_API_BASE = ...), not JSON — do not pipe to jq.
curl -sS http://localhost:18080/config.js | grep -i apiKey \
  && echo "FAIL: api key leaked via /config.js" || echo "OK: no key in /config.js"

# Authenticated browser session reaches ONLY the session-backed proxy...
curl -sS -b /tmp/c.txt http://localhost:18080/api/ui/v1/dashboard/summary \
  | jq -e '.servers // .summary // 0' >/dev/null \
  || echo "FAIL: authed browser cannot reach the session proxy"

# ...and must NOT be treated as authenticated on the direct backend path —
# a cookie alone carries no x-api-key/bearer, so this should be 401.
curl -sS -o /dev/null -w "cookie_on_direct_api=%{http_code}\n" \
  -b /tmp/c.txt http://localhost:18080/api/v1/dashboard/summary   # want 401

# A GET path NOT on the session-proxy allowlist must 404/401 through the
# proxy even for an authenticated cookie (fails closed on unlisted paths).
curl -sS -o /dev/null -w "unlisted_proxy_path=%{http_code}\n" \
  -b /tmp/c.txt http://localhost:18080/api/ui/v1/runtime/unlisted-path

# Direct API-key client should also work, on the direct path.
curl -sS -H "x-api-key: $ADMIN_KEY" http://localhost:18080/api/v1/dashboard/summary \
  | jq -e '.' >/dev/null || echo "FAIL: direct key client"
```

## Step 10 — Sub-suite H: Ingress + PII redactor (only when those changed)

```bash
kubectl get ingress -A
kubectl logs -n traefik deploy/traefik --tail=120 \
  | grep -iE 'middleware.*does not exist|panic|error' || echo OK
# PII redactor on the documented dev overlay should redact known patterns in
# request/response bodies. Probe with a synthetic SSN/email payload to a tool
# that echoes; verify the audit body in /api/v1/events does not contain the raw value.
```

## Step 11 — Sub-suite I: Live-log secret scan (always)

Regressions where tokens leak into logs are common after refactors.

```bash
for ns in mcp-runtime mcp-sentinel mcp-servers traefik registry; do
  for d in $(kubectl get pods -n "$ns" -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}'); do
    kubectl logs -n "$ns" "$d" --all-containers --since=10m 2>/dev/null \
      | grep -aE 'Bearer [A-Za-z0-9._-]{20,}|sk-[A-Za-z0-9]{16,}|eyJ[A-Za-z0-9._-]{20,}|x-api-key:\s*[A-Za-z0-9_-]{12,}'
  done
done
# Any hit is a High severity finding. Do NOT copy the matched value into the
# report — describe the pod + line number range only.
```

## Step 12 — Report

Use the rubric and template in `../_shared/FINDINGS-TEMPLATE.md` exactly. Each
finding should include:

- The **trust boundary** it crosses (anon→user, user→admin, agent A→agent B,
  low→high trust, pod→secret).
- The **command + response code or body fragment** that demonstrates the
  failure (with secrets redacted).
- A **regression test** suggestion that lands in the relevant split API service package (for example `services/platform-api/` or `services/runtime-api/`), `services/ui/main_test.go`, `services/mcp-gateway/...`, or `pkg/access/...`.

Cross-link to `change-security-audit` / `platform-security-audit` for any static
counterpart, to `kubernetes-hardening-audit` for cluster-policy gaps, and to
`supply-chain-audit` for any dependency-CVE angle uncovered.
