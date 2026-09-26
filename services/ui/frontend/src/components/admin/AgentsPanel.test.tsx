import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { AgentsPanel } from "./AgentsPanel";
import { AppProviders } from "../../providers/AppProviders";

function setup() {
  let status = "active";
  const agent = () => ({ id: "agt_01arz3ndektsv4rrffq69g5fav", team_id: "team-acme", team_slug: "acme", name: "Ops Agent", status });
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.endsWith("/runtime/teams")) {
      return { ok: true, status: 200, json: async () => ({ teams: [{ id: "team-acme", slug: "acme", name: "Acme", namespace: "mcp-team-acme" }] }) } as unknown as Response;
    }
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
  render(<AppProviders><AgentsPanel onSignIn={() => {}} /></AppProviders>);
  return { fetchMock };
}

beforeEach(() => { delete window.MCP_API_BASE; });
afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); });

describe("AgentsPanel", () => {
  it("creates an agent and deactivation revokes its active lifecycle", async () => {
    const user = userEvent.setup();
    const { fetchMock } = setup();

    await screen.findByText("Ops Agent");
    await user.type(screen.getByTestId("agent-create-name"), "New Agent");
    await user.click(screen.getByTestId("agent-create-submit"));
    await screen.findByTestId("agent-action-notice");
    expect(fetchMock).toHaveBeenCalledWith(expect.stringContaining("/runtime/teams/acme/agents"), expect.objectContaining({ method: "POST" }));

    await user.click(screen.getByTestId("agent-deactivate"));
    await user.click(screen.getByTestId("agent-confirm-yes"));
    expect(await screen.findByTestId("agent-reactivate")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByText("inactive")).toBeInTheDocument());
  });
});
