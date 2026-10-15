import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { axe } from "vitest-axe";
import { AgentLifecycleDialog } from "./AgentLifecycleDialog";
import { AppProviders } from "../../providers/AppProviders";
import type { SessionSummary } from "../../api/types";

const agent = { id: "agt-one", name: "Demo", status: "active", team_id: "B", team_slug: "b" } as const;
const sessions: SessionSummary[] = [
  { name: "live", namespace: "team-a", subject: { humanID: "alice", agentID: agent.id }, revoked: false, expiresAt: "2099-01-01T00:00:00Z" },
  { name: "expired", namespace: "team-a", revoked: false, expiresAt: "2000-01-01T00:00:00Z" },
  { name: "old", namespace: "team-a", revoked: true, revocationCause: "manual" },
];
function setup(options: { partial?: boolean; unavailable?: boolean; impactError?: boolean } = {}) {
  let changed = false;
  const onChanged = vi.fn(), onClose = vi.fn();
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    if (init?.method === "POST") {
      changed = true;
      if (options.partial) return new Response(JSON.stringify({ message: "agent is inactive but session revocation is incomplete; retry deactivation" }), { status: 500 });
      return new Response(JSON.stringify({ agent: { ...agent, status: "inactive" } }));
    }
    if (options.impactError || (changed && options.unavailable)) return new Response("unavailable", { status: 503 });
    return new Response(JSON.stringify({ agent: { ...agent, status: changed ? "inactive" : "active" }, can_manage: true, grants: [], sessions: changed ? sessions.map((s) => s.name === "live" ? { ...s, revoked: true, revocationCause: "agent_deactivated" } : s) : sessions }));
  });
  vi.stubGlobal("fetch", fetchMock);
  const view = render(<AppProviders><AgentLifecycleDialog agent={agent} onClose={onClose} onChanged={onChanged} /></AppProviders>);
  return { ...view, fetchMock, onChanged, onClose };
}
afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); });

it("previews visible sessions and verifies recorded revocations while retaining history", async () => {
  const user = userEvent.setup();
  const { onChanged, container } = setup();
  expect(await screen.findByText(/1 visible active sessions/)).toBeInTheDocument();
  expect(screen.getByText("team-a/expired — Expired")).toBeInTheDocument();
  expect(await axe(container, { rules: { "color-contrast": { enabled: false } } })).toHaveNoViolations();
  await user.click(screen.getByTestId("agent-confirm-yes"));
  const result = await screen.findByTestId("agent-lifecycle-result");
  expect(result).toHaveTextContent("Final agent status: inactive");
  expect(result).toHaveTextContent("1 visible sessions newly recorded as revoked by agent deactivation");
  expect(result).toHaveTextContent("Manually revoked");
  expect(within(result).getByRole("link", { name: "team-a/live" })).toHaveAttribute("href", expect.stringContaining("namespace=team-a"));
  expect(onChanged).toHaveBeenCalledOnce();
});

it("shows partial failure alongside verified final session states", async () => {
  const user = userEvent.setup();
  const { fetchMock } = setup({ partial: true });
  await screen.findByText(/1 visible active sessions/);
  await user.click(screen.getByTestId("agent-confirm-yes"));
  expect(await screen.findByRole("alert")).toHaveTextContent("revocation is incomplete");
  expect(screen.getByTestId("agent-lifecycle-result")).toHaveTextContent("Final agent status: inactive");
  await user.click(screen.getByRole("button", { name: "Retry deactivation" }));
  expect(fetchMock.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(2);
});

it("does not claim verified success when the final read fails", async () => {
  const user = userEvent.setup();
  setup({ unavailable: true });
  await screen.findByText(/1 visible active sessions/);
  await user.click(screen.getByTestId("agent-confirm-yes"));
  expect(await screen.findByRole("alert")).toHaveTextContent("could not be verified");
  expect(screen.getByTestId("agent-lifecycle-result")).not.toHaveTextContent("Final agent status:");
});

it("allows cancelling when the impact read fails and sends no write", async () => {
  const user = userEvent.setup();
  const { fetchMock, onClose } = setup({ impactError: true });
  await screen.findByText("Session impact could not be loaded.");
  expect(screen.getByTestId("agent-confirm-yes")).toBeDisabled();
  await user.click(screen.getByTestId("agent-confirm-cancel"));
  expect(onClose).toHaveBeenCalledOnce();
  expect(fetchMock.mock.calls.some(([, init]) => init?.method === "POST")).toBe(false);
});
