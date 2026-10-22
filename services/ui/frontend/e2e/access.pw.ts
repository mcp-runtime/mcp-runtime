import { expect, test, type Page } from "@playwright/test";

// Browser regressions use explicit API fixtures. Real service authorization and
// certificate traffic remain covered by Kind/Staging E2E, never by these mocks.
async function fixture(page: Page, partial = false) {
  let inactive = false;
  const agent = () => ({ id: "agt-b", name: "B Agent", team_id: "B", team_slug: "b", status: inactive ? "inactive" : "active" });
  const grant = { name: "delegated", namespace: "team-a", subject: { teamID: "B", agentID: "agt-b" }, serverRef: { name: "demo" }, disabled: false };
  const session = () => ({ name: "session-b", namespace: "team-a", serverRef: { name: "demo" }, subject: { humanID: "alice", agentID: "agt-b", teamID: "B" }, grantName: "delegated", grantNamespace: "team-a", revoked: inactive, revocationCause: inactive ? "agent_deactivated" : "", expiresAt: "2099-01-01T00:00:00Z" });
  await page.route("**/config.js", (route) => route.fulfill({ contentType: "application/javascript", body: "" }));
  await page.route("**/auth/status", (route) => route.fulfill({ json: { authenticated: true, principal: { role: "admin", subject: "admin" } } }));
  await page.route("**/api/ui/v1/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/ui/v1", "");
    let json: unknown = {};
    if (path === "/runtime/teams") json = { teams: [{ id: "B", slug: "b", name: "B", namespace: "team-b", role: "owner" }] };
    else if (path === "/runtime/teams/b/agents") json = { agents: [agent()] };
    else if (path === "/runtime/agents/agt-b") json = { agent: agent(), grants: [grant], sessions: [session()], can_manage: true };
    else if (path === "/user/analytics/usage") json = { filters: { agent_id: url.searchParams.get("agent_id") }, totals: { events: 0, allowed: 0, denied: 0 }, requests: [], recent: [] };
    else if (path === "/runtime/agents/agt-b/deactivate") {
      inactive = true;
      if (partial) return route.fulfill({ status: 500, json: { message: "agent is inactive but session revocation is incomplete; retry deactivation" } });
      json = { agent: agent() };
    }
    else if (path === "/runtime/grants") json = { grants: [grant] };
    else if (path === "/runtime/sessions") json = { sessions: [session()] };
    else if (path === "/events") json = { events: [{ timestamp: new Date().toISOString(), namespace: "team-a", server: "demo", tool_name: "echo", decision: "would_deny", payload: { matched_grant: "delegated", matched_grant_namespace: "team-a", matched_session: "session-b", matched_session_namespace: "team-a" } }] };
    else if (path === "/runtime/namespaces") json = { namespaces: [{ namespace: "team-a" }, { namespace: "team-b" }] };
    return route.fulfill({ json });
  });
}

test("agent → session → exact authorizing grant shares namespace-safe details", async ({ page }) => {
  await fixture(page);
  await page.goto("/#/agents?team=b&agent=agt-b");
  await expect(page.getByRole("heading", { name: "B Agent" })).toBeVisible();
  await page.screenshot({ path: test.info().outputPath("agent-details.png"), fullPage: true });
  await page.getByRole("link", { name: "team-a/session-b", exact: true }).click();
  await expect(page.getByTestId("access-detail-kicker")).toHaveText("team-a / MCPAgentSession");
  await expect(page.getByText("alice", { exact: true })).toBeVisible();
  await page.getByRole("link", { name: "team-a/delegated", exact: true }).click();
  await expect(page.getByTestId("access-detail-kicker")).toHaveText("team-a / MCPAccessGrant");
  await expect(page.getByLabel("Loaded decision breakdown")).toContainText("would_deny: 1");
  await page.goBack();
  await expect(page.getByTestId("access-detail-kicker")).toHaveText("team-a / MCPAgentSession");
});

test("agent ownership, certificate issuance and exact-agent tool activity remain distinct on mobile", async ({ page }) => {
  await fixture(page);
  await page.setViewportSize({ width: 390, height: 844 });
  const now = new Date().toISOString();
  await page.route("**/api/ui/v1/runtime/agents/agt-b", route => route.fulfill({ json: {
    agent: { id: "agt-b", name: "B Agent", team_id: "B", team_slug: "b", status: "active", created_by: "creator-user", created_at: now, updated_at: now },
    grants: [], sessions: [], can_manage: true,
    certificates: { recorded: 2, since: now, until: now, recent: [{ namespace: "team-a", session: "session-b", server: "demo", human_id: "alice", serial: "abc123", fingerprint_sha256: "a".repeat(64), spiffe_id: "spiffe://example.org/ns/team-a/session/session-b", not_before: now, not_after: "2099-01-01T00:00:00Z", issued_at: now }] },
  } }));
  const activityQueries: URLSearchParams[] = [];
  await page.route("**/api/ui/v1/user/analytics/usage?**", route => {
    const query = new URL(route.request().url()).searchParams;
    activityQueries.push(query);
    return route.fulfill({ json: { filters: { agent_id: query.get("agent_id") }, totals: { events: 12, allowed: 10, denied: 2, http_failures: 2 }, requests: [{ namespace: "team-a", server: "demo", rpc_method: "tools/call", tool_name: "echo", decision: "allow", events: 10, http_failures: 0, last_seen: now, trace_id: "test-trace" }], recent: [] } });
  });
  await page.goto("/#/agents?team=b&agent=agt-b");
  await expect(page.getByText("creator-user", { exact: true })).toBeVisible();
  await expect(page.getByRole("group", { name: "Visible session lifecycle" })).toContainText("Visible sessions issued0");
  await expect(page.getByRole("group", { name: "Recorded certificates", exact: true })).toContainText("2");
  await expect(page.getByRole("group", { name: "Agent tool call totals" })).toContainText("12");
  expect(activityQueries[0].get("agent_id")).toBe("agt-b");
  expect(activityQueries[0].get("rpc_method")).toBe("tools/call");
  await page.getByLabel("Activity time range").selectOption("24");
  await expect.poll(() => activityQueries.some(query => query.get("window_hours") === "24")).toBe(true);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.screenshot({ path: test.info().outputPath("agent-accountability-mobile.png"), fullPage: true });
});

