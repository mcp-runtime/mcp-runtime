import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { App } from "./App";
import { AppProviders } from "./providers/AppProviders";

function renderApp() {
  return render(
    <AppProviders>
      <App />
    </AppProviders>
  );
}

async function openAccountSignIn(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByTestId("account-trigger"));
  await user.click(await screen.findByTestId("account-menu-signin"));
}

async function signOut(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByTestId("account-trigger"));
  await user.click(await screen.findByTestId("logout-button"));
}

type Route = { status: number; body: unknown };

function stubRoutes(routes: Record<string, Route | Route[]>) {
  const calls: Array<{ url: string; init: RequestInit }> = [];
  const pending = new Map<string, Route[]>();
  for (const [key, value] of Object.entries(routes)) {
    pending.set(key, Array.isArray(value) ? [...value] : [value]);
  }

  const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input);
    calls.push({ url, init });
    const match = [...pending.keys()].find((key) => url.startsWith(key));
    const queue = match ? pending.get(match) : undefined;
    const route = queue && (queue.length > 1 ? queue.shift() : queue[0]);
    if (!route) {
      return { ok: false, status: 404, text: async () => "not stubbed" } as unknown as Response;
    }
    return {
      ok: route.status < 400,
      status: route.status,
      json: async () => route.body,
      text: async () => JSON.stringify(route.body),
    } as unknown as Response;
  });

  vi.stubGlobal("fetch", fetchMock);
  return { fetchMock, calls };
}

const SIGNED_OUT: Route = { status: 200, body: { authenticated: false } };
const ADMIN: Route = {
  status: 200,
  body: { authenticated: true, principal: { role: "admin", email: "admin@mcpruntime.org" } },
};
const EMPTY_CATALOG = {
  "/api/ui/v1/runtime/namespaces": { status: 200, body: { namespaces: [] } },
  "/api/ui/v1/runtime/servers": { status: 200, body: { servers: [] } },
  "/api/ui/v1/runtime/tools": { status: 200, body: { tools: [] } },
};

beforeEach(() => {
  delete window.MCP_API_BASE;
  window.localStorage.removeItem("mcp-theme");
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  window.localStorage.removeItem("mcp-theme");
});

