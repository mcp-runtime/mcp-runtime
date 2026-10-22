import { expect, test, type Page } from "@playwright/test";

// UI interaction fixtures; live authentication and MCP authorization remain
// covered by the platform's Kind/Staging suites.
const endpoint = "https://mcp.example.com/team-a/example/mcp";
const server = {
  name: "example", namespace: "team-a", ready: "1/1", status: "Ready",
  authMode: "oauth", endpoint: "http://internal-service:8080/mcp",
  access_json: { mcpServers: { example: { type: "http", url: endpoint } } },
};

async function catalog(page: Page, options: { signedOut?: boolean; noConfig?: boolean; headers?: boolean } = {}) {
  const entry = options.headers ? { ...server.access_json.mcpServers.example, headers: { "Private-Token": "" } } : server.access_json.mcpServers.example;
  const item = { ...server, ...(options.headers ? { authMode: "header", authHeaders: ["Private-Token"] } : {}), access_json: options.noConfig ? undefined : { mcpServers: { example: entry } } };
  await page.route("**/config.js", route => route.fulfill({ contentType: "application/javascript", body: "" }));
  await page.route("**/auth/status", route => route.fulfill({ json: options.signedOut ? { authenticated: false } : { authenticated: true, principal: { role: "user", subject: "member-a", namespace: "team-a" } } }));
  await page.route(/\/api\/(?:ui\/)?v1\//, route => {
    const path = new URL(route.request().url()).pathname.replace(/^\/api\/(?:ui\/)?v1/, "");
    const json = path === "/runtime/namespaces" ? { namespaces: [{ namespace: "team-a" }] }
      : path === "/runtime/servers" ? { servers: [item] }
      : path === "/runtime/servers/team-a/example" ? { server: item }
      : path === "/runtime/server-events" ? { events: [] }
      : path === "/runtime/teams" ? { teams: [] }
      : { tools: [] };
    return route.fulfill({ json });
  });
}

for (const width of [1280, 390]) {
  test(`server install chooser preserves config and fits at ${width}px`, async ({ page }) => {
    const errors: string[] = [];
    const failedRequests: string[] = [];
    page.on("pageerror", error => errors.push(error.message));
    page.on("response", response => { if (response.status() >= 400) failedRequests.push(`${response.status()} ${new URL(response.url()).pathname}`); });
    await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
    await catalog(page);
    await page.setViewportSize({ width, height: 900 });
    await page.goto("/#/servers");
    await page.getByTestId("server-card-details").click();
    const install = page.getByRole("region", { name: "Install in an MCP client" });
    await expect(install.getByRole("link")).toHaveCount(5);
    const cursor = install.getByRole("link", { name: "Install in Cursor" });
    await cursor.focus();
    await expect(cursor).toBeFocused();
    const url = new URL((await cursor.getAttribute("href"))!);
    expect(JSON.parse(Buffer.from(url.searchParams.get("config")!, "base64").toString("utf8"))).toEqual({ type: "http", url: endpoint });
    const claude = install.getByRole("link", { name: "Add to Claude", exact: true });
    expect(new URL((await claude.getAttribute("href"))!).searchParams.get("connectorUrl")).toBe(endpoint);
    await expect(claude).toHaveAttribute("rel", "noopener noreferrer");
    await page.getByRole("tab", { name: "VS Code", exact: true }).click();
    await expect(page.getByTestId("server-detail-config")).toContainText('"servers"');
    await expect(page.getByTestId("server-detail-config")).toContainText(endpoint);
    await page.getByTestId("server-detail-copy-config").click();
    const copied = await page.evaluate(() => navigator.clipboard.readText());
    expect(JSON.parse(copied)).toEqual(server.access_json);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    const bounds = await install.boundingBox();
    expect(bounds!.width).toBeLessThanOrEqual(width);
    expect(errors).toEqual([]);
    expect(failedRequests).toEqual([]);
    await page.screenshot({ path: test.info().outputPath(`server-install-${width}.png`), fullPage: true });
  });
}

test("header-auth installs retain the required header fields", async ({ page }) => {
  await catalog(page, { headers: true });
  await page.goto("/#/servers?server=team-a/example");
  const install = page.getByTestId("server-detail-install");
  await expect(install.getByRole("link", { name: "Add to Claude", exact: true })).toHaveCount(0);
  const vscode = new URL((await install.getByRole("link", { name: "Install in VS Code" }).getAttribute("href"))!);
  expect(JSON.parse(decodeURIComponent(vscode.search.slice(1))).headers).toEqual({ "Private-Token": "" });
  await expect(page.getByTestId("server-detail-auth")).toContainText("Private-Token");
});

test("servers without published config have no install controls", async ({ page }) => {
  await catalog(page, { noConfig: true });
  await page.goto("/#/servers?server=team-a/example");
  await expect(page.getByTestId("server-detail-no-config")).toBeVisible();
  await expect(page.getByTestId("server-detail-install")).toHaveCount(0);
});

test("signed-out org readers cannot reach server installation controls", async ({ page }) => {
  await catalog(page, { signedOut: true });
  await page.goto("/#/servers?server=team-a/example");
  await expect(page.getByTestId("catalog-signed-out")).toBeVisible();
  await expect(page.getByTestId("server-detail-install")).toHaveCount(0);
});
