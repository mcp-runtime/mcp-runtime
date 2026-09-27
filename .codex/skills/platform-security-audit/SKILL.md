---
name: platform-security-audit
description: Run a deep platform-wide security audit of MCP Runtime — every component, every trust boundary, against an explicit threat model. Use when Codex is asked for a thorough security review (not a single PR), pre-release sign-off, red-team-style assessment, or to produce a structured report of findings across operator, gateway, sentinel services, registry, ingress, governance, and CI. For change-scoped reviews use change-security-audit; for SBOM/image/dependency work use supply-chain-audit; for cluster RBAC/PSS/NetworkPolicy hygiene use kubernetes-hardening-audit.
---

# Platform Security Audit

## Overview

For broad audits that assess proposed system or trust-boundary design choices,
read [the shared design principles](../_shared/design-principles.md). Skip it
when assessing only the deployed posture against the threat model.

Use this skill when the goal is a deep, repository-wide security assessment of
MCP Runtime, not a PR review. The output is a structured report with a threat
model, a per-endpoint authn/authz matrix, tenant-isolation probes, protocol
fuzz results, audit-log integrity tests, TLS hygiene, and DAST against a live
cluster. Findings use the shared template at
`../_shared/FINDINGS-TEMPLATE.md`.

This skill is intentionally heavy. Expect hours, not minutes. Skip nothing
silently — every check that did not run becomes a recorded gap.

Run live probes against the isolated contributor Kind cluster or a disposable
staging target by default. Verify the target kubeconfig/context before any
probe that changes state. Never run DoS, oversized-body, high-concurrency, or
resource-exhaustion probes against production unless the user explicitly
authorizes that exact target and test window; prefer a production read-only
review and report the dynamic checks as out of scope.

## Step 1 — Build the threat model before running tools

Produce a STRIDE table per component. Components to cover:

- **operator** (`cmd/operator/`, `internal/operator/`): reconciles `MCPServer`,
  `MCPAccessGrant`, `MCPAgentSession`; injects gateway sidecar.
- **mcp-gateway** (`services/mcp-gateway/`): in-pod sidecar enforcing
  rendered policy, emitting audit events.
- **platform-api** (`services/platform-api/`): identity, admin, registry forward-auth.
- **runtime-api** (`services/runtime-api/`): runtime governance, deployments, registry push.
- **analytics-api** (`services/analytics-api/`): ClickHouse events and usage analytics.
- **sentinel-ui** (`services/ui/`): browser UI, login, dashboards.
- **sentinel-ingest** (`services/ingest/`): high-volume event intake.
- **sentinel-processor** (`services/processor/`): event processing, ClickHouse
  writes.
- **registry** (`k8s/`, `config/`): Distribution v2 registry (HTTP dev or
  HTTPS prod).
- **traefik plugins** (`traefik-plugins/`): PII redactor, dev-only
  middleware.
- **CRD types** (`api/v1alpha1/`): trust source for resource shapes.
- **CI** (`.github/workflows/`): build, sign, test, release pathways.

For each component fill: Spoofing, Tampering, Repudiation, Information
disclosure, Denial of service, Elevation of privilege. State preconditions,
attacker, and impact.

Attacker profiles to enumerate:

- **anon-public**: someone reaching `platform.<domain>` from the internet.
- **anon-cluster**: a pod inside the cluster with no creds.
- **authenticated-user**: holds `UI_API_KEY` or a logged-in session.
- **admin-user**: holds an `ADMIN_API_KEYS` value.
- **ingest-only**: holds an `INGEST_API_KEYS` value.
- **rogue-mcp-image**: a malicious image pulled from the registry.
- **rogue-tenant-agent**: a session for tenant A trying to reach tenant B.
- **mitm-ingress / mitm-registry**: someone between client and ingress.
- **compromised-CI-token**: a leaked GH Actions secret.

Out of scope must be stated explicitly (e.g., "physical access to nodes",
"k8s control plane CVEs not patched by user").

## Step 2 — Authentication and authorization matrix

For every route in `services/platform-api/routes.go`, `services/runtime-api/routes.go`, and `services/analytics-api/routes.go` produce a row:

| Path | Method | Anon | UI cookie | UI API key | Admin API key | Ingest API key | Notes |

