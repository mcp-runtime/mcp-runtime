import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { axe } from "vitest-axe";

import { AccessWorkspace } from "./AccessWorkspace";
import { AppProviders } from "../../providers/AppProviders";
import type { AuthStatus } from "../../api/types";

// Access control is served to any authenticated principal (the backend
// registers /runtime/grants and /runtime/sessions with auth(), not
// adminOnly()), so this suite covers a tenant session as thoroughly as an
// admin one.

const AXE_OPTIONS = { rules: { "color-contrast": { enabled: false } } };

const ADMIN: AuthStatus = {
  authenticated: true,
  principal: { role: "admin", email: "admin@mcpruntime.org" },
};
const TENANT: AuthStatus = {
  authenticated: true,
  principal: { role: "user", subject: "user-1", email: "dev@example.com" },
};
const SIGNED_OUT: AuthStatus = { authenticated: false };

const NAMESPACES = {
  namespaces: [{ namespace: "mcp-team-acme" }, { namespace: "mcp-shared" }],
};

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
      expiresAt: "2099-10-01T00:00:00Z",
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

function stubAccessApi(overrides: Record<string, { status?: number; body?: unknown }> = {}) {
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
    const body = url.includes("/runtime/namespaces")
      ? NAMESPACES
      : url.includes("/runtime/grants")
        ? GRANTS
        : url.includes("/runtime/sessions")
          ? SESSIONS
          : url.includes("/events")
            ? EVENTS
            : url.includes("/runtime/servers")
              ? { servers: [] }
              : url.includes("/runtime/tools")
                ? { tools: [] }
                : {};
    return { ok: true, status: 200, json: async () => body } as unknown as Response;
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function renderAccess(auth: AuthStatus) {
  return render(
    <AppProviders>
      <AccessWorkspace auth={auth} onSignIn={() => {}} />
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

describe("AccessWorkspace gating", () => {
  it("prompts a signed-out visitor and issues no request", () => {
    const fetchMock = stubAccessApi();

    renderAccess(SIGNED_OUT);

    expect(screen.getByTestId("access-signed-out")).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("gives a tenant user the same read access an admin gets", async () => {
    stubAccessApi();

    renderAccess(TENANT);

    expect(await screen.findByTestId("grants-table")).toBeInTheDocument();
    expect(screen.getByTestId("sessions-table")).toBeInTheDocument();
  });

  it("also gives an admin full access", async () => {
    stubAccessApi();

    renderAccess(ADMIN);

    expect(await screen.findByTestId("grants-table")).toBeInTheDocument();
  });

  it("defaults a tenant session to their first visible namespace", async () => {
    const fetchMock = stubAccessApi();

    renderAccess(TENANT);
    await screen.findByTestId("grants-table");

    // An empty namespace 403s for a non-admin, so the first grants read must
    // already carry a concrete scope.
    const grantReads = fetchMock.mock.calls
      .map((call) => String(call[0]))
      .filter((url) => url.includes("/runtime/grants"));
    expect(grantReads.length).toBeGreaterThan(0);
    for (const url of grantReads) {
      expect(url).toContain("namespace=mcp-team-acme");
    }
  });

  it("shows a normal user read-only access records", async () => {
    const fetchMock = stubAccessApi();
    renderAccess(TENANT);
    await screen.findByTestId("grants-table");
    expect(screen.queryByTestId("grant-create-toggle")).not.toBeInTheDocument();
    expect(screen.queryByTestId("session-create-toggle")).not.toBeInTheDocument();
    expect(screen.queryByTestId("grant-toggle")).not.toBeInTheDocument();
    expect(screen.queryByTestId("session-toggle")).not.toBeInTheDocument();
    expect(fetchMock.mock.calls.filter((call) => (call[1] as RequestInit)?.method)).toHaveLength(0);
  });

  it("leaves an admin scoped to every namespace by default", async () => {
    const fetchMock = stubAccessApi();

    renderAccess(ADMIN);
    await screen.findByTestId("grants-table");

    const grantReads = fetchMock.mock.calls
      .map((call) => String(call[0]))
      .filter((url) => url.includes("/runtime/grants"));
    expect(grantReads.some((url) => !url.includes("namespace="))).toBe(true);
  });
});

describe("AccessWorkspace reads", () => {
  it("reads through the session proxy without credential headers", async () => {
    const fetchMock = stubAccessApi();

    renderAccess(ADMIN);
    await screen.findByTestId("grants-table");

    const urls = fetchMock.mock.calls.map((call) => String(call[0]));
    expect(urls.some((url) => url.startsWith("/api/ui/v1/runtime/grants"))).toBe(true);
    expect(urls.some((url) => url.startsWith("/api/ui/v1/runtime/sessions"))).toBe(true);
    for (const call of fetchMock.mock.calls) {
      const init = call[1] as RequestInit;
      expect(init?.credentials).toBe("same-origin");
      const headers = new Headers(init?.headers);
      expect(headers.get("authorization")).toBeNull();
      expect(headers.get("x-api-key")).toBeNull();
    }
  });

  it("renders grant and session rows with status and subject", async () => {
    stubAccessApi();

    renderAccess(ADMIN);
    const table = await screen.findByTestId("grants-table");

    expect(within(table).getByText("alice@example.com / acme")).toBeInTheDocument();
    expect(within(table).getByText("Disabled")).toBeInTheDocument();
    expect(screen.getByTestId("access-stats")).toHaveTextContent("1");
    expect(
      within(screen.getByTestId("sessions-table")).getByText("agent-7", { exact: false })
    ).toBeInTheDocument();
  });

  it("filters grants and sessions by term", async () => {
    const user = userEvent.setup();
    stubAccessApi();

    renderAccess(ADMIN);
    await screen.findByTestId("grants-table");

    await user.type(screen.getByTestId("access-filter"), "retired");
    expect(screen.getAllByTestId("grants-table-row")).toHaveLength(1);

    await user.clear(screen.getByTestId("access-filter"));
    await user.type(screen.getByTestId("access-filter"), "zzz-no-match");
    expect(screen.getByTestId("grants-table-empty")).toHaveTextContent(
      "No grants match this filter."
    );
  });

  it("shows an empty state when no grants exist", async () => {
    stubAccessApi({
      "/runtime/grants": { body: { grants: [] } },
      "/runtime/sessions": { body: { sessions: [] } },
    });

    renderAccess(ADMIN);

    expect(await screen.findByTestId("grants-table-empty")).toHaveTextContent(
      "No access grants found."
    );
  });

  it("shows an error state when the grant read fails", async () => {
    stubAccessApi({ "/runtime/grants": { status: 500, body: { error: "boom" } } });

    renderAccess(ADMIN);

    const error = await screen.findByTestId("grants-error", {}, { timeout: 5000 });
    expect(error).toHaveTextContent("Access grants could not be loaded.");
    expect(error).toHaveAttribute("role", "alert");
  });

  it("shows a session-expired state on 401", async () => {
    stubAccessApi({ "/runtime/grants": { status: 401, body: { error: "unauthorized" } } });

    renderAccess(ADMIN);

    expect(await screen.findByTestId("grants-unauthorized")).toHaveTextContent(
      "Your session expired."
    );
  });
});

describe("AccessWorkspace mutations", () => {
  it("writes only after an explicit confirmation", async () => {
    const user = userEvent.setup();
    const fetchMock = stubAccessApi();

    renderAccess(ADMIN);
    await screen.findByTestId("grants-table");

    await user.click(screen.getAllByTestId("grant-toggle")[0]);
    const dialog = await screen.findByTestId("access-confirm");
    expect(dialog).toHaveTextContent('Disable grant "acme-readonly"?');
    expect(fetchMock.mock.calls.filter((call) => (call[1] as RequestInit)?.method)).toHaveLength(0);

    await user.click(screen.getByTestId("access-confirm-yes"));

    await waitFor(() => {
      const writes = fetchMock.mock.calls.filter((call) => (call[1] as RequestInit)?.method);
      expect(writes.map((call) => (call[1] as RequestInit).method)).toContain("PATCH");
    });
  });

  it("cancels without writing", async () => {
    const user = userEvent.setup();
    const fetchMock = stubAccessApi();

    renderAccess(ADMIN);
    await screen.findByTestId("grants-table");

    await user.click(screen.getAllByTestId("session-toggle")[0]);
    await screen.findByTestId("access-confirm");
    await user.click(screen.getByTestId("access-confirm-cancel"));

    expect(screen.queryByTestId("access-confirm")).not.toBeInTheDocument();
    expect(fetchMock.mock.calls.filter((call) => (call[1] as RequestInit)?.method)).toHaveLength(0);
  });
});

describe("AccessWorkspace drill-down", () => {
  it("opens a grant detail and returns via the back path", async () => {
    const user = userEvent.setup();
    stubAccessApi();

    renderAccess(ADMIN);
    await screen.findByTestId("grants-table");

    await user.click(screen.getAllByTestId("grant-drilldown")[0]);

    const detail = await screen.findByTestId("access-detail");
    expect(detail).toHaveTextContent("acme-readonly");
    expect(screen.getByTestId("access-detail-kicker")).toHaveTextContent(
      "mcp-servers / MCPAccessGrant"
    );

    await user.click(screen.getByTestId("access-detail-back"));
    await waitFor(() => expect(screen.getByTestId("grants-table")).toBeInTheDocument());
  });

  it("opens a session detail labelled as an agent session", async () => {
    const user = userEvent.setup();
    stubAccessApi();

    renderAccess(ADMIN);
    await screen.findByTestId("sessions-table");

    await user.click(screen.getAllByTestId("session-drilldown")[0]);

    expect(await screen.findByTestId("access-detail-kicker")).toHaveTextContent(
      "mcp-servers / MCPAgentSession"
    );
  });

  it("reports an analytics outage as an outage", async () => {
    const user = userEvent.setup();
    stubAccessApi({ "/events": { status: 502, body: { error: "upstream_error" } } });

    renderAccess(ADMIN);
    await screen.findByTestId("grants-table");
    await user.click(screen.getAllByTestId("grant-drilldown")[0]);

    const error = await screen.findByTestId("access-activity-error", {}, { timeout: 5000 });
    expect(error).toHaveTextContent("Activity is unavailable.");
    expect(error).toHaveTextContent("analytics service");
  });

  it("tells a non-admin the decision log needs admin access, not that analytics is down", async () => {
    const user = userEvent.setup();
    stubAccessApi({ "/events": { status: 403, body: { error: "forbidden" } } });

    renderAccess(TENANT);
    await screen.findByTestId("grants-table");
    await user.click(screen.getAllByTestId("grant-drilldown")[0]);

    const forbidden = await screen.findByTestId("access-activity-forbidden", {}, { timeout: 5000 });
    expect(forbidden).toHaveTextContent("Admin access required.");
    expect(screen.queryByTestId("access-activity-error")).not.toBeInTheDocument();
  });
});

describe("AccessWorkspace accessibility", () => {
  it("has no detectable violations for a tenant user", async () => {
    stubAccessApi();

    const { container } = renderAccess(TENANT);
    await screen.findByTestId("grants-table");

    expect(await axe(container, AXE_OPTIONS)).toHaveNoViolations();
  });

  it("has no detectable violations on the grant drill-down", async () => {
    const user = userEvent.setup();
    stubAccessApi();

    const { container } = renderAccess(ADMIN);
    await screen.findByTestId("grants-table");
    await user.click(screen.getAllByTestId("grant-drilldown")[0]);
    await screen.findByTestId("access-detail");

    expect(await axe(container, AXE_OPTIONS)).toHaveNoViolations();
  });
});
