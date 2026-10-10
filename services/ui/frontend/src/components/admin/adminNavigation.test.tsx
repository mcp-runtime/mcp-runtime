import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { App } from "../../App";
import { AppProviders } from "../../providers/AppProviders";

// Exercises the admin workspace through the real shell, so the navigation and
// the route guard are covered together rather than in isolation.

const CATALOG: Record<string, unknown> = {
  "/runtime/namespaces": { namespaces: [] },
  "/runtime/servers": { servers: [] },
  "/runtime/tools": { tools: [] },
  "/runtime/grants": { grants: [] },
  "/runtime/sessions": { sessions: [] },
  "/runtime/teams": { teams: [] },
  "/runtime/components": { components: [] },
  "/admin/operations": { users: [], audit_logs: [], images: [] },
};

function stub(role: string | undefined, authenticated = true) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/auth/status") {
      return {
        ok: true,
        status: 200,
        json: async () => ({
          authenticated,
          principal: authenticated ? { role, email: "someone@mcpruntime.org" } : undefined,
        }),
      } as unknown as Response;
    }
    const key = Object.keys(CATALOG).find((path) => url.includes(path));
    return {
      ok: true,
      status: 200,
      json: async () => (key ? CATALOG[key] : {}),
    } as unknown as Response;
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function renderApp() {
  return render(
    <AppProviders>
      <App />
    </AppProviders>
  );
}

beforeEach(() => {
  delete window.MCP_API_BASE;
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("admin workspace navigation", () => {
  it("offers administration pages directly in the primary sidebar", async () => {
    const user = userEvent.setup();
    stub("admin");

    renderApp();
    const tab = await screen.findByTestId("admin-section-teams");

    await user.click(tab);
    expect(await screen.findByTestId("teams-table")).toBeInTheDocument();
    const primary = screen.getByRole("navigation", { name: "Primary", exact: true });
    for (const section of ["teams", "operations", "platform", "analytics"]) {
      expect(primary).toContainElement(screen.getByTestId(`admin-section-${section}`));
    }
    expect(screen.queryByRole("navigation", { name: "Administration sections" })).not.toBeInTheDocument();
  });

  it("filters admin pages and navigates between sections without another sidebar", async () => {
    const user = userEvent.setup();
    stub("admin");
    renderApp();
    await screen.findByTestId("admin-section-teams");
    await user.type(screen.getByRole("searchbox", { name: "Filter navigation" }), "health");
    expect(screen.queryByTestId("admin-section-teams")).not.toBeInTheDocument();
    await user.click(screen.getByTestId("admin-section-platform"));
    expect(window.location.hash).toBe("#/admin/platform");
    expect(screen.getByTestId("admin-section-platform")).toHaveAttribute("aria-current", "page");
    expect(await screen.findByTestId("platform-empty")).toBeInTheDocument();
    await user.clear(screen.getByRole("searchbox", { name: "Filter navigation" }));
    await user.click(screen.getByTestId("admin-section-operations"));
    expect(window.location.hash).toBe("#/admin/operations");
    expect(screen.queryByRole("navigation", { name: "Administration sections" })).not.toBeInTheDocument();
  });

  it("offers the same section links in compact navigation and closes it on selection", async () => {
    const user = userEvent.setup();
    stub("admin");
    renderApp();
    await screen.findByTestId("admin-section-teams");
    await user.click(screen.getByTestId("nav-toggle"));
    await user.click(screen.getByTestId("mobile-admin-section-platform"));
    expect(window.location.hash).toBe("#/admin/platform");
    expect(screen.getByTestId("nav-toggle")).toHaveAttribute("aria-expanded", "false");
  });

  it("hides administration pages from a tenant user", async () => {
    stub("user");

    renderApp();
    await screen.findByTestId("workspace-tab-servers");

    expect(screen.queryByTestId("admin-section-teams")).not.toBeInTheDocument();
    expect(screen.getByTestId("workspace-tab-servers")).toBeInTheDocument();
  });

  it("hides administration pages from a signed-out visitor", async () => {
    stub(undefined, false);

    renderApp();
    await screen.findByTestId("workspace-tab-servers");

    expect(screen.queryByTestId("admin-section-teams")).not.toBeInTheDocument();
  });

  it("drops out of the admin workspace when the session signs out", async () => {
    const user = userEvent.setup();
    const fetchMock = stub("admin");

    renderApp();
    await user.click(await screen.findByTestId("admin-section-teams"));
    await screen.findByTestId("admin-section-teams");

    fetchMock.mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ authenticated: false }),
    } as unknown as Response);
    await user.click(await screen.findByTestId("account-trigger"));
    await user.click(screen.getByTestId("logout-button"));

    await waitFor(() =>
      expect(screen.queryByTestId("admin-section-teams")).not.toBeInTheDocument()
    );
    expect(screen.queryByTestId("admin-section-teams")).not.toBeInTheDocument();
  });

  it("refuses a deep link into administration for a tenant user", async () => {
    window.location.hash = "#/admin/access";
    stub("user");

    renderApp();
    await screen.findByTestId("workspace-tab-servers");

    // The guard and the route both fail closed, so nothing admin renders.
    expect(screen.queryByTestId("admin-section-teams")).not.toBeInTheDocument();
    expect(screen.queryByTestId("grants-table")).not.toBeInTheDocument();
  });

  it("puts the administration section in the URL", async () => {
    const user = userEvent.setup();
    stub("admin");

    renderApp();
    await user.click(await screen.findByTestId("admin-section-teams"));
    await user.click(await screen.findByTestId("admin-section-teams"));

    expect(window.location.hash).toBe("#/admin/teams");
  });
});

// Access control is a top-level workspace reachable by any authenticated
// principal - admin or tenant - matching the backend's plain auth()
// middleware on /runtime/grants and /runtime/sessions.
describe("access control workspace navigation", () => {
  it("offers Access control to a tenant user and opens it", async () => {
    const user = userEvent.setup();
    stub("user");

    renderApp();
    await user.click(await screen.findByTestId("workspace-tab-access"));

    expect(await screen.findByTestId("grants-table")).toBeInTheDocument();
    expect(window.location.hash).toBe("#/access");
  });

  it("offers Access control to an admin too", async () => {
    const user = userEvent.setup();
    stub("admin");

    renderApp();
    await user.click(await screen.findByTestId("workspace-tab-access"));

    expect(await screen.findByTestId("grants-table")).toBeInTheDocument();
  });

  it("hides Access control from a signed-out visitor", async () => {
    stub(undefined, false);

    renderApp();
    await screen.findByTestId("workspace-tab-servers");

    expect(screen.queryByTestId("workspace-tab-access")).not.toBeInTheDocument();
  });
});