Compare to `docs/security/authz-matrix.md` (the checked-in source of truth).
Flag any route in code that is not in the matrix, and any matrix row whose
expected status diverges from the live response.

Verification harness (run against a test cluster with known keys):

```bash
BASE=http://localhost:18080
while IFS= read -r row; do
  path=$(echo "$row"   | jq -r .path)
  method=$(echo "$row" | jq -r .method)
  role=$(echo "$row"   | jq -r .role)
  want=$(echo "$row"   | jq -r .expect)
  headers=()
  case "$role" in
    anon) ;;
    ui) headers=(-H "x-api-key: $UI_API_KEY") ;;
    admin) headers=(-H "x-api-key: $ADMIN_API_KEY") ;;
    ingest) headers=(-H "x-api-key: $INGEST_API_KEY") ;;
    *) echo "UNKNOWN ROLE $role"; continue ;;
  esac
  got="$(curl -sS -o /dev/null -w "%{http_code}\n" -X "$method" "${headers[@]}" "$BASE$path")"
  test "$got" = "$want" || echo "MISMATCH $method $path role=$role got=$got want=$want"
done < <(jq -c '.[]' docs/security/authz-matrix.json)
```

The starter matrix is `docs/security/authz-matrix.json` (subset of rows). Expand it
toward full parity with `docs/security/authz-matrix.md` when auditing finds gaps.

Any 200/204 response on a path-role combo where the matrix expects 401/403 is
**Critical** until proven otherwise.

The harness above only exercises `x-api-key`-header roles. It does not cover
the cookie-authenticated `ui-cookie` role: the browser dashboard's
`GET /api/ui/v1/*` session proxy (`services/ui/session_proxy.go`) translates
the HttpOnly `mcp_ui_session` cookie server-side and is not represented in
`docs/security/authz-matrix.json`/`.md` at all as of this audit. Test it with
a cookie jar instead of a header:

```bash
JAR=$(mktemp)
curl -sS -c "$JAR" -X POST "$BASE/auth/login" -H 'content-type: application/json' \
  -d '{"email":"<test-user>","password":"<test-password>"}' -o /dev/null
curl -sS -b "$JAR" -o /dev/null -w "%{http_code}\n" "$BASE/api/ui/v1/runtime/servers"
rm -f "$JAR"
```

Add matrix rows for every allowlisted proxy path (`/api/ui/v1/dashboard/summary`,
`/runtime/{namespaces,servers,tools,server-events,teams,agents,grants,sessions,
components,policy,observability/*}`, `/user/api-keys`,
`/admin/{operations,deployments}`, `/events`, `/analytics/usage`,
`/user/analytics/usage` — see `sessionProxyRuntimePrefixes` /
`sessionProxyAnalyticsPrefixes` in `session_proxy.go` for the authoritative
list) and confirm a missing/expired/invalid session returns 401 on each.
A cookie-authenticated write allowlist exists in `session_proxy.go`
(`sessionProxyWriteRoutes`). Its current routes include `/user/api-keys`
(POST, DELETE item), `/runtime/grants` (POST, PATCH, DELETE item, POST
`revoke-sessions`), `/runtime/sessions` (POST, PATCH, DELETE item), team
membership and agent lifecycle routes, `/runtime/agents/{id}` (PATCH),
`/runtime/servers/{namespace}/{name}` (DELETE), and
`/runtime/actions/restart` (POST). Re-read `sessionProxyWriteRoutes` as the
source of truth before each audit. Allowed writes pass CSRF verification
(`verifyCSRF`) before reaching the upstream; methods outside the allowlist
return 405 before session or CSRF validation. Audit both paths:

- Non-GET methods **not** on the allowlist still return 405 (`sessionProxyWriteAllowed`
  rejects them before the CSRF/session check even runs).
- Non-GET methods **on** the allowlist reject a missing or invalid
  `X-CSRF-Token` with 403 (`{"error":"csrf_failed"}`), and only succeed with
  a valid session-bound token obtained from `/auth/login`/`/auth/status`.

```bash
# Missing CSRF token on an allowlisted write route must 403, not succeed.
curl -sS -b "$JAR" -o /dev/null -w "%{http_code}\n" -X POST \
  "$BASE/api/ui/v1/runtime/sessions" -H 'content-type: application/json' -d '{}'
```

## Step 3 — Tenant and grant isolation probes

