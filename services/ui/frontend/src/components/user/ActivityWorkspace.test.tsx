import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ActivityWorkspace } from "./ActivityWorkspace";
import { AppProviders } from "../../providers/AppProviders";
import type { AuthStatus } from "../../api/types";

const TENANT: AuthStatus = {
  authenticated: true,
  principal: { role: "user", subject: "user-1", email: "dev@example.com" },
};

const ADMIN: AuthStatus = {
  authenticated: true,
  principal: { role: "admin", subject: "admin-1" },
};

const USAGE = {
  totals: {
    events: 200,
    allowed: 180,
    denied: 20,
    unique_servers: 2,
    unique_humans: 1,
    unique_agents: 3,
    unique_sessions: 5,
  },
  servers: [
    {
      server: "workspace-assistant",
      namespace: "mcp-team-acme",
      events: 150,
      allowed: 140,
      denied: 10,
      unique_humans: 1,
      unique_agents: 2,
      last_seen: "2026-09-14T09:00:00Z",
    },
  ],
  tools: [
    {
      server: "workspace-assistant",
      tool_name: "create_task",
      human_id: "dev@example.com",
      team_id: "team-1",
      agent_id: "agent-1",
      events: 90,
      denied: 4,
      last_seen: "2026-09-14T09:00:00Z",
    },
  ],
  window_days: 7,
};

const TEAMS = {
  teams: [{ id: "team-1", slug: "acme", name: "Acme", namespace: "mcp-team-acme", role: "member" }],
};

function stub(overrides: { usage?: { status: number; body: unknown }; teams?: { status: number; body: unknown } } = {}) {
  const calls: string[] = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    calls.push(url);
    const route = url.includes("/user/analytics/usage")
      ? overrides.usage ?? { status: 200, body: USAGE }
      : overrides.teams ?? { status: 200, body: TEAMS };
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

function renderActivity(auth: AuthStatus = TENANT) {
  return render(
    <AppProviders>
      <ActivityWorkspace auth={auth} onSignIn={() => {}} />
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

describe("ActivityWorkspace", () => {
  it("keeps membership available during an analytics outage", async () => {
    stub({ usage: { status: 503, body: {} } });
    render(<AppProviders><ActivityWorkspace auth={TENANT} onSignIn={vi.fn()} /></AppProviders>);
    expect(await screen.findByTestId("activity-error")).toBeInTheDocument();
    expect(screen.getByTestId("team-list")).toHaveTextContent("Acme");
  });
  it("prompts signed-out users and fetches nothing", () => {
    const { fetchMock } = stub();

    render(
      <AppProviders>
        <ActivityWorkspace auth={{ authenticated: false }} onSignIn={() => {}} />
      </AppProviders>
    );

    expect(screen.getByTestId("activity-signed-out")).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("hides tenant activity from admins", () => {
    const { fetchMock } = stub();

    renderActivity(ADMIN);

    expect(screen.getByTestId("activity-admin-hidden")).toBeInTheDocument();
    expect(screen.queryByTestId("usage-summary")).not.toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("shows loading then usage, tools, and teams", async () => {
    stub();

    renderActivity();
    expect(screen.getByTestId("activity-loading")).toBeInTheDocument();

    await screen.findByTestId("usage-summary");
    const summary = screen.getByTestId("usage-summary");
    expect(summary).toHaveTextContent("200");
    expect(summary).toHaveTextContent("10%");

    expect(screen.getAllByTestId("server-usage-row")).toHaveLength(1);
    expect(screen.getAllByTestId("tool-usage-row")).toHaveLength(1);
    expect(screen.getByTestId("tool-usage-table")).toHaveTextContent("create_task");

    await screen.findByTestId("team-list");
    expect(screen.getByTestId("team-list")).toHaveTextContent("Acme");
    expect(screen.getByTestId("team-list")).toHaveTextContent("mcp-team-acme");
  });

  it("reads usage through the session proxy with a window filter", async () => {
    const { calls } = stub();

    renderActivity();
    await screen.findByTestId("usage-summary");

    expect(
      calls.some((url) => url.startsWith("/api/ui/v1/user/analytics/usage?") && url.includes("window_days=7"))
    ).toBe(true);
    expect(calls).toContain("/api/ui/v1/runtime/teams");
  });

  it("refetches when the time range changes", async () => {
    const user = userEvent.setup();
    const { calls } = stub();

    renderActivity();
    await screen.findByTestId("usage-summary");

    await user.selectOptions(screen.getByTestId("activity-window-filter"), "30");

    await waitFor(() =>
      expect(calls.some((url) => url.includes("window_days=30"))).toBe(true)
    );
  });

  it("scopes the request when a server is selected", async () => {
    const user = userEvent.setup();
    const { calls } = stub();

    renderActivity();
    await screen.findByTestId("usage-summary");

    await user.selectOptions(screen.getByTestId("activity-server-filter"), "workspace-assistant");

    await waitFor(() =>
      expect(calls.some((url) => url.includes("server=workspace-assistant"))).toBe(true)
    );
  });

  it("renders empty tables when there is no activity", async () => {
    stub({
      usage: {
        status: 200,
        body: { ...USAGE, servers: [], tools: [], totals: { ...USAGE.totals, events: 0, denied: 0 } },
      },
    });

    renderActivity();

    expect(await screen.findByTestId("server-usage-empty")).toBeInTheDocument();
    expect(screen.getByTestId("tool-usage-empty")).toBeInTheDocument();
    expect(screen.getByTestId("usage-summary")).toHaveTextContent("0%");
  });

  it("renders an error state when the analytics service is unavailable", async () => {
    stub({ usage: { status: 502, body: { error: "upstream_error" } } });

    renderActivity();

    const error = await screen.findByTestId("activity-error", {}, { timeout: 5000 });
    expect(error).toHaveAttribute("role", "alert");
  });

  it("renders a session-expired state on 401", async () => {
    stub({ usage: { status: 401, body: { error: "unauthorized" } } });

    renderActivity();

    expect(
      await screen.findByTestId("activity-unauthorized", {}, { timeout: 5000 })
    ).toHaveTextContent("Your session expired.");
  });

  it("keeps usage visible when only the team read fails", async () => {
    stub({ teams: { status: 500, body: { error: "boom" } } });

    renderActivity();
    await screen.findByTestId("usage-summary");

    expect(await screen.findByTestId("teams-error", {}, { timeout: 5000 })).toBeInTheDocument();
    expect(screen.getByTestId("usage-summary")).toBeInTheDocument();
  });

  it("renders an empty team state for a user with no teams", async () => {
    stub({ teams: { status: 200, body: { teams: [] } } });

    renderActivity();

    const empty = await screen.findByTestId("teams-empty");
    expect(within(empty).getByText("You are not a member of any team.")).toBeInTheDocument();
  });
});
