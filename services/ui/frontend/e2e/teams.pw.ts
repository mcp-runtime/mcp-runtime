import { expect, test, type Page } from "@playwright/test";

// Explicit API fixtures: these checks cover UI behavior, not live authorization.
async function fixture(page: Page, role = "admin", failMembers = false) {
  const reads: string[] = [];
  const teams = [
    { id: "t-1", slug: "acme", name: "Acme", namespace: "team-acme", role: "owner" },
    { id: "t-2", slug: "globex", name: "Globex", namespace: "team-globex", role: "member" },
  ];
  await page.route("**/config.js", route => route.fulfill({ contentType: "application/javascript", body: "" }));
  await page.route("**/auth/status", route => route.fulfill({ json: { authenticated: true, principal: { role, subject: "admin" } } }));
  await page.route("**/api/ui/v1/**", route => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/ui/v1", "");
    reads.push(path);
    let json: unknown = {};
    if (path === "/runtime/teams" || path === "/runtime/workspace/teams") json = { teams };
    else if (path === "/runtime/teams/acme/members") {
      if (failMembers) return route.fulfill({ status: 503, json: { message: "members unavailable" } });
      json = { members: [{ user_id: "u-1", email: "alice@example.com", role: "owner" }] };
    }
    else if (path === "/runtime/teams/globex/members") json = { members: [] };
    else if (path === "/runtime/teams/acme/agents") json = { agents: [{ id: "agt-acme", name: "Build agent", team_id: "t-1", team_slug: "acme", status: "active", created_by: "u-1" }] };
    else if (path === "/runtime/teams/globex/agents") json = { agents: [] };
    else if (path === "/runtime/agents/agt-acme") json = { agent: { id: "agt-acme", name: "Build agent", team_id: "t-1", team_slug: "acme", status: "active" }, grants: [], sessions: [], can_manage: false };
    else if (path === "/user/analytics/usage") json = { filters: { agent_id: url.searchParams.get("agent_id") }, totals: { events: 0 }, requests: [], recent: [] };
    return route.fulfill({ json });
  });
  return reads;
}

for (const width of [1440, 390]) {
  test(`team members and agents remain scoped when switching teams at ${width}px`, async ({ page }) => {
    const errors: string[] = [];
    page.on("pageerror", error => errors.push(error.message));
    await fixture(page);
    await page.setViewportSize({ width, height: 900 });
    await page.goto("/#/admin/teams");
    await expect(page.getByTestId("team-members-table")).toContainText("alice@example.com");
    await expect(page.getByTestId("team-agents-table")).toContainText("Build agent");
    await expect(page.getByRole("group", { name: "Selected team membership" })).toContainText("Owners1");
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await page.screenshot({ path: test.info().outputPath(`team-overview-${width}.png`), fullPage: true });
    await page.getByTestId("team-select-input").selectOption("globex");
    await expect(page.getByRole("heading", { name: "Agents of Globex" })).toBeVisible();
    await expect(page.getByTestId("team-agents-table")).toContainText("No agents registered to this team.");
    await expect(page.getByText("alice@example.com", { exact: true })).toHaveCount(0);
    await expect(page.getByRole("link", { name: "Build agent" })).toHaveCount(0);
    await page.getByTestId("team-select-input").selectOption("acme");
    await page.getByRole("link", { name: "Build agent", exact: true }).click();
    await expect(page.getByTestId("agent-detail")).toBeVisible();
    expect(errors).toEqual([]);
  });
}

test("member directory outage keeps agents visible and membership totals unknown", async ({ page }) => {
  await fixture(page, "admin", true);
  await page.goto("/#/admin/teams");
  await expect(page.getByTestId("team-members-error")).toBeVisible();
  await expect(page.getByTestId("team-agents-table")).toContainText("Build agent");
  await expect(page.getByRole("group", { name: "Selected team membership" })).toContainText("Members—");
  await expect(page.getByTestId("team-agents-table")).toContainText("u-1");
});

test("non-admin direct navigation performs no team-management reads", async ({ page }) => {
  const reads = await fixture(page, "member");
  await page.goto("/#/admin/teams");
  await expect(page.getByRole("heading", { name: "Servers", exact: true })).toBeVisible();
  await expect(page.getByTestId("team-overview")).toHaveCount(0);
  expect(reads.filter(path => path.startsWith("/runtime/teams"))).toEqual([]);
});


test("team owner can inspect its management page while a member cannot", async ({ page }) => {
  await fixture(page, "user");
  await page.goto("/#/workspace/team?team=acme");
  await expect(page.getByTestId("team-members-table")).toContainText("alice@example.com");
  await expect(page.getByTestId("team-agents-table")).toContainText("Build agent");
  await page.goto("/#/workspace/team?team=globex");
  await expect(page.getByText("You cannot manage this team.")).toBeVisible();
  await expect(page.getByTestId("team-overview")).toHaveCount(0);
});
