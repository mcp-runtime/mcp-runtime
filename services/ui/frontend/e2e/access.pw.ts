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