test("denied agent details do not fetch activity or reveal certificate records", async ({ page }) => {
  await fixture(page);
  await page.route("**/api/ui/v1/runtime/agents/agt-b", route => route.fulfill({ status: 403, json: { message: "forbidden" } }));
  let activityReads = 0;
  await page.route("**/api/ui/v1/user/analytics/usage?**", route => { activityReads++; return route.fulfill({ json: {} }); });
  await page.goto("/#/agents?team=b&agent=agt-b");
  await expect(page.getByTestId("agent-detail-error")).toBeVisible();
  await expect(page.getByTestId("agent-mcp-activity")).toHaveCount(0);
  await expect(page.getByTestId("agent-certificates-table")).toHaveCount(0);
  expect(activityReads).toBe(0);
});

test("deactivation previews impact and exposes partial failures with final state", async ({ page }) => {
  await fixture(page, true);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/#/agents?team=b&agent=agt-b");
  await page.getByRole("button", { name: "Deactivate agent", exact: true }).click();
  await expect(page.getByRole("alertdialog")).toContainText("1 visible active sessions");
  await page.getByTestId("agent-confirm-yes").click();
  const result = page.getByTestId("agent-lifecycle-result");
  await expect(result.getByRole("alert")).toContainText("revocation is incomplete");
  await expect(result).toContainText("Final agent status: inactive");
  await expect(result).toContainText("Agent deactivation");
  await page.screenshot({ path: test.info().outputPath("deactivation-result-mobile.png"), fullPage: true });
  await page.getByRole("button", { name: "Done", exact: true }).click();
  await expect(result).toBeHidden();
});

test("gateway audit opens exact grant and session deep links", async ({ page }) => {
  await fixture(page);
  await page.goto("/#/admin/analytics");
  await page.getByRole("button", { name: "Load gateway decisions" }).click();
  await page.getByRole("link", { name: "team-a/delegated", exact: true }).click();
  await expect(page.getByTestId("access-detail-kicker")).toHaveText("team-a / MCPAccessGrant");
});

test("resource owner creates a time-bound grant for a known foreign team's active agent", async ({ page }) => {
  await fixture(page);
  await page.route("**/auth/status", (route) => route.fulfill({ json: { authenticated: true, principal: { role: "user", subject: "owner-a", namespace: "team-a" } } }));
  await page.route("**/api/ui/v1/runtime/teams", (route) => route.fulfill({ json: { teams: [{ id: "A", slug: "a", name: "Team A", namespace: "team-a", role: "owner" }] } }));
  await page.route("**/api/ui/v1/runtime/delegation-recipients?**", (route) => route.fulfill({ json: { team: { id: "B", slug: "b", name: "Team B" }, agents: [{ id: "agt-b", name: "B Agent", status: "active", team_id: "B", team_slug: "b" }], members: [{ user_id: "alice" }] } }));
  let submitted: Record<string, unknown> | undefined;
  await page.route("**/api/ui/v1/runtime/grants", async (route) => {
    if (route.request().method() !== "POST") return route.fallback();
    submitted = route.request().postDataJSON();
    return route.fulfill({ status: 201, json: { grant: submitted } });
  });
  await page.goto("/#/access");
  await page.getByTestId("grant-create-toggle").click();
  await page.getByTestId("grant-name").fill("delegation-b");
  await page.getByRole("textbox", { name: "Server (required)", exact: true }).fill("demo");
  await page.getByRole("checkbox", { name: "Delegate to another team" }).check();
  await page.getByTestId("delegation-team-slug").fill("b");
  await page.getByTestId("delegation-lookup").click();
  await page.getByTestId("delegation-agent").selectOption("agt-b");
  await expect(page.getByTestId("grant-expires-at")).not.toHaveValue("");
  await page.screenshot({ path: test.info().outputPath("cross-team-grant.png"), fullPage: true });
  await page.getByTestId("grant-create-submit").click();
  await expect.poll(() => submitted).toMatchObject({ namespace: "team-a", subject: { teamID: "B", agentID: "agt-b" } });
  expect(submitted?.subject).not.toHaveProperty("humanID");
  expect(Date.parse(String(submitted?.expiresAt))).toBeGreaterThan(Date.now());
});
