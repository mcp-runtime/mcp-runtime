# Sentinel UI Coverage Checklist

Use this reference for `full-ui` audits, for `git-range` audits where the diff
touches a listed page, or when a finding requires detailed UI reproduction.
For smoke checks, cover only dashboard load, one successful login, one protected
action, console/network sanity, and the curl smoke in `SKILL.md`.

The report matrix must use this shape:

```text
Page | Feature | Field/control | Action | Expected result | Actual result | Network/API evidence | Console evidence | Status
```

Use one row per meaningful capability. Forms, filters, enable/disable,
revoke/unrevoke, empty states, and error states are separate rows.

## Role And Session Flows

Run browser flows in order. Use test-mode credentials only in local Kind test
mode.

Signed out:

- Header shows product title and Sign In.
- Catalog is visible.
- Protected tabs/actions open auth modal and do not switch to protected data.
- Namespace selector behavior matches platform mode.
- No Google button appears when `MCP_GOOGLE_CLIENT_ID` is empty.

Tenant user (`test@mcpruntime.org` / `test@123`):

- Visible tabs: Server Catalog, My Activity, API Keys, Governance.
- Hidden tabs: Dashboard, Operations, Platform.
- Logout resets active tab and clears protected data.
- A 401 from any UI API call resets auth state or shows a clear unauthorized
  state without leaving mixed signed-in/signed-out controls.

Admin (`admin@mcpruntime.org` / `admin@123`):

- Visible tabs: Server Catalog, API Keys, Dashboard, Governance, Operations,
  Platform.
- Hidden tab fallback works after logout.
- Auto-refresh starts only for admin dashboard and stops on logout.
- Grafana and Prometheus header links appear only for admin in dev path mode.
- Normal tenant users never see the raw header Grafana/Prometheus links.

API-key login:

```bash
UI_KEY="$(kubectl get secret mcp-sentinel-secrets -n mcp-sentinel -o jsonpath='{.data.UI_API_KEY}' | base64 -d)"
test -n "$UI_KEY" || { echo "FAIL: UI_API_KEY is empty"; exit 1; }
```

- Valid UI API key logs in as admin and clears the key input after success.
- Invalid API key returns a visible error and does not clear the field until
  the user edits it or retries.
- Negative cases: blank form, email without password, password without email,
  invalid password, and backend unavailable/network failure when safely
  reproducible.

## Server Catalog

Drive from the browser, then cross-check with CLI/Kubernetes.

Namespace selector:

- signed-out tenant mode
- tenant aggregate catalog
- user namespace
- team namespace
- shared/org namespace
- admin namespace list
- public preview namespace in public mode, if configured

Catalog data and filters:

- total servers, ready servers, tool count, publish quota
- status filters: All, Ready, Issues
- search by server name, namespace, description, endpoint, status
- search by tools, prompts, resources, tasks
- search by inventory labels and server labels
- no-match state

Server cards:

- title, namespace, description, status, ready pods, endpoint
- tool/prompt/resource/task counts
- created date and labels
- inventory details for string and object forms
- access JSON / connect config
- long names/descriptions/labels do not break layout

Actions:

- Details opens the detail panel and loads recent activity.
- Copy URL and Copy JSON show success or fallback/error feedback.
- Card Enter/Space selection works where the card is keyboard focusable.
- Retire is tested only against a temporary `qa-audit-*` MCPServer.
- Cancel leaves the server unchanged; confirm removes it; the detail panel
  resets after removal.

Cross-check:

```bash
./bin/mcp-runtime server list
kubectl get mcpservers -A
```

Any UI/CLI count mismatch is a finding unless the UI label clearly describes a
different metric, such as historical active servers.

## My Activity

Tenant user only:

- Metrics: My Servers, Ready, Requests, Denied, Deny Rate.
- Deployed Servers table: identity, namespace, status, inventory, endpoint,
  Analytics action, Prometheus action, Grafana action, Copy URL, tenant Retire.
- Usage controls: All servers, per-server selector, 24h/7d/30d/90d, Refresh.
- Usage tables: per-server usage, top tools, recent activity.
- Empty state: no personal servers.
- Shared catalog servers are excluded from personal count.
- Analytics empty response and API error render clear states.
- If a selected server disappears, selector and tables recover cleanly.
- Scoped observability: clicking Prometheus for a temporary tenant server opens
  an allowlisted `/api/v1/runtime/observability/prometheus/query` URL with
  `namespace` and `server` filters for that exact server.
