---
name: dashboard-browser-qa
description: Browser-first real-cluster Sentinel UI/dashboard QA - role-based navigation, auth flows, every tab, forms, filters, destructive actions, rendered data, network/API evidence, console evidence, responsive/accessibility checks, cleanup, static assets, and public-host defenses. Use when Codex is asked to QA UI changes, dashboard regressions, login/admin/tenant flows, browser-visible API behavior, copyable MCP connect config, or backend analytics/observability changes that must be validated through UI controls. Complements cluster-ops with feature-correctness and browser interaction checks. Assumes contributor-cluster has run.
---

# Dashboard Browser QA

## Overview

This skill validates that the **Sentinel dashboard actually works as a user
experience** against the real UI + API + Traefik stack. Curl checks are useful
smoke gates, but they are not a substitute for browser interaction. Use
Playwright and Chrome DevTools MCP whenever available to collect:

- accessibility snapshots for actionable element names and roles
- browser console messages
- network request/response evidence for UI-triggered API calls
- screenshots for visual regressions and responsive layout failures
- keyboard interaction evidence for reachable controls

`services/ui/main_test.go` covers handler-level behavior. This skill covers
browser-visible workflows: role-gated navigation, session transitions, forms,
filters, tables, modals, copy controls, destructive actions, public-host
defenses, and whether rendered UI data matches backend truth.

Header / CSP / lockout / secret-leak checks live in `cluster-ops`; do not
duplicate them here unless the symptom is visible in the UI.

Regression evidence contract: a UI pass needs browser evidence, not only curl
or unit tests. For changed auth, role, or API-key behavior, cover both a
user-identity session and a no-user-identity or denied session so role-gating
regressions surface before merge. If browser automation is unavailable, report
the UI result **blocked**.

Phase-gate rule: any change under `services/ui/`, `services/ui/frontend/`,
`services/ui/static/`, UI ingress manifests, or a backend path rendered by the
dashboard requires this skill. Run the same browser workflow once against the
pre-change deployment and once against the candidate deployment. A phase is
not passed by unit tests alone.

## Step 1 - Confirm preconditions

Do not reinstall the platform or run codegen as part of UI QA. The live
cluster is the source of truth.

```bash
TEST_KUBECONFIG="${TEST_KUBECONFIG:-$HOME/.kube/test-mcp-runtime-config}"
kubectl --kubeconfig "$TEST_KUBECONFIG" config current-context \
  | grep -qx test-mcp-runtime \
  || { echo "Run contributor-cluster first"; exit 1; }
export KUBECONFIG="$TEST_KUBECONFIG"

curl -fsS -o /dev/null http://localhost:18080/ \
  || { echo "Traefik port-forward not running; run: kubectl port-forward -n traefik svc/traefik 18080:8000"; exit 1; }

./bin/mcp-runtime status
```

Use a unique scratch directory for temporary artifacts:

```bash
QA_TMP="$(mktemp -d)"
trap 'rm -rf "$QA_TMP"' EXIT
```

If the live cluster has unrelated user changes, work with them. Do not retire
or mutate non-temporary objects unless the user explicitly approves.

If you rebuild the API or UI image before a browser pass, build for the Kind
node architecture, not your memory of the last machine:

```bash
NODE_ARCH="$(docker version --format '{{.Server.Arch}}')"
IMAGE="registry.registry.svc.cluster.local:5000/<repo>:ui-qa-$(date +%s)"
docker build --platform="linux/${NODE_ARCH}" -t "$IMAGE" -f "$DOCKERFILE" .
docker image inspect "$IMAGE" --format '{{.Os}}/{{.Architecture}}'
kind load docker-image "$IMAGE" --name mcp-runtime
kubectl set image -n mcp-sentinel deploy/"$DEPLOYMENT" "$CONTAINER=$IMAGE"
kubectl rollout status -n mcp-sentinel deploy/"$DEPLOYMENT" --timeout=180s
```

`kind load docker-image` can load the wrong architecture into containerd. A UI
QA pass is invalid if the pod is still running an old image or an image whose
architecture does not match the Kind node.

## Step 2 - Choose audit mode

- **full-ui**: every visible capability and every role. Default for broad UI
  QA requests.
- **git-range** (`BASE=<merge-base>`, default `origin/main`): trim by diff,
  but still run Dashboard Load and at least one login/logout flow.
- **smoke**: only dashboard load, one successful login, one protected action,
  and console/network sanity. Use only when the user asks for a quick check.

Diff guidance:

| Diff touches | Required sub-suites |
|---|---|
| `services/ui/main.go` | Dashboard Load, Auth, UI->API Proxy, affected tabs |
| `services/ui/static/**` | Browser Matrix, Static Assets, Responsive/A11y |
| `services/runtime-api/internal/runtimeapi/**` | Catalog, Governance, Operations, API Contract |
| `services/runtime-api/internal/runtimeapi/*observability*`, `services/mcp-gateway/**`, `k8s/*prometheus*` | My Activity scoped observability, Prometheus and Grafana actions, scoped query evidence, tenant negative checks |
| `services/platform-api/internal/platformstore/**` or auth code | Auth, API Keys, Role Gating |
| `config/ingress/**`, `k8s/**` UI ingress | Public-host Defense |
| `docs/**`, `website/**` only | Dashboard Load smoke, then report docs-only scope |

Before selecting checks, map every touched UI-visible change to a browser
workflow. If the diff affects data rendered by a tab but not `services/ui/**`
directly, still validate the owning tab and controls through the browser. Do
not report a backend-only pass for a change whose success or failure is visible
in the dashboard.

### Before/after phase gate

For every migration or UI PR, keep the comparison deterministic:

1. **Before:** start a fresh browser context, run the affected signed-out and
   signed-in workflows at the required desktop and mobile viewports, and save
   the snapshot, network list, console list, and screenshot evidence.
2. **Deploy verification:** build/load the candidate image for the Kind node
   architecture, verify the pod image and environment, and wait for rollout
   readiness. Do not call browser results “after” until the page is served by
   that pod.
3. **After:** repeat the exact actions, roles, viewport sizes, and assertions.
   Compare visible data, URL/API paths, status codes, console errors, and
   responsive bounds. Expected negative tests (for example signed-out `401`)
   must be labeled as such; every other failed request is a finding.
4. **Phase decision:** record `PASS`, `FAIL`, or `BLOCKED` for each applicable
   migration phase. `PASS` requires all phase acceptance criteria and browser
   evidence. `BLOCKED` is for missing fixtures, unavailable browser automation,
   or an unavailable live dependency—not for a discovered defect.

Use a stable evidence record for each phase:

```text
phase=<migration phase>
before_commit=<SHA or deployed image>
after_commit=<SHA or deployed image>
role=<signed-out|tenant-user|admin>
viewport=<width>x<height>
workflow=<named browser workflow>
visible_assertions=<snapshot text or screenshot path>
network_delta=<new/removed/changed routes and statuses>
console_delta=<new errors/warnings, or none>
decision=<PASS|FAIL|BLOCKED>
```

When the migration intentionally leaves a legacy fallback, test both the new
surface and the fallback. A fallback that is merely reachable but emits
unexplained browser `401`/`404` errors is a failed phase, not a passing
compatibility check.

Script it once, parameterized by surface, not copy-pasted per pass — this is
what made a same-day three-PR migration comparison tractable in practice:

```js
// node evidence.mjs <out-dir> <surface: legacy|react>
// Drives the same signed-out -> login -> workflow -> mobile -> logout flow
// against either surface. Collects page.on('console'), page.on('pageerror'),
// page.on('response') filtered to /api/ and /auth/, a screenshot per step,
// and document.documentElement.scrollWidth vs clientWidth at 390px.
```

Run it unmodified with `out-dir=before` against the pre-change deployment and
`out-dir=after` against the candidate. Diff the two evidence sets by hand —
server/tool counts, request paths and statuses, console errors, 390px
overflow — and paste the delta as a literal before/after table in the PR
description. Do not narrate results from memory.

## Step 3 - Browser instrumentation is required

Prefer MCP browser tools in Codex sessions:

1. Use Playwright MCP for navigation, snapshots, clicks, form fill, screenshots,
   viewport resizing, and network request lists.
2. Use Chrome DevTools MCP for a second console/network pass when available,
   especially when investigating 401/403, redirects, cookie/session behavior,
   or copy/clipboard failures.
3. Treat browser console errors and failed network requests as findings unless
   they are explained by an intentional negative test.

If no MCP browser server is wired in, do not jump straight to **blocked** —
drive Playwright directly from Node first (see
`references/frontend-tooling.md` for the runner setup and the
cached-browser-revision gotcha). Mark browser checks **blocked** only when
no browser can be launched at all, and then run only curl/API smoke checks. Do
not report a full UI pass without browser evidence.

Capture the following evidence per role:

```text
role=<signed-out|tenant-user|admin>
selected_tab=<tab>
console_errors=<count and messages>
failed_network=<method url status/error>
visible_assertions=<snapshot text or screenshot path>
```

## Step 4 - Required UI feature matrix