Build adversarial cases against governance. Pre-create:

- subjects `user-A`, `user-B`; agents `agent-A`, `agent-B`.
- servers `srv-one` in namespace `tenant-a`, `srv-two` in namespace `tenant-b`.
- grants and sessions for each tenant.

Create agents through the team directory and use their actual active IDs
(`agt_<26-character lowercase ULID>`); placeholder IDs may be rejected before
the cross-team authorization check is reached.

Probes (each is a finding when it succeeds):

- Replay `Mcp-Session-Id` from tenant A against `srv-two` URL.
- POST `MCPAccessGrant` whose `serverRef` targets a server in another
  namespace; confirm rejection. Read the current handler and API contract
  before describing whether validation is transactional.
- Disable a grant mid-call: hold an in-flight `tools/call` open, toggle
  `PATCH /api/v1/runtime/grants/{ns}/{name}` with `{"disabled":true}` (the
  legacy POST `/disable` route also exists), and assert the next request is
  denied within the proxy poll window.
- Toggle `revoke` on a session and immediately reuse the session ID.
- Apply `MCPServer` whose `ingressHost` collides with another tenant's host.
- Apply a session whose `consentedTrust` exceeds the grant `maxTrust`.

## Step 4 — Protocol fuzzing of the MCP request path

Fuzz the proxy/gateway request handling. Targets:

- `initialize` with malformed JSON, missing `Mcp-Protocol-Version`,
  unsupported version, oversized params.
- `notifications/initialized` before any `initialize`.
- `tools/call` for unknown tool, with a valid tool but malformed arguments,
  with deeply nested JSON (≥1000 levels), with extremely large argument
  bodies (≥10MB).
- Replayed `Mcp-Session-Id` across servers, expired session IDs,
  session-ID prefix attacks (`../`, NUL bytes).
- Race: parallel `notifications/initialized` for the same session;
  parallel `tools/call` against the same session ID from N goroutines.

If a Go fuzz target exists in `services/mcp-gateway/`:
`KUBECONFIG="$(mktemp)" go test -run='^$' -fuzz='Fuzz<Name>' -fuzztime=300s ./...`

If no fuzz target exists at this trust boundary, that absence is itself a
finding (Medium).

## Step 5 — DoS and resource exhaustion

Run this section only on a disposable Kind/staging target. Do not point these
probes at a production hostname or cluster without explicit authorization.

- **Slow read**: open many connections to `mcp.<domain>/<name>/mcp`, send one
  byte per second; assert connection limits and read-timeout enforcement.
- **Large body**: `dd if=/dev/urandom bs=1M count=200 | curl -X POST --data-binary @- ...`;
  assert max-body limits.
- **Header amplification**: 1000 custom headers; assert max-header limits.
- **Session map growth**: open 100k sessions without `tools/call`; watch
  proxy memory and confirm idle eviction.
- **Audit storm**: drive sustained allow+deny traffic; assert ingest backlog
  bound and that the proxy fails closed when ingest is unreachable rather
  than dropping audit events silently.
- **Ingest endpoint**: replay valid keys at high QPS to confirm rate-limits
  and backpressure exist; absence is a Medium finding.

## Step 6 — Audit log integrity

Audit events must be tamper-evident at the boundary the operator controls:

- Confirm every `tools/call` (allow and deny) emits exactly one event.
- Confirm denied calls do not include the unredacted argument payload.
- Confirm credentials, headers, cookies, and JWTs do not appear in audit
  fields.
- Confirm the event includes subject, agent, server, tool, decision, trust,
  timestamp, and a stable correlation ID.
- Test an ingest outage: events must buffer or fail closed, not be dropped.
- Test event ordering under parallel calls; out-of-order without a sequence
  ID is a Medium finding.
- Confirm processor writes to ClickHouse use parameterized queries (grep
  for string concatenation in `services/processor/`).

## Step 7 — TLS hygiene and ingress posture

Run against the live ingress:

- `testssl.sh https://platform.<domain>`,
  `testssl.sh https://mcp.<domain>`,
  `testssl.sh https://registry.<domain>`.
  Expect TLS 1.2+, no weak ciphers, no TLS 1.0/1.1, OCSP-stapling on prod
  certs, cert lifetime ≤90d for ACME-issued.
- HTTP→HTTPS redirect on `platform.<domain>`. Confirm via
  `curl -I http://platform.<domain>` returns 308.
