import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { AdminWorkspace } from "./AdminWorkspace";
import { AppProviders } from "../../providers/AppProviders";
import type { AuthStatus } from "../../api/types";

const ADMIN: AuthStatus = {
  authenticated: true,
  principal: { role: "admin", email: "admin@mcpruntime.org" },
};
const TENANT: AuthStatus = {
  authenticated: true,
  principal: { role: "user", email: "test@mcpruntime.org" },
};
const SIGNED_OUT: AuthStatus = { authenticated: false };

const GRANTS = {
  grants: [
    {
      name: "acme-readonly",
      namespace: "mcp-servers",
      serverRef: { name: "workspace-assistant" },
      subject: { humanID: "alice@example.com", teamID: "acme" },
      maxTrust: "low",
      allowedSideEffects: ["read"],
      disabled: false,
    },
    {
      name: "acme-retired",
      namespace: "mcp-servers",
      serverRef: { name: "workspace-assistant" },
      subject: { humanID: "bob@example.com" },
      maxTrust: "high",
      allowedSideEffects: ["read", "write"],
      disabled: true,
    },
  ],
};

const SESSIONS = {
  sessions: [
    {
      name: "sess-1",
      namespace: "mcp-servers",
      serverRef: { name: "workspace-assistant" },
      subject: { humanID: "alice@example.com", agentID: "agent-7" },
      consentedTrust: "low",
      revoked: false,
      expiresAt: "2026-10-01T00:00:00Z",
    },
  ],
};

const TEAMS = {
  teams: [{ id: "t-1", slug: "verify", name: "Verify Team", namespace: "mcp-team-verify" }],
};

const COMPONENTS = {
  components: [
    {
      key: "operator",
      display: "Operator",
      namespace: "mcp-runtime",
      kind: "Deployment",
      resource: "mcp-runtime-operator",
      status: "Ready",
      ready: "2/2",
    },
    {
      key: "clickhouse",
      display: "ClickHouse",
      namespace: "mcp-observability",
      kind: "StatefulSet",
      resource: "clickhouse",
      status: "NotReady",
      ready: "0/1",
      message: "CrashLoopBackOff",
    },
  ],
};

const OPERATIONS = {
  users: [
    {
      id: "u-1",
      email: "admin@mcpruntime.org",
      role: "admin",
      login_count: 25,
      failed_action_count: 0,
      registry_credentials: 1,
      api_keys: 2,
    },
  ],
  audit_logs: [
    {
      action: "server_publish",
      resource: "workspace-assistant",
      namespace: "mcp-servers",
      status: "success",
      created_at: "2026-09-15T10:00:00Z",
    },
  ],
  images: [
    {
      image_ref: "registry/demo:1",
      action: "publish",
      status: "success",
      created_at: "2026-09-15T10:00:00Z",
    },
  ],
};

const EVENTS = {
  events: [
    {
      timestamp: new Date().toISOString(),
      namespace: "mcp-servers",
      tool_name: "add",
      decision: "allow",
      payload: { matched_grant: "acme-readonly", matched_grant_namespace: "mcp-servers" },
    },
  ],
};