- Scoped observability links: `/api/v1/runtime/observability/links` returns only
  queries for the authorized tenant server and does not expose arbitrary PromQL.
- Grafana tenant action: clicking Grafana opens the default scoped platform
  Grafana dashboard endpoint for that namespace/server. It must not open the raw
  cluster-wide `/grafana` UI for tenant users.
- Tenant negative checks: the same user receives an intentional 403/404 when
  requesting observability links or Prometheus queries for a shared or foreign
  namespace/server.
- Backend metrics regressions: when the diff touches gateway metrics,
  Prometheus scrape config, or runtime observability APIs, the browser matrix
  must include the Metrics button plus network evidence from the scoped
  Prometheus query endpoint, even when no UI file changed.

## API Keys

Table:

- name, prefix, created timestamp, active/revoked state, revoke action.

Create:

- empty name validation produces visible feedback or native validation.
- normal, long, special-character, and duplicate-looking names.
- one-time key display.
- input clearing after success.
- list refresh preserves or intentionally clears the one-time key; record the
  expected behavior from implementation.
- automatic one-time key timeout, if implemented.

Revoke:

- modal cancel leaves row active.
- modal confirm revokes row.
- error toast or visible error appears on failed revoke.

Cleanup: revoke all temporary `qa-audit-*` keys before finishing.

## Admin Dashboard

Admin only:

- Summary metrics: Total Events, Active Servers, Active Grants, Active Sessions.
- Usage metrics: Events, Allowed, Denied, Humans, Agents, Allow Rate, Deny Rate.
- Decision meter widths match allow/deny rates.
- Row limit controls: Top 10, Top 25, Top 50.
- Analytics tables: MCP Servers, Users & Agents, Tools, Decisions.
- Decision Audit table: time/source/type, human, agent, server/namespace/action,
  decision badge, policy reason/trust/session.
- Auto-refresh enabled starts polling; disabled stops polling; logout stops it.
- Non-admin never starts dashboard polling.
- Empty/zero, allow-only, deny-only, mixed totals, malformed timestamps, and API
  errors render usable states when safely reproducible.

Cross-check current inventory metrics:

```bash
./bin/mcp-runtime server list
./bin/mcp-runtime access grant list --namespace mcp-servers
./bin/mcp-runtime access session list --namespace mcp-servers
```

If dashboard metrics are historical rather than current, the UI label must make
that clear.

## Governance

Summary:

- Active Grants, Disabled Grants, Active Sessions, Revoked Sessions.

Grant list and filters:

- name, namespace, server reference, server namespace fallback
- human, agent, team, and combined subjects
- max trust, allowed side effects, active/disabled state
- filter by grant name, server, human ID, agent ID, team ID, no-match state

Grant form validation:

- missing name, missing server, no subject, no side effects
- valid `tool:allow`, `tool:deny`, `tool:allow:medium`
- valid tool name containing `:`
- invalid decision, invalid trust, malformed line

Grant actions:

- create `qa-audit-*`
- refresh
- disable cancel/confirm
- enable cancel/confirm
- API error feedback
- policy render cross-check:

```bash
./bin/mcp-runtime server policy inspect <server> --namespace <namespace>
```

Session form validation:

- missing name, missing server, no subject
- valid local datetime converted to UTC
- invalid datetime
- empty expiry allowed

Session actions:

- create `qa-audit-*`
- refresh
- revoke cancel/confirm
- unrevoke cancel/confirm
- API error feedback
- policy/session cross-check:

```bash
./bin/mcp-runtime access session get <name> --namespace <namespace>
kubectl get mcpagentsession <name> -n <namespace> -o yaml
```

## Operations

Admin only:

- Summary metrics: MCP Servers, Ready, Issues, Recent Logins, Users, Images,
  Deployments, MCP Events.
- Filters: user/tenant, since datetime, until datetime, Apply, Clear, invalid
  datetime behavior.
- Logged-in Users table.
- MCP Server Health table.
- Activity Timeline table.
- MCP Activity table.
- Images and Deployments table, including long image refs.
- MCP Server Inspector empty and selected states.

Cross-check Operations server counts:

```bash
./bin/mcp-runtime server list
kubectl get mcpservers -A
```

Missing namespaces or stale server counts are product findings.

## Platform

Admin only:

- Platform Health component cards: display name, status, ready count, namespace,
  resource/key, message.