- Security headers on every UI/API response: `Strict-Transport-Security`,
  `Content-Security-Policy`, `X-Content-Type-Options: nosniff`,
  `X-Frame-Options: DENY`, `Referrer-Policy: strict-origin-when-cross-origin`,
  `Permissions-Policy` for sensors. Each missing header is at least Medium.
- Confirm Grafana and Prometheus are NOT reachable on the public platform
  host (CLAUDE.md invariant). Reachability is a High finding.
- Confirm the registry ingress in production does not reference the dev-only
  `pii-redactor@file` middleware.

## Step 8 — Log and response leak audit

Static and dynamic checks for credential leakage:

```sh
grep -RIn -E '(api[_-]?key|password|secret|token|bearer|cookie|x-api-key)' \
     --include='*.go' services/ internal/ pkg/ cmd/
```

Triage every match: is the value being logged, returned, or only read into a
struct? For the dynamic side, run sustained traffic and grep
`kubectl logs deploy/<name> -n mcp-sentinel` for the actual secret values from
`mcp-sentinel-secrets` — anything found is at least High.

Check error responses for stack traces, internal paths, or environment
variables — any of those is Medium.

## Step 9 — Deserialization and injection surfaces

- Confirm YAML loaders use `sigs.k8s.io/yaml`, not bare
  `gopkg.in/yaml.v2 Unmarshal` against untrusted input. `grep -RIn 'yaml.v2'`
  and triage.
- JSON unmarshalling: confirm all HTTP handlers cap body size before
  `json.NewDecoder(r.Body).Decode(...)`. Missing limit is Medium.
- SQL/ClickHouse: `grep -RIn 'fmt.Sprintf' services/processor/` for any
  format-string into a query. Any hit is High until proven safe.
- Postgres in `mcp-sentinel`: same search across `services/platform-api/` for query
  building.
- Shell exec: `grep -RIn 'exec.Command' --include='*.go'`. Any non-constant
  first arg is High.

## Step 10 — SSRF and path traversal

- Image refs: `pkg/metadata/host_resolve.go` and registry parsing must reject
  `127.0.0.1`, `169.254.169.254`, `[::1]`, IPv6 zone IDs, schemes other than
  `http`/`https`, and registry hosts that resolve to private CIDRs in prod.
- Ingress hosts: confirm `MCP_PLATFORM_DOMAIN` parsing rejects values that
  contain a path or query (e.g., `example.com/../`).
- Manifest paths: `--ingress-manifest` and any other CLI path flag must not
  follow symlinks out of the repo root.
- Registry credential storage in `services/platform-api/registry/credentials.go`: any
  file path comes from request input must be normalized via `filepath.Clean`
  and rejected if it escapes a known base.

## Step 11 — DAST against a live cluster

When a test cluster is available:

- `nuclei -u https://platform.<domain>` with default templates.
- ZAP baseline: `docker run --rm -t ghcr.io/zaproxy/zaproxy:stable zap-baseline.py -t https://platform.<domain>`.
- `kube-hunter --remote <ingress-ip>`.

Treat all High and Critical findings as in-scope; deduplicate against earlier
manual findings.

## Step 12 — Run static and supply-chain scanners (full)

- `gitleaks detect --source . --config .gitleaks.toml --redact`.
- `gosec ./...` (pinned `v2.26.1` per CI).
- `govulncheck ./...` from repo root and from each `services/<name>/`.
- `trivy fs --scanners vuln,secret,license,misconfig --severity CRITICAL,HIGH --ignore-unfixed --skip-dirs inspirations/mcp-gateway-registry .`.
- `semgrep --config=auto` (and any project rules in `.semgrep/` if added).
- For container images and SBOM/cosign work, hand off to
  `supply-chain-audit`.
- For RBAC, PSS, NetworkPolicy, and manifest hygiene, hand off to
  `kubernetes-hardening-audit`.

## Step 13 — Report

Use `../_shared/FINDINGS-TEMPLATE.md`. Required sections in the
order specified there: Summary, Threat model assumptions, Findings, Scanner
output, Checks run, Checks skipped, Remediation plan.

State the commit SHA, cluster context (Kind / k3s / prod-like), date, and the
auditor identity (human or agent name). Tally findings by severity at the
top.