describe("App", () => {
  it("renders the signed-out shell with a sign-in action", async () => {
    const user = userEvent.setup();
    stubRoutes({ "/auth/status": SIGNED_OUT });

    renderApp();

    expect(await screen.findByTestId("catalog-signed-out")).toBeInTheDocument();
    expect(screen.getByTestId("account-trigger")).toHaveTextContent("Account");
    await user.click(screen.getByTestId("account-trigger"));
    expect(screen.getByTestId("account-menu-signin")).toBeInTheDocument();
    expect(
      screen.getByRole("heading", { name: "See what’s running. Govern every tool call." })
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Read the platform docs/ })).toHaveAttribute(
      "href",
      "https://mcpruntime.org/docs/"
    );
  });

  it("switches themes and persists the preference", async () => {
    const user = userEvent.setup();
    stubRoutes({ "/auth/status": SIGNED_OUT });

    renderApp();

    const toggle = await screen.findByTestId("theme-toggle");
    expect(toggle).toHaveAttribute("aria-label", "Switch to light mode");
    expect(document.documentElement.dataset.theme).toBe("dark");

    await user.click(toggle);

    expect(toggle).toHaveAttribute("aria-label", "Switch to dark mode");
    expect(document.documentElement.dataset.theme).toBe("light");
    expect(window.localStorage.getItem("mcp-theme")).toBe("light");
  });

  it("signs in through the UI session endpoint and loads the catalog", async () => {
    const user = userEvent.setup();
    const { fetchMock, calls } = stubRoutes({
      "/auth/status": SIGNED_OUT,
      "/auth/login": ADMIN,
      ...EMPTY_CATALOG,
    });

    renderApp();
    await openAccountSignIn(user);

    await user.type(screen.getByTestId("login-email"), "admin@mcpruntime.org");
    await user.type(screen.getByTestId("login-password"), "admin@123");
    await user.click(screen.getByTestId("login-submit"));

    await user.click(await screen.findByTestId("account-trigger"));
    expect(await screen.findByText("Administrator")).toBeInTheDocument();
    expect(screen.getAllByText("admin@mcpruntime.org")).toHaveLength(2);
    expect(screen.getByTestId("logout-button")).toBeInTheDocument();

    const loginCall = calls.find((call) => call.url === "/auth/login");
    expect(loginCall?.init.credentials).toBe("same-origin");
    expect(new Headers(loginCall?.init.headers).get("authorization")).toBeNull();
    expect(String(loginCall?.init.body)).not.toContain("api_key");

    await waitFor(() =>
      expect(fetchMock.mock.calls.map((call) => String(call[0]))).toContain(
        "/api/ui/v1/runtime/servers"
      )
    );
  });

  it("signs in with an API key without exposing it after submit", async () => {
    const user = userEvent.setup();
    const { calls } = stubRoutes({
      "/auth/status": SIGNED_OUT,
      "/auth/login": ADMIN,
      ...EMPTY_CATALOG,
    });

    renderApp();
    await openAccountSignIn(user);
    // Account and API-key credentials are separate modes now.
    await user.click(screen.getByTestId("signin-mode-api-key"));
    await user.type(screen.getByTestId("login-api-key"), "ui-key");
    await user.click(screen.getByTestId("login-submit"));

    await user.click(await screen.findByTestId("account-trigger"));
    await screen.findByTestId("logout-button");
    const loginCall = calls.find((call) => call.url === "/auth/login");
    expect(String(loginCall?.init.body)).toBe(JSON.stringify({ api_key: "ui-key" }));
    expect(document.body.innerHTML).not.toContain("ui-key");
  });

  it("signs in with a Google credential", async () => {
    const user = userEvent.setup();
    window.MCP_GOOGLE_CLIENT_ID = "test-client-id";
    const initialize = vi.fn();
    window.google = { accounts: { id: { initialize, renderButton: vi.fn() } } };
    const { calls } = stubRoutes({
      "/auth/status": SIGNED_OUT,
      "/auth/login": ADMIN,
      ...EMPTY_CATALOG,
    });

    renderApp();
    await openAccountSignIn(user);
    await waitFor(() => expect(initialize).toHaveBeenCalledTimes(1));

    const { callback } = initialize.mock.calls[0][0];
    callback({ credential: "google-id-token" });

    await user.click(await screen.findByTestId("account-trigger"));
    await screen.findByTestId("logout-button");
    const loginCall = calls.find((call) => call.url === "/auth/login");
    expect(String(loginCall?.init.body)).toBe(JSON.stringify({ id_token: "google-id-token" }));

    delete window.google;
    delete window.MCP_GOOGLE_CLIENT_ID;
  });

  it("shows a sign-in error without leaving the form", async () => {
    const user = userEvent.setup();
    stubRoutes({
      "/auth/status": SIGNED_OUT,
      "/auth/login": { status: 401, body: { error: "unauthorized" } },
    });

    renderApp();
    await openAccountSignIn(user);
    await user.type(screen.getByTestId("login-email"), "nobody@example.com");
    await user.type(screen.getByTestId("login-password"), "wrong");
    await user.click(screen.getByTestId("login-submit"));

    expect(await screen.findByTestId("login-error")).toHaveTextContent(
      "That email, password, or API key was not accepted."
    );
    expect(screen.getByTestId("login-form")).toBeInTheDocument();
  });

  it("returns to the signed-out state after sign out", async () => {
    const user = userEvent.setup();
    const { calls } = stubRoutes({
      "/auth/status": ADMIN,
      "/auth/logout": { status: 200, body: { authenticated: false } },
      ...EMPTY_CATALOG,
    });

    renderApp();
    await signOut(user);

    await waitFor(() =>
      expect(screen.getByTestId("account-trigger")).toHaveTextContent("Account")
    );
    expect(calls.some((call) => call.url === "/auth/logout" && call.init.method === "POST")).toBe(
      true
    );
  });

  it("no longer renders the nested legacy dashboard anywhere", async () => {
    stubRoutes({ "/auth/status": ADMIN, ...EMPTY_CATALOG });

    renderApp();
    await screen.findByTestId("workspace-tab-servers");

    expect(screen.queryByTestId("workspace-tab-legacy")).not.toBeInTheDocument();
    expect(screen.queryByTitle("MCP Sentinel dashboard")).not.toBeInTheDocument();
    expect(document.querySelector("iframe")).toBeNull();
  });

  it("returns to Servers after signing in from a deep link", async () => {
    const user = userEvent.setup();
    window.location.hash = "#/keys";
    stubRoutes({
      "/auth/status": SIGNED_OUT,
      "/auth/login": ADMIN,
      ...EMPTY_CATALOG,
    });

    renderApp();
    await openAccountSignIn(user);
    await user.type(screen.getByTestId("login-email"), "admin@mcpruntime.org");
    await user.type(screen.getByTestId("login-password"), "admin@123");
    await user.click(screen.getByTestId("login-submit"));

    await waitFor(() => expect(window.location.hash).toBe("#/servers"));
  });
});