The final report must include a feature matrix. For full UI audits, or for
`git-range` audits touching a listed page, read
`references/ui-coverage.md` and use its exact matrix shape and page checklist.
Do not load that reference for a narrow smoke check unless a finding needs
detailed page coverage.

Minimum page categories for the full matrix:

- Global App Shell
- Authentication
- Server Catalog
- My Activity
- API Keys
- Admin Dashboard
- Governance
- Operations
- Platform
- Responsive / Accessibility
- Public-host Defense, when applicable

## Step 5 - Run role and page coverage

Always use browser evidence for UI conclusions. In `full-ui`, work through the
page checklist in `references/ui-coverage.md`. In `git-range`, load only the
sections that match the diff plus Role And Session Flows. In `smoke`, cover:

- dashboard load
- signed-out protected-action behavior
- one successful tenant-user or admin login
- one UI-triggered API call with network evidence
- one responsive viewport
- console and failed-network sanity

Create or mutate only temporary `qa-audit-*` objects for destructive actions.
Record every skipped page/control with the reason and the fixture or cluster
mode needed to cover it later.

For changed UI-visible behavior, include at least one positive and one negative
browser/API assertion for the exact changed control. Examples:

- Metrics/observability: tenant-owned `qa-audit-*` server shows per-server
  `Prometheus` and `Grafana` actions by default. The clicked URL or fetched
  link is scoped to its namespace/server, and a shared or foreign namespace
  returns 403/404 for the same tenant session.
- Tenant observability buttons: tenant users should not see the raw header
  `Prometheus` or `Grafana` links; those are admin-only cluster-wide surfaces.
  Tenant users should see per-server `Prometheus` and `Grafana` actions in My
  Activity when scoped observability is available.
- Analytics: changing the server selector triggers a network request with the
  selected namespace/server filters and excludes shared catalog servers from
  personal totals.
- Role-gated actions: the allowed role sees and can run the action; signed-out
  or disallowed roles cannot see it or receive an intentional auth error.

## Step 6 - Curl smoke and API contract evidence

Use curl to support browser findings, not replace them.

Dashboard load — the root `/` is the React shell (`services/ui/static/`, a
Vite build with a content-hashed bundle filename; never hardcode it, read it
from `index.html`). Unmigrated workspaces render through the legacy fallback
at `/legacy/index.html` (`services/ui/static/legacy/`, a plain unhashed
`app.js`). Check both while any workspace still depends on the fallback:

```bash
curl -sS -o "$QA_TMP/index.html" -w "%{http_code} %{size_download}\n" \
  http://localhost:18080/
grep -q '<div id="root">' "$QA_TMP/index.html" || echo "FAIL: React root mount missing"
grep -qE '<script type="module"[^>]*src="/assets/index-[^"]+\.js"' "$QA_TMP/index.html" \
  || echo "FAIL: React bundle script missing"

BUNDLE_JS="$(grep -oE '/assets/index-[^"]+\.js' "$QA_TMP/index.html" | head -1)"
curl -sSI "http://localhost:18080${BUNDLE_JS}" | tr -d '\r' \
  | grep -qiE '^Content-Type: (text|application)/javascript' \
  || echo "FAIL: bundle content-type"

curl -sS -o "$QA_TMP/legacy.html" -w "%{http_code}\n" http://localhost:18080/legacy/index.html
grep -q 'Dashboard sections' "$QA_TMP/legacy.html" || echo "FAIL: legacy dashboard shell missing"
grep -q 'app.js' "$QA_TMP/legacy.html" || echo "FAIL: legacy bundle script missing"

curl -sS http://localhost:18080/config.js | grep -q 'window.MCP_API_BASE' \
  || echo "FAIL: /config.js missing MCP_API_BASE"
curl -sS http://localhost:18080/config.js | grep -qi 'api.?key' \
  && echo "FAIL: /config.js exposes an apiKey-shaped field"
```

Static assets — the React bundle is minified build output gated by
`npm run build`/`tsc -b` (`references/frontend-tooling.md`), not something to
syntax-check directly. `services/ui/static/legacy/app.js` is the only
remaining hand-authored, unminified static script:

```bash
node --check services/ui/static/legacy/app.js
curl -sS -o "$QA_TMP/legacy-app.js" http://localhost:18080/legacy/app.js
node --check "$QA_TMP/legacy-app.js"
```

Discover API routes from implementation and browser network traffic. Do not
invent endpoints. If an endpoint path differs from the skill examples, record
the live route in the report.

## Step 7 - Automated regression checks

For broad UI audits or UI/API/CLI changes, run the automated checks that match
the touched surface. Do not run codegen, formatters, setup reinstall, or any
command that rewrites tracked files during the audit.

