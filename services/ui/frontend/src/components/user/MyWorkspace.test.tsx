import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { axe } from "vitest-axe";
import { AppProviders } from "../../providers/AppProviders";
import { MyWorkspace } from "./MyWorkspace";
import type { AuthStatus } from "../../api/types";

const TENANT: AuthStatus = { authenticated: true, principal: { subject: "me", role: "user" } };
function stub({ role = "member", activityStatus = 200, teamsStatus = 200 } = {}) {
  const calls: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input); calls.push(url);
    let status = 200; let body: unknown = {};
    if (url.includes("/workspace/teams")) { status = teamsStatus; body = { teams: [{ id: "team", slug: "acme", name: "Acme", namespace: "mcp-team-acme", role }] }; }
    else if (url.includes("/workspace/servers")) body = { servers: [{ name: "mine", namespace: "mcp-team-acme", status: "Ready", can_manage: true }, { name: "visible", namespace: "mcp-team-acme", status: "Ready", can_manage: false }], deployment_attribution: "unavailable" };
    else if (url.includes("/workspace/access")) body = { grants: [], sessions: [] };
    else if (url.includes("/user/analytics/usage")) { status = activityStatus; body = { recent: [] }; }
    else if (url.includes("/teams/acme/agents")) body = { agents: [{ id: "agent", name: "Team agent", status: "active" }] };
    else if (url.includes("/runtime/teams")) body = { teams: [{ id: "team", slug: "acme", name: "Acme", role, namespace: "mcp-team-acme" }], members: [] };
    return { ok: status < 400, status, json: async () => body, text: async () => JSON.stringify(body) } as Response;
  })); return calls;
}
afterEach(() => vi.unstubAllGlobals());
describe("My workspace", () => {
  it("separates catalog visibility from management and unavailable attribution while analytics fails", async () => {
    stub({ activityStatus: 503 });
    const { container } = render(<AppProviders><MyWorkspace auth={TENANT} onSignIn={vi.fn()} /></AppProviders>);
    expect(await screen.findByText("Accessible to me: 2")).toBeInTheDocument();
    expect(screen.getByText("Manageable by me: 1")).toBeInTheDocument();
    expect(screen.getByText(/Deployed by me: unavailable/)).toBeInTheDocument();
    expect(await screen.findByText("Team agent")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Manage members" })).not.toBeInTheDocument();
    expect(await screen.findByTestId("workspace-activity-error")).toBeInTheDocument();
    expect(screen.getAllByRole("heading", { name: "Acme" })).toHaveLength(2);
    expect(await axe(container, { rules: { "color-contrast": { enabled: false } } })).toHaveNoViolations();
  });
  it("offers owner membership management and keeps it separate from platform administration", async () => {
    stub({ role: "owner" });
    render(<AppProviders><MyWorkspace auth={TENANT} onSignIn={vi.fn()} /></AppProviders>);
    expect(await screen.findByRole("link", { name: "Manage members" })).toHaveAttribute("href", "#/workspace/team?team=acme");
    expect(screen.queryByRole("link", { name: "Manage platform teams" })).not.toBeInTheDocument();
  });
  it("does not request personal analytics for an administrator or label all teams as memberships", async () => {
    const calls = stub();
    render(<AppProviders><MyWorkspace auth={{ authenticated: true, principal: { role: "admin", subject: "admin" } }} onSignIn={vi.fn()} /></AppProviders>);
    expect(await screen.findByText("Your role: member")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Platform overview" })).toHaveAttribute("href", "#/admin/overview");
    expect(calls.some(url => url.includes("/user/analytics"))).toBe(false);
    expect(calls.some(url => url.endsWith("/runtime/teams"))).toBe(false);
  });
  it("denies a member's direct team-management deep link", async () => {
    const calls = stub();
    render(<AppProviders><MyWorkspace auth={TENANT} onSignIn={vi.fn()} section="team" team="acme" /></AppProviders>);
    expect(await screen.findByText("You cannot manage this team.")).toBeInTheDocument();
    expect(calls.some(url => url.includes("/members"))).toBe(false);
    expect(screen.queryByTestId("team-create-toggle")).not.toBeInTheDocument();
  });
  it("reuses member management for owners without offering platform team creation", async () => {
    stub({ role: "owner" });
    render(<AppProviders><MyWorkspace auth={TENANT} onSignIn={vi.fn()} section="team" team="acme" /></AppProviders>);
    expect(await screen.findByTestId("team-members-table")).toBeInTheDocument();
    expect(screen.queryByTestId("team-create-toggle")).not.toBeInTheDocument();
  });
  it("shows a sign-in action for an expired section while other sections remain usable", async () => {
    stub({ teamsStatus: 401 }); const signIn = vi.fn(); const user = userEvent.setup();
    render(<AppProviders><MyWorkspace auth={TENANT} onSignIn={signIn} /></AppProviders>);
    const expired = await screen.findByTestId("workspace-teams-unauthorized");
    await user.click(within(expired).getByRole("button", { name: "Sign in" })); expect(signIn).toHaveBeenCalledOnce();
    expect(await screen.findByText("Accessible to me: 2")).toBeInTheDocument();
  });
});
