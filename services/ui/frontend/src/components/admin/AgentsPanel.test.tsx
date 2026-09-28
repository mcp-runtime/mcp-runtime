import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { AgentsPanel } from "./AgentsPanel";
import { AppProviders } from "../../providers/AppProviders";

function setup(options: { role?: "admin" | "user"; teamRole?: "owner" | "member"; accessNamespace?: string } = {}) {
  let status = "active";
  const agent = () => ({ id: "agt_01arz3ndektsv4rrffq69g5fav", team_id: "team-acme", team_slug: "acme", name: "Ops Agent", status });
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.endsWith("/runtime/teams")) {
      return { ok: true, status: 200, json: async () => ({ teams: [{ id: "team-acme", slug: "acme", name: "Acme", namespace: "mcp-team-acme", role: options.teamRole || "owner" }] }) } as unknown as Response;
    }
    if (url.endsWith("/runtime/agents/agt_01arz3ndektsv4rrffq69g5fav") && (!init?.method || init.method === "GET")) {
      const namespace = options.accessNamespace;
      return { ok: true, status: 200, json: async () => ({ agent: agent(), grants: namespace ? [{ name: "agent-grant", namespace, disabled: false }] : [], sessions: namespace ? [{ name: "agent-session", namespace, revoked: false }] : [], can_manage: options.role !== "user" || options.teamRole === "owner" }) } as unknown as Response;
    }
    if (url.includes("/runtime/namespaces")) return { ok: true, status: 200, json: async () => ({ namespaces: [] }) } as unknown as Response;
    if (url.includes("/runtime/servers")) return { ok: true, status: 200, json: async () => ({ servers: [] }) } as unknown as Response;
    if (url.includes("/runtime/tools")) return { ok: true, status: 200, json: async () => ({ tools: [] }) } as unknown as Response;
    if (url.includes("/runtime/teams/acme/agents") && (!init?.method || init.method === "GET")) {
      return { ok: true, status: 200, json: async () => ({ agents: [agent()] }) } as unknown as Response;
    }
    if (url.endsWith("/runtime/teams/acme/agents") && init?.method === "POST") {
      return { ok: true, status: 201, json: async () => ({ agent: agent() }) } as unknown as Response;
    }
    if (url.endsWith("/runtime/agents/agt_01arz3ndektsv4rrffq69g5fav/deactivate")) {
      status = "inactive";
      return { ok: true, status: 200, json: async () => ({ agent: agent() }) } as unknown as Response;
    }
    throw new Error(`unexpected request: ${url} ${init?.method ?? "GET"}`);
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<AppProviders><AgentsPanel auth={{ authenticated: true, principal: { role: options.role || "admin", subject: "user-one" } }} onSignIn={() => {}} /></AppProviders>);
  return { fetchMock };
}

beforeEach(() => { delete window.MCP_API_BASE; });
afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); });

describe("AgentsPanel", () => {
  it("creates an agent and deactivation revokes its active lifecycle", async () => {
    const user = userEvent.setup();
    const { fetchMock } = setup();

    await screen.findByText("Ops Agent");
    await user.selectOptions(screen.getByTestId("agent-team-select"), "acme");
    await user.type(screen.getByTestId("agent-create-name"), "New Agent");
    await user.click(screen.getByTestId("agent-create-submit"));
    await screen.findByTestId("agent-action-notice");
    expect(fetchMock).toHaveBeenCalledWith(expect.stringContaining("/runtime/teams/acme/agents"), expect.objectContaining({ method: "POST" }));

    await user.click(screen.getByTestId("agent-deactivate"));
    await user.click(screen.getByTestId("agent-confirm-yes"));
    expect(await screen.findByTestId("agent-reactivate")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByText("inactive")).toBeInTheDocument());
  });

  it("shows a member only scoped read actions and no grant controls", async () => {
    const user = userEvent.setup();
    const { fetchMock } = setup({ role: "user", teamRole: "member" });
    await screen.findByTestId("agent-open");
    expect(screen.queryByTestId("agent-create-name")).not.toBeInTheDocument();
    expect(screen.queryByTestId("agent-deactivate")).not.toBeInTheDocument();
    await user.click(screen.getByTestId("agent-open"));
    expect(await screen.findByTestId("agent-detail")).toBeInTheDocument();
    expect(screen.queryByTestId("agent-grant-access")).not.toBeInTheDocument();
    expect(screen.getByText("Connect this agent")).toBeInTheDocument();
    expect(fetchMock.mock.calls.some((call) => String(call[0]).includes("/runtime/agents/agt_"))).toBe(true);
  });

  it("preselects the agent and team in the grant form", async () => {
    const user = userEvent.setup();
    setup();
    await user.click(await screen.findByTestId("agent-open"));
    await user.click(await screen.findByTestId("agent-grant-access"));
    expect(await screen.findByTestId("grant-create-form")).toBeInTheDocument();
    expect(screen.getByTestId("grant-fixed-agent")).toHaveTextContent("acme");
    expect(screen.getByTestId("grant-fixed-agent")).toHaveTextContent("agt_01arz3ndektsv4rrffq69g5fav");
  });

  it("limits an owner's access actions to their namespace", async () => {
    const user = userEvent.setup();
    setup({ role: "user", teamRole: "owner", accessNamespace: "mcp-team-other" });
    await user.click(await screen.findByTestId("agent-open"));
    expect(await screen.findByText("agent-grant")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Disable" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Revoke" })).not.toBeInTheDocument();
  });
});