- Styling for Ready, Degraded, NotReady, unknown.
- Fleet Health: total servers, ready servers, issues, server table.
- Fleet empty/error states, if safely reproducible.
- Component restart selector includes every restartable component documented by
  the UI. If health shows Grafana/Prometheus but restart omits them, either the
  UI copy must explain why or it is a finding.
- Restart no-selection validation must show visible feedback.
- Restart modal cancel works.
- Confirm restart only for an explicitly safe temporary target or skip with a
  documented reason.
- Open Operations activates Operations and triggers data load.

## Responsive And Accessibility

Run at least:

- desktop: 1200x900
- mobile: 390x844

Check:

- no body-level horizontal overflow unless intentional and documented
- tab bar scroll behavior on mobile
- text does not overlap controls or table cells
- modals fit viewport and remain keyboard usable
- buttons, inputs, selects, tabs, and dialogs have accessible names/roles
- Enter/Space work on keyboard-reachable actions
- focus does not remain trapped in hidden modals after close

Record accessibility snapshot evidence for each major tab.

## Platform Mode And Extensibility

For broad UI audits, cover tenant mode against current Kind plus org/public
mode through local UI-server config passes when practical.

Extensibility cases:

- duplicate or empty namespace list
- user, team, shared/org, and public preview namespaces
- string vs object tools/prompts/resources/tasks
- server labels and inventory labels
- unknown status values
- missing endpoint or missing access JSON
- new component returned by API appears without code changes
- platform JWT, UI API-key, OIDC/Google, and direct API-key clients

If a mode or case cannot be exercised, mark it skipped with a reason and the
command or fixture needed to cover it later.

## React console structure (after `ui/servers_console_redesign`)

The console is hash-routed, so every screen is directly addressable and
back/forward work. Drive coverage from these URLs rather than clicking through:

| Route | Screen |
| --- | --- |
| `#/servers` | Servers home (default; summary, server fleet, tool catalog) |
| `#/servers?server=<ns>/<name>` | Server inspector open |
| `#/servers?tool=<ns>/<server>/<tool>` | Tool inspector open |
| `#/activity` | Tenant activity |
| `#/keys` | Personal API keys |
| `#/admin/access` `#/admin/teams` `#/admin/operations` `#/admin/platform` `#/admin/analytics` | Administration sections |
| `#/signin` | Sign-in |

Things that changed and will break an older script:

- There is no "More workspaces" tab and no iframe. `workspace-tab-legacy` and
  `getByTitle('MCP Sentinel dashboard')` no longer exist. The legacy assets are
  still served at `/legacy/index.html` for the one unmigrated drill-down.
- Sign-in has separate Account and API-key modes: click
  `signin-mode-api-key` before filling `login-api-key`.
- `window.confirm` is gone. Destructive actions open a shared dialog:
  `access-confirm` / `teams-confirm` / `platform-confirm` / `revoke-confirm`,
  each with `-yes` and `-cancel` buttons. Escape cancels.
- Administration is a grouped rail (`admin-section-<id>`) plus
  `admin-section-select` below 900px.
- Operations is tabbed: `operations-tab-users|audit|images`. Only the active
  tab's table is in the DOM.
- Platform health is a card grid (`platform-components`, `platform-component`),
  not `platform-table`. `restart-all` sits in a separated disruptive-actions
  area.
- The tool catalog paginates at 25 rows: `tool-table-next-page`,
  `tool-table-prev-page`, `tool-table-pagination-status`.
- Server and tool search are separate and scoped: `server-search` matches server
  metadata only, `tool-search` matches tool metadata only.

Assertions worth keeping in any sweep, because they encode truthfulness rules:

- An unknown metric renders `—`, never `0`.
- A session with `revoked=false` and a past `expiresAt` reads "Expired".
- An analytics outage renders an error state, not an empty table.

## Serving a candidate build against the cluster

The deployed UI pod may be older than the source and only proxies the catalog
paths, so admin/keys/analytics reads 404 against it. Run the candidate service
locally and point the Vite dev server at it — the full recipe is in
`docs/ui-console-design-system.md`, "Local development against a cluster".
`npm run dev` honours `MCP_DEV_UPSTREAM` (default `http://localhost:18080`).

Repeated scripted sign-ins trip the UI login rate limiter (429). Raise
`UI_LOGIN_RATE_CAPACITY` on the local candidate process, or sign in once per
browser context and reuse it.