function stubAdminApi(overrides: Record<string, { status?: number; body?: unknown }> = {}) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    const route = Object.keys(overrides).find((key) => url.includes(key));
    if (route) {
      const { status = 200, body = {} } = overrides[route];
      return {
        ok: status < 400,
        status,
        json: async () => body,
        text: async () => JSON.stringify(body),
      } as unknown as Response;
    }
    const body = url.includes("/runtime/grants")
      ? GRANTS
      : url.includes("/runtime/sessions")
        ? SESSIONS
        : url.includes("/runtime/teams")
          ? TEAMS
          : url.includes("/runtime/components")
            ? COMPONENTS
            : url.includes("/admin/operations")
              ? OPERATIONS
              : url.includes("/events")
                ? EVENTS
                : {};
    return { ok: true, status: 200, json: async () => body } as unknown as Response;
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function renderAdmin(auth: AuthStatus) {
  return render(
    <AppProviders>
      <AdminWorkspace auth={auth} onSignIn={() => {}} />
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

describe("AdminWorkspace role gating", () => {
  it("refuses a signed-out visitor and issues no admin request", () => {
    const fetchMock = stubAdminApi();

    renderAdmin(SIGNED_OUT);

    expect(screen.getByTestId("admin-signed-out")).toBeInTheDocument();
    expect(screen.queryByTestId("admin-section-teams")).not.toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("refuses a non-admin session and issues no admin request", () => {
    const fetchMock = stubAdminApi();

    renderAdmin(TENANT);

    const refusal = screen.getByTestId("admin-forbidden");
    expect(refusal).toHaveTextContent("This workspace is restricted to administrators.");
    expect(refusal).toHaveAttribute("role", "alert");
    // No admin control of any kind is rendered for a tenant user.
    expect(screen.queryByTestId("admin-section-teams")).not.toBeInTheDocument();
    expect(screen.queryByTestId("teams-table")).not.toBeInTheDocument();
    expect(screen.queryByTestId("grafana-link")).not.toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("treats an unknown role as non-admin", () => {
    stubAdminApi();

    renderAdmin({ authenticated: true, principal: {} });

    expect(screen.getByTestId("admin-forbidden")).toBeInTheDocument();
  });

  it("renders the admin sections for an admin principal", async () => {
    stubAdminApi();

    renderAdmin(ADMIN);

    expect(await screen.findByTestId("teams-table")).toBeInTheDocument();
    for (const section of ["teams", "operations", "platform", "analytics"]) {
      expect(screen.getByTestId(`admin-section-${section}`)).toBeInTheDocument();
    }
  });

  it("does not put access control behind the admin gate", () => {
    stubAdminApi();

    renderAdmin(ADMIN);

    // Grants and sessions are served to any authenticated principal, so they
    // live in their own workspace rather than an admin section.
    expect(screen.queryByTestId("admin-section-access")).not.toBeInTheDocument();
  });
});

describe("AdminWorkspace sections", () => {
  it("renders teams", async () => {
    const user = userEvent.setup();
    stubAdminApi();

    renderAdmin(ADMIN);

    const table = await screen.findByTestId("teams-table");
    expect(within(table).getByText("Verify Team")).toBeInTheDocument();
    expect(within(table).getByText("mcp-team-verify")).toBeInTheDocument();
  });

  it("renders operations users, audit trail, and image activity", async () => {
    const user = userEvent.setup();
    stubAdminApi();

    renderAdmin(ADMIN);
    await screen.findByTestId("teams-table");
    await user.click(screen.getByTestId("admin-section-operations"));

    expect(await screen.findByTestId("operations-users-table")).toHaveTextContent(
      "admin@mcpruntime.org"
    );

    // Users, the audit trail, and image activity are separate local sections.
    await user.click(screen.getByTestId("operations-tab-audit"));
    expect(await screen.findByTestId("operations-audit-table")).toHaveTextContent("server_publish");

    await user.click(screen.getByTestId("operations-tab-images"));
    expect(await screen.findByTestId("operations-images-table")).toHaveTextContent("registry/demo:1");
  });

  it("refetches operations scoped to the applied user filter", async () => {
    const user = userEvent.setup();
    const fetchMock = stubAdminApi();

    renderAdmin(ADMIN);
    await screen.findByTestId("teams-table");
    await user.click(screen.getByTestId("admin-section-operations"));
    await screen.findByTestId("operations-users-table");

    await user.type(screen.getByTestId("operations-user-filter"), "alice@example.com");
    await user.click(screen.getByTestId("operations-apply"));

    await waitFor(() => {
      const urls = fetchMock.mock.calls.map((call) => String(call[0]));
      expect(urls.some((url) => url.includes("user=alice%40example.com"))).toBe(true);
    });
  });

  it("opens Grafana Explore from usage analytics for detailed activity", async () => {
    const user = userEvent.setup();
    stubAdminApi();

    renderAdmin(ADMIN);
    await screen.findByTestId("teams-table");
    await user.click(screen.getByTestId("admin-section-analytics"));

    const detailedActivity = await screen.findByTestId("analytics-detailed-activity");
    expect(detailedActivity).toHaveAttribute("href", "/grafana/explore");
    expect(detailedActivity).toHaveAttribute("target", "_blank");
  });

  it("renders platform health and keeps Grafana a plain forward-auth link", async () => {
    const user = userEvent.setup();
    stubAdminApi();

    renderAdmin(ADMIN);
    await screen.findByTestId("teams-table");
    await user.click(screen.getByTestId("admin-section-platform"));

    const grid = await screen.findByTestId("platform-components");
    expect(within(grid).getByText("Operator")).toBeInTheDocument();
    expect(within(grid).getByText("CrashLoopBackOff")).toBeInTheDocument();
    expect(screen.getByTestId("platform-stats")).toHaveTextContent("1");
    // Restart all is separated from the safe read actions.
    expect(screen.getByTestId("restart-all")).toBeInTheDocument();

    // Must stay a direct href so the platform ingress forward-auth still applies.
    expect(screen.getByTestId("grafana-link")).toHaveAttribute("href", "/grafana");
    expect(screen.queryByTestId("prometheus-link")).not.toBeInTheDocument();
  });
});
