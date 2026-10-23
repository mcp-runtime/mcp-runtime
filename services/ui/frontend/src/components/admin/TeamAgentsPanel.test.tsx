import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { axe } from "vitest-axe";

import { AppProviders } from "../../providers/AppProviders";
import { TeamAgentsPanel } from "./TeamAgentsPanel";

const team = { id: "t-1", slug: "acme", name: "Acme", namespace: "team-acme" };
const agent = { id: "agt-1", team_id: "t-1", team_slug: "acme", name: "Build agent", status: "active", created_by: "u-1", created_at: "2026-10-20T10:00:00Z" };
const members = [{ user_id: "u-1", email: "alice@example.com", role: "owner" }];
function mount() {
  return render(<AppProviders><TeamAgentsPanel team={team} members={members} /></AppProviders>);
}
function response(json: unknown, status = 200) {
  return { ok: status < 400, status, json: async () => json, text: async () => JSON.stringify(json) } as Response;
}
afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); });

describe("TeamAgentsPanel", () => {
  it("loads every cursor page and labels partial counts honestly", async () => {
    const user = userEvent.setup();
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => String(input).includes("cursor=next")
      ? response({ agents: [{ ...agent, id: "agt-2", name: "Retired agent", status: "inactive", created_by: "former-user" }] })
      : response({ agents: [agent], next_cursor: "next" }));
    vi.stubGlobal("fetch", fetchMock);
    const { container } = mount();
    const table = await screen.findByTestId("team-agents-table");
    expect(table).toHaveTextContent("alice@example.com");
    expect(screen.getByRole("status", { name: "Agent directory completeness" })).toHaveTextContent("1 agents loaded. The directory is incomplete.");
    expect(screen.getByRole("link", { name: "Build agent" })).toHaveAttribute("href", "#/agents?team=acme&agent=agt-1");
    await user.click(screen.getByTestId("team-agents-more"));
    await screen.findByText("Retired agent");
    expect(table).toHaveTextContent("former-user");
    expect(screen.getByRole("status", { name: "Agent directory completeness" })).toHaveTextContent("All 2 team agents loaded.");
    expect(screen.queryByTestId("team-agents-more")).not.toBeInTheDocument();
    expect(fetchMock.mock.calls.every(([url]) => String(url).includes("/runtime/teams/acme/agents"))).toBe(true);
    expect((await axe(container, { rules: { "color-contrast": { enabled: false } } })).violations).toEqual([]);
  });

  it("shows a denied directory as an error, never a zero count", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => response({ message: "forbidden" }, 403)));
    mount();
    await screen.findByTestId("team-agents-error", {}, { timeout: 3000 });
    expect(screen.queryByTestId("team-agents-table")).not.toBeInTheDocument();
    expect(screen.queryByRole("group", { name: "Loaded team agents" })).not.toBeInTheDocument();
  });

  it("retains loaded rows after a next-page failure and retries that page", async () => {
    const user = userEvent.setup();
    let fail = true;
    vi.stubGlobal("fetch", vi.fn(async (url) => String(url).includes("cursor=next")
      ? fail ? response({ message: "directory unavailable" }, 503) : response({ agents: [{ ...agent, id: "agt-2", name: "Second agent" }] })
      : response({ agents: [agent], next_cursor: "next" })));
    mount();
    await user.click(await screen.findByTestId("team-agents-more"));
    const error = await screen.findByTestId("team-agents-error", {}, { timeout: 3000 });
    expect(screen.getByTestId("team-agents-table")).toHaveTextContent("Build agent");
    expect(screen.getByRole("status", { name: "Agent directory completeness" })).toHaveTextContent("incomplete");
    fail = false;
    await user.click(within(error).getByRole("button"));
    await screen.findByText("Second agent");
    await waitFor(() => expect(screen.queryByTestId("team-agents-error")).not.toBeInTheDocument());
  });

  it("rejects foreign-team records", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => response({ agents: [{ ...agent, team_id: "other" }] })));
    mount();
    await screen.findByTestId("team-agents-error", {}, { timeout: 3000 });
    expect(screen.queryByText("Build agent")).not.toBeInTheDocument();
  });

  it("reports cursor loops without claiming the complete directory", async () => {
    const user = userEvent.setup();
    vi.stubGlobal("fetch", vi.fn(async () => response({ agents: [agent], next_cursor: "same" })));
    mount();
    await user.click(await screen.findByTestId("team-agents-more"));
    await screen.findByTestId("team-agents-cursor-error");
    expect(screen.getByRole("status", { name: "Agent directory completeness" })).toHaveTextContent("incomplete");
    expect(screen.queryByTestId("team-agents-more")).not.toBeInTheDocument();
  });

  it("describes a successful empty directory explicitly", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => response({ agents: [] })));
    mount();
    await screen.findByText("No agents registered to this team.");
    expect(screen.getByRole("status", { name: "Agent directory completeness" })).toHaveTextContent("All 0 team agents loaded.");
  });
});