```bash
(
TEST_ISOLATION_KUBECONFIG="$(mktemp)"
trap 'rm -f "$TEST_ISOLATION_KUBECONFIG"' EXIT
export KUBECONFIG="$TEST_ISOLATION_KUBECONFIG"
(cd services/ui && go test ./... -race -count=1)
(cd services/ui/frontend && npm test && npm run build)
node --check services/ui/static/legacy/app.js
go test ./internal/cli/... ./cmd/mcp-runtime/... -count=1
go test ./test/golden/... -count=1
)
KUBECONFIG="$TEST_KUBECONFIG" E2E_CACHE_MODE=1 \
  E2E_SCENARIOS=smoke-auth,governance \
  CLUSTER_NAME=mcp-runtime \
  E2E_KEEP_CLUSTER=1 \
  bash test/e2e/qa-e2e.sh
```

If a check is unsafe or too expensive for the requested scope, skip it with a
specific reason. For example, skip QA E2E when the live contributor cluster is
busy with unrelated user work or when the user requested a read-only audit.

## Step 7a - Know the console's routes and test ids

The React console is hash-routed and no longer embeds the legacy dashboard.
Read `references/ui-coverage.md`, "React console structure", before writing any
browser script: the routes, the confirmation dialogs, the Operations tabs, and
the Platform health grid all changed, and older selectors will silently fail.

## Step 7b - Frontend development and Playwright tooling

Read [references/frontend-tooling.md](references/frontend-tooling.md) before
frontend component-test/build work, or when driving Playwright directly from
Node because no Playwright MCP server is available. Skip it for narrow API-only
or docs-only checks.

## Step 8 - Public-host defense

In test mode `MCP_PLATFORM_DOMAIN` is usually unset and `/grafana` +
`/prometheus` route through the dev path-based gateway. The invariant under
test: **when** `MCP_PLATFORM_DOMAIN` is set, the public platform ingress must
not expose Grafana or Prometheus.

```bash
kubectl get ingress -n mcp-sentinel -o yaml \
  | yq '.items[] | select(.metadata.name=="mcp-sentinel-platform-ui") | .spec.rules' 2>/dev/null \
  || kubectl get ingress -n mcp-sentinel mcp-sentinel-platform-ui -o yaml \
       | grep -E 'path:|grafana|prometheus'
```

If `mcp-sentinel-platform-ui` does not exist, mark this sub-suite **N/A in
test mode** and recommend a `MCP_PLATFORM_DOMAIN=*` rerun.

## Step 9 - Safe mutation and cleanup

Only mutate temporary resources:

- `qa-audit-*` MCPServers
- `qa-audit-*` grants
- `qa-audit-*` sessions
- `qa-audit-*` API keys

Required cleanup evidence:

```bash
kubectl get mcpservers,mcpaccessgrants,mcpagentsessions -A | grep qa-audit || true
./bin/mcp-runtime server list
```

Also confirm:

- no active `qa-audit-*` API keys remain
- temporary credentials or cookie files were removed
- temporary port-forwards were stopped
- temporary e2e/example workloads created during the audit were removed

If another command recreates temporary workloads after cleanup, delete them
again and wait until pods/deployments/services/ingresses disappear before
finishing.

## Step 10 - Report

For UI QA, use this finding shape instead of the security template unless the
finding is actually security-sensitive:

```text
[SEV-{High|Medium|Low|Info}] <short title>
UI path: <tab / modal / workflow>
Affected API/CLI path: <endpoint or command, if any>
User impact: <what the user cannot do or might misunderstand>
Repro steps:
  1. <browser action>
  2. <browser action>
Expected:
Actual:
Network/API evidence:
Console evidence:
Likely implementation area: <services/ui/frontend/src/... | services/ui/static/legacy/app.js | services/ui/main.go | services/platform-api/... | services/runtime-api/...>
Recommended regression test:
Status: Open
```

Final report sections:

1. Summary: scope, commit SHA, cluster context, browser tools used, counts by
   status/severity.
2. UI feature matrix.
3. Severity-ranked findings.
4. CLI/Kubernetes cross-checks and mismatches.
5. Checks run and checks skipped.
6. Cleanup evidence.
7. Fix plan grouped by data correctness, missing UI coverage, role/auth
   behavior, form validation, destructive-action safety,
   responsive/accessibility, and automated regression tests.

Cross-link to `cluster-ops` for header/lockout findings, to
`cluster-ops` for rollout/ingress findings, and to `cluster-ops` when a
UI complaint is really a latency complaint.
