import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { axe } from "vitest-axe";

import { App } from "../../App";
import { AppProviders } from "../../providers/AppProviders";
import { visibleWorkspaceTabs } from "../WorkspaceNavigation";
import type { AuthStatus } from "../../api/types";

const AXE_OPTIONS = { rules: { "color-contrast": { enabled: false } } };

const TENANT = {
  authenticated: true,
  principal: { role: "user", subject: "user-1", email: "dev@example.com" },
  csrf_token: "t",
};
const ADMIN = {
  authenticated: true,
  principal: { role: "admin", subject: "admin-1", email: "admin@example.com" },
  csrf_token: "t",
};
const API_KEY_SESSION = {
  authenticated: true,
  principal: { role: "admin", auth_type: "ui_api_key" },
  csrf_token: "t",
};

const EMPTY_USAGE = {
  totals: {
    events: 0,
    allowed: 0,
    denied: 0,
    unique_servers: 0,
    unique_humans: 0,
    unique_agents: 0,
    unique_sessions: 0,
  },
  servers: [],
  tools: [],
  window_days: 7,
};

function stubApp(status: Record<string, unknown>) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      let body: unknown = {};
      if (url === "/auth/status") body = status;
      else if (url.includes("/user/api-keys")) body = { keys: [] };
      else if (url.includes("/user/analytics/usage")) body = EMPTY_USAGE;
      else if (url.includes("/runtime/teams")) body = { teams: [] };
      else if (url.includes("/runtime/namespaces")) body = { namespaces: [] };
      else if (url.includes("/runtime/servers")) body = { servers: [] };
      else if (url.includes("/runtime/tools")) body = { tools: [] };
      return {
        ok: true,
        status: 200,
        json: async () => body,
        text: async () => JSON.stringify(body),
      } as unknown as Response;
    })
  );
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

describe("visibleWorkspaceTabs", () => {
  const ids = (auth: AuthStatus) => visibleWorkspaceTabs(auth).map((tab) => tab.id);

  it("shows only public workspaces when signed out", () => {
    expect(ids({ authenticated: false })).toEqual(["servers"]);
  });

  it("shows Activity and Keys to a tenant user", () => {
    expect(ids(TENANT as AuthStatus)).toEqual(["servers", "agents", "access", "activity", "keys"]);
  });

  it("hides Activity from admins but keeps Keys when they have an identity", () => {
    // Legacy: Activity is data-user-only; Keys needs a user subject.
    expect(ids(ADMIN as AuthStatus)).toEqual(["servers", "agents", "access", "keys", "admin"]);
  });

  it("hides both from a session with no user identity", () => {
    expect(ids(API_KEY_SESSION as AuthStatus)).toEqual(["servers", "agents", "access", "admin"]);
  });
});

describe("workspace navigation", () => {
  it("lets a tenant user open Activity and Keys", async () => {
    const user = userEvent.setup();
    stubApp(TENANT);

    renderApp();
    await screen.findByTestId("workspace-tab-activity");

    await user.click(screen.getByTestId("workspace-tab-activity"));
    expect(await screen.findByTestId("usage-summary")).toBeInTheDocument();

    await user.click(screen.getByTestId("workspace-tab-keys"));
    expect(await screen.findByTestId("api-keys-empty")).toBeInTheDocument();
  });

  it("does not offer Activity to an admin", async () => {
    stubApp(ADMIN);

    renderApp();
    await screen.findByTestId("workspace-tab-keys");

    expect(screen.queryByTestId("workspace-tab-activity")).not.toBeInTheDocument();
  });

  it("falls back to Servers when sign-out removes the active workspace", async () => {
    const user = userEvent.setup();
    stubApp(TENANT);

    renderApp();
    await screen.findByTestId("workspace-tab-keys");
    await user.click(screen.getByTestId("workspace-tab-keys"));
    await screen.findByTestId("api-keys-empty");

    await user.click(await screen.findByTestId("account-trigger"));
    await user.click(screen.getByTestId("logout-button"));

    // Keys is gone for a signed-out principal, so the shell must not keep it.
    await waitFor(() =>
      expect(screen.queryByTestId("workspace-tab-keys")).not.toBeInTheDocument()
    );
    expect(screen.getByTestId("catalog-signed-out")).toBeInTheDocument();
  });

  it("no longer offers the retired More workspaces entry", async () => {
    stubApp(TENANT);

    renderApp();
    await screen.findByTestId("workspace-tab-servers");

    expect(screen.queryByTestId("workspace-tab-legacy")).not.toBeInTheDocument();
    expect(screen.queryByTitle("MCP Sentinel dashboard")).not.toBeInTheDocument();
  });

  it("puts the active workspace in the URL so it can be shared", async () => {
    const user = userEvent.setup();
    stubApp(TENANT);

    renderApp();
    await user.click(await screen.findByTestId("workspace-tab-keys"));
    await screen.findByTestId("api-keys-empty");

    expect(window.location.hash).toBe("#/keys");
  });

  it("opens a shared deep link straight into that workspace", async () => {
    window.location.hash = "#/activity";
    stubApp(TENANT);

    renderApp();

    expect(await screen.findByTestId("usage-summary")).toBeInTheDocument();
  });
});

