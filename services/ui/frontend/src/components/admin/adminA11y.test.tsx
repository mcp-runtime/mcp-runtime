import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { axe } from "vitest-axe";

import { AdminWorkspace } from "./AdminWorkspace";
import { AppProviders } from "../../providers/AppProviders";
import { visibleWorkspaceTabs } from "../WorkspaceNavigation";
import type { AuthStatus } from "../../api/types";

// Colour contrast needs real layout, which jsdom does not provide; the browser
// QA pass covers it.
const AXE_OPTIONS = { rules: { "color-contrast": { enabled: false } } };

const ADMIN: AuthStatus = {
  authenticated: true,
  principal: { role: "admin", email: "admin@mcpruntime.org" },
};

const PAYLOADS: Record<string, unknown> = {
  "/runtime/grants": {
    grants: [
      {
        name: "acme-readonly",
        namespace: "mcp-servers",
        serverRef: { name: "workspace-assistant" },
        subject: { humanID: "alice@example.com" },
        maxTrust: "low",
        allowedSideEffects: ["read"],
        disabled: false,
      },
    ],
  },
  "/runtime/sessions": {
    sessions: [
      {
        name: "sess-1",
        namespace: "mcp-servers",
        serverRef: { name: "workspace-assistant" },
        subject: { humanID: "alice@example.com" },
        consentedTrust: "low",
        revoked: false,
      },
    ],
  },
  "/runtime/teams": {
    teams: [{ id: "t-1", slug: "verify", name: "Verify Team", namespace: "mcp-team-verify" }],
  },
  "/runtime/components": {
    components: [
      {
        key: "operator",
        display: "Operator",
        namespace: "mcp-runtime",
        kind: "Deployment",
        resource: "op",
        status: "Ready",
        ready: "2/2",
      },
    ],
  },
  "/admin/operations": {
    users: [
      {
        id: "u-1",
        email: "admin@mcpruntime.org",
        role: "admin",
        login_count: 1,
        failed_action_count: 0,
        registry_credentials: 0,
        api_keys: 0,
      },
    ],
    audit_logs: [{ action: "login", resource: "session", status: "success" }],
    images: [{ image_ref: "registry/demo:1", action: "publish", status: "success" }],
  },
  "/events": { events: [] },
};

function stub() {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      const key = Object.keys(PAYLOADS).find((path) => url.includes(path));
      return {
        ok: true,
        status: 200,
        json: async () => (key ? PAYLOADS[key] : {}),
      } as unknown as Response;
    })
  );
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

describe("admin navigation gating", () => {
  it("lists the admin tab only for an admin role", () => {
    expect(visibleWorkspaceTabs(ADMIN).map((tab) => tab.id)).toEqual(["servers", "agents", "access", "admin"]);
    const user = { authenticated: true, principal: { role: "user" } } as AuthStatus;
    expect(visibleWorkspaceTabs(user).map((tab) => tab.id)).not.toContain("admin");
  });
});

describe("admin accessibility", () => {
  it("has no detectable violations on operations", async () => {
    const user = userEvent.setup();
    stub();
    const { container } = renderAdmin(ADMIN);
    await screen.findByTestId("teams-table");
    await user.click(screen.getByTestId("admin-section-operations"));
    await screen.findByTestId("operations-users-table");

    expect(await axe(container, AXE_OPTIONS)).toHaveNoViolations();
  });

  it("has no detectable violations on platform health", async () => {
    const user = userEvent.setup();
    stub();
    const { container } = renderAdmin(ADMIN);
    await screen.findByTestId("teams-table");
    await user.click(screen.getByTestId("admin-section-platform"));
    await screen.findByTestId("platform-components");

    expect(await axe(container, AXE_OPTIONS)).toHaveNoViolations();
  });

  it("has no detectable violations on the non-admin refusal", async () => {
    stub();
    const { container } = renderAdmin({ authenticated: true, principal: { role: "user" } });

    expect(await axe(container, AXE_OPTIONS)).toHaveNoViolations();
  });
});