describe("accessibility", () => {
  it("has no detectable violations on the Activity workspace", async () => {
    const user = userEvent.setup();
    stubApp(TENANT);

    const { container } = renderApp();
    await user.click(await screen.findByTestId("workspace-tab-activity"));
    await screen.findByTestId("usage-summary");

    expect(await axe(container, AXE_OPTIONS)).toHaveNoViolations();
  });

  it("has no detectable violations on the API keys workspace", async () => {
    const user = userEvent.setup();
    stubApp(TENANT);

    const { container } = renderApp();
    await user.click(await screen.findByTestId("workspace-tab-keys"));
    await screen.findByTestId("api-keys-empty");

    expect(await axe(container, AXE_OPTIONS)).toHaveNoViolations();
  });

  it("has no detectable violations while the one-time key is shown", async () => {
    const user = userEvent.setup();
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
        const url = String(input);
        let body: unknown = {};
        if (url === "/auth/status") body = TENANT;
        else if (url.includes("/user/api-keys") && init.method === "POST") {
          body = { key: { id: "uk_1", name: "ci" }, api_key: "mcpu_one_time" };
        } else if (url.includes("/user/api-keys")) body = { keys: [] };
        return {
          ok: true,
          status: 200,
          json: async () => body,
          text: async () => JSON.stringify(body),
        } as unknown as Response;
      })
    );

    const { container } = renderApp();
    await user.click(await screen.findByTestId("workspace-tab-keys"));
    await screen.findByTestId("create-key-name");
    await user.type(screen.getByTestId("create-key-name"), "ci");
    await user.click(screen.getByTestId("create-key-submit"));
    await screen.findByTestId("one-time-key");

    expect(await axe(container, AXE_OPTIONS)).toHaveNoViolations();
  });

  it("moves focus to the one-time key notice so it cannot be missed", async () => {
    const user = userEvent.setup();
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
        const url = String(input);
        let body: unknown = {};
        if (url === "/auth/status") body = TENANT;
        else if (url.includes("/user/api-keys") && init.method === "POST") {
          body = { key: { id: "uk_1", name: "ci" }, api_key: "mcpu_one_time" };
        } else if (url.includes("/user/api-keys")) body = { keys: [] };
        return {
          ok: true,
          status: 200,
          json: async () => body,
          text: async () => JSON.stringify(body),
        } as unknown as Response;
      })
    );

    renderApp();
    await user.click(await screen.findByTestId("workspace-tab-keys"));
    await screen.findByTestId("create-key-name");
    await user.type(screen.getByTestId("create-key-name"), "ci");
    await user.click(screen.getByTestId("create-key-submit"));

    const notice = await screen.findByTestId("one-time-key");
    await waitFor(() =>
      expect(within(notice).getByRole("heading", { level: 3 })).toHaveFocus()
    );
  });
});
