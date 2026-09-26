import { useState } from "react";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { GrantForm, SessionForm, type GrantDraft, type SessionDraft } from "./accessForms";
import { AppProviders } from "../../providers/AppProviders";

const TEAMS = {
  teams: [
    { id: "team-acme", slug: "acme", name: "Acme", namespace: "mcp-team-acme" },
    { id: "team-globex", slug: "globex", name: "Globex", namespace: "mcp-team-globex" },
  ],
};

const MEMBERS: Record<string, unknown> = {
  acme: { members: [{ user_id: "user-alice", email: "alice@example.com", role: "owner" }] },
  globex: { members: [{ user_id: "user-bob", email: "bob@example.com", role: "member" }] },
};

const EMPTY_DRAFT: GrantDraft = {
  name: "picker-test",
  namespace: "mcp-servers",
  server: "demo",
  humanID: "",
  agentID: "",
  teamID: "",
  maxTrust: "low",
  allowedSideEffects: ["read"],
  expiresAt: "",
};

const EMPTY_SESSION: SessionDraft = {
  name: "picker-session",
  namespace: "mcp-servers",
  server: "demo",
  humanID: "",
  agentID: "",
  teamID: "",
  consentedTrust: "low",
  expiresAt: "",
};

function stubIdentityApi(options: {
  teams?: unknown;
  members?: Record<string, unknown>;
  agents?: Record<string, unknown>;
  failTeams?: boolean;
  failMembers?: boolean;
  holdTeams?: boolean;
  holdMembers?: boolean;
  holdAgents?: boolean;
  failAgents?: boolean;
} = {}) {
  let releaseTeams: (() => void) | undefined;
  const teamsGate = new Promise<void>((resolve) => {
    releaseTeams = resolve;
  });
  let releaseMembers: (() => void) | undefined;
  const membersGate = new Promise<void>((resolve) => {
    releaseMembers = resolve;
  });
  let releaseAgents: (() => void) | undefined;
  const agentsGate = new Promise<void>((resolve) => {
    releaseAgents = resolve;
  });
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    const agentMatch = url.match(/\/runtime\/teams\/([^/]+)\/agents/);
    if (agentMatch) {
      if (options.holdAgents) await agentsGate;
      if (options.failAgents) {
        return { ok: false, status: 500, json: async () => ({ error: "agent_lookup_failed" }), text: async () => JSON.stringify({ error: "agent_lookup_failed" }) } as unknown as Response;
      }
      const slug = decodeURIComponent(agentMatch[1]);
      const defaults = { acme: { agents: [
        { id: "agt_01arz3ndektsv4rrffq69g5fav", team_id: "team-acme", team_slug: "acme", name: "Ops Agent", status: "active" },
        { id: "agt_01arz3ndektsv4rrffq69g5faw", team_id: "team-acme", team_slug: "acme", name: "Retired Agent", status: "inactive" },
      ] } };
      return { ok: true, status: 200, json: async () => (options.agents ?? defaults)[slug] ?? { agents: [] }, text: async () => "" } as unknown as Response;
    }
    const memberMatch = url.match(/\/runtime\/teams\/([^/]+)\/members/);
    if (memberMatch) {
      const slug = decodeURIComponent(memberMatch[1]);
      if (options.holdMembers) await membersGate;
      const failed = options.failMembers;
      return {
        ok: !failed,
        status: failed ? 500 : 200,
        json: async () => failed ? { error: "member_lookup_failed" } : (options.members ?? MEMBERS)[slug] ?? { members: [] },
        text: async () => JSON.stringify({ error: "member_lookup_failed" }),
      } as unknown as Response;
    }
    if (options.holdTeams && url.endsWith("/runtime/teams")) await teamsGate;
    const failed = options.failTeams && url.includes("/runtime/teams");
    return {
      ok: !failed,
      status: failed ? 500 : 200,
      json: async () => failed ? { error: "team_lookup_failed" } : (options.teams ?? TEAMS),
      text: async () => JSON.stringify({ error: "team_lookup_failed" }),
    } as unknown as Response;
  });
  vi.stubGlobal("fetch", fetchMock);
  return {
    fetchMock,
    releaseTeams: () => releaseTeams?.(),
    releaseMembers: () => releaseMembers?.(),
    releaseAgents: () => releaseAgents?.(),
  };
}

function FormHarness({ onSubmit = vi.fn() }: { onSubmit?: (draft: GrantDraft) => void }) {
  const [draft, setDraft] = useState(EMPTY_DRAFT);
  return (
    <AppProviders>
      <GrantForm
        draft={draft}
        servers={[{ name: "demo", namespace: "mcp-servers", team_id: "team-acme", ready: "1/1", status: "Ready" }]}
        namespaces={["mcp-servers"]}
        busy={false}
        submitError=""
        onChange={setDraft}
        onCancel={() => {}}
        onSubmit={() => onSubmit(draft)}
      />
    </AppProviders>
  );
}

function SessionFormHarness({ onSubmit = vi.fn() }: { onSubmit?: (draft: SessionDraft) => void }) {
  const [draft, setDraft] = useState(EMPTY_SESSION);
  return (
    <AppProviders>
      <SessionForm
        draft={draft}
        servers={[{ name: "demo", namespace: "mcp-servers", team_id: "team-acme", ready: "1/1", status: "Ready" }]}
        namespaces={["mcp-servers"]}
        busy={false}
        submitError=""
        onChange={setDraft}
        onCancel={() => {}}
        onSubmit={() => onSubmit(draft)}
      />
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

describe("access subject pickers", () => {
  it("submits the selected team's canonical ID in team-only mode", async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn();
    stubIdentityApi();
    render(<FormHarness onSubmit={onSubmit} />);

    await user.selectOptions(await screen.findByTestId("grant-subject-mode"), "team");
    await user.selectOptions(await screen.findByTestId("grant-team-select"), "acme");
    await user.click(screen.getByTestId("grant-create-submit"));

    expect(onSubmit).toHaveBeenCalledWith(expect.objectContaining({ teamID: "team-acme", humanID: "" }));
    expect(onSubmit.mock.calls[0][0]).toHaveProperty("agentID", "");
  });

  it("submits the stable user ID selected from the chosen team's member list", async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn();
    const { fetchMock } = stubIdentityApi();
    render(<FormHarness onSubmit={onSubmit} />);

    await user.selectOptions(await screen.findByTestId("grant-team-select"), "acme");
    await user.selectOptions(await screen.findByTestId("grant-human-select"), "user-alice");
    await user.click(screen.getByTestId("grant-create-submit"));

    expect(onSubmit).toHaveBeenCalledWith(expect.objectContaining({ humanID: "user-alice", teamID: "team-acme" }));
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes("/runtime/teams/acme/members"))).toBe(true);
  });

  it("clears the previous member when the team changes", async () => {
    const user = userEvent.setup();
    stubIdentityApi();
    render(<FormHarness />);

    await user.selectOptions(await screen.findByTestId("grant-team-select"), "acme");
    await user.selectOptions(await screen.findByTestId("grant-human-select"), "user-alice");

    await user.selectOptions(screen.getByTestId("grant-team-select"), "globex");
    expect(await screen.findByTestId("grant-human-select")).toHaveValue("");
    expect(screen.queryByRole("option", { name: /alice@example.com/ })).not.toBeInTheDocument();
    expect(await screen.findByRole("option", { name: /bob@example.com/ })).toBeInTheDocument();
  });

  it("exposes agent subjects backed by the team-scoped directory", async () => {
    stubIdentityApi();
    render(<FormHarness />);

    const subjectMode = await screen.findByTestId("grant-subject-mode");
    expect(subjectMode).toHaveDisplayValue("Human");
    expect(screen.getByRole("option", { name: "Team only" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "Agent" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "Human and agent" })).toBeInTheDocument();
    expect(screen.getByText(/Agent choices come from the selected team's active agent directory/)).toBeInTheDocument();
  });

  it("submits the selected active agent and displays inactive agents as disabled", async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn();
    stubIdentityApi();
    render(<FormHarness onSubmit={onSubmit} />);

    await user.selectOptions(await screen.findByTestId("grant-subject-mode"), "agent");
    await user.selectOptions(await screen.findByTestId("grant-team-select"), "acme");
    const agentSelect = await screen.findByTestId("grant-agent-select");
    await user.selectOptions(agentSelect, "agt_01arz3ndektsv4rrffq69g5fav");
    expect(screen.getByRole("option", { name: /Retired Agent.*inactive, unavailable/ })).toBeDisabled();
    await user.click(screen.getByTestId("grant-create-submit"));
    expect(onSubmit).toHaveBeenCalledWith(expect.objectContaining({ agentID: "agt_01arz3ndektsv4rrffq69g5fav", teamID: "team-acme" }));
  });

  it("never offers a custom agent ID outside the directory", async () => {
    const user = userEvent.setup();
    stubIdentityApi();
    render(<FormHarness />);

    await user.selectOptions(await screen.findByTestId("grant-subject-mode"), "agent");
    await user.selectOptions(await screen.findByTestId("grant-team-select"), "acme");
    await screen.findByTestId("grant-agent-select");
    expect(screen.queryByRole("button", { name: "Enter a custom agent ID" })).not.toBeInTheDocument();
    expect(screen.queryByTestId("grant-agent-custom")).not.toBeInTheDocument();
  });

  it("shows active-agent loading, empty, and error states", async () => {
    const user = userEvent.setup();
    const held = stubIdentityApi({ holdAgents: true });
    const loading = render(<FormHarness />);
    await user.selectOptions(await screen.findByTestId("grant-subject-mode"), "agent");
    await user.selectOptions(await screen.findByTestId("grant-team-select"), "acme");
    expect(await screen.findByTestId("grant-agents-loading")).toBeInTheDocument();
    held.releaseAgents();
    expect(await screen.findByTestId("grant-agent-select")).toBeInTheDocument();
    loading.unmount();

    stubIdentityApi({ agents: { acme: { agents: [] } } });
    const empty = render(<FormHarness />);
    await user.selectOptions(await screen.findByTestId("grant-subject-mode"), "agent");
    await user.selectOptions(await screen.findByTestId("grant-team-select"), "acme");
    expect(await screen.findByTestId("grant-agents-empty")).toHaveTextContent("This team has no active agents");
    empty.unmount();

    stubIdentityApi({ failAgents: true });
    render(<FormHarness />);
    await user.selectOptions(await screen.findByTestId("grant-subject-mode"), "agent");
    await user.selectOptions(await screen.findByTestId("grant-team-select"), "acme");
    expect(await screen.findByTestId("grant-agents-error", {}, { timeout: 4000 })).toHaveTextContent("Team agents could not be loaded");
  });

  it("requires a 24-hour-defaulted expiry for a cross-team session", async () => {
    const user = userEvent.setup();
    stubIdentityApi();
    render(<SessionFormHarness />);

    await user.selectOptions(await screen.findByTestId("session-subject-mode"), "team");
    await user.selectOptions(await screen.findByTestId("session-team-select"), "globex");
    expect(await screen.findByTestId("session-cross-team-banner")).toHaveTextContent("Cross-team access");
    expect(screen.getByTestId("session-expires")).not.toHaveValue("");
    expect(screen.getByTestId("session-expires")).toBeRequired();
  });

  it("shows cross-team access and requires a 24-hour-defaulted expiry", async () => {
    const user = userEvent.setup();
    stubIdentityApi();
    render(<FormHarness />);

    await user.selectOptions(await screen.findByTestId("grant-subject-mode"), "team");
    await user.selectOptions(await screen.findByTestId("grant-team-select"), "globex");
    expect(await screen.findByTestId("grant-cross-team-banner")).toHaveTextContent("Cross-team access");
    expect(screen.getByTestId("grant-expires-at")).not.toHaveValue("");
    expect(screen.getByTestId("grant-expires-at")).toBeRequired();
  });

  it("shows loading, empty, and API error states and allows a marked custom ID", async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn();
    const teamsRequest = stubIdentityApi({ holdTeams: true });
    const loading = render(<FormHarness />);
    expect(screen.getByTestId("grant-teams-loading")).toBeInTheDocument();
    teamsRequest.releaseTeams();
    await screen.findByTestId("grant-team-select");
    loading.unmount();

    stubIdentityApi({ teams: { teams: [] } });
    const { unmount } = render(<FormHarness onSubmit={onSubmit} />);
    expect(await screen.findByTestId("grant-teams-empty")).toHaveTextContent("No teams are available");
    await user.click(screen.getByRole("button", { name: "Enter a custom team ID" }));
    expect(screen.getByTestId("grant-team-not-in-directory")).toHaveTextContent("Not in directory");
    await user.type(screen.getByTestId("grant-team-custom"), "external-team");
    await user.click(screen.getByTestId("grant-create-submit"));
    expect(onSubmit).toHaveBeenCalledWith(expect.objectContaining({ teamID: "external-team" }));
    unmount();

    stubIdentityApi({ failTeams: true });
    render(<FormHarness />);
    expect(await screen.findByTestId("grant-teams-error", {}, { timeout: 4000 })).toHaveTextContent("Teams could not be loaded");
    await user.click(screen.getByRole("button", { name: "Enter a custom team ID" }));
    expect(screen.getByTestId("grant-team-custom")).toBeInTheDocument();
  });

  it("shows loading, empty, and API error states for team members", async () => {
    const user = userEvent.setup();
    const { releaseMembers } = stubIdentityApi({ holdMembers: true });
    const first = render(<FormHarness />);

    await user.selectOptions(await screen.findByTestId("grant-team-select"), "acme");
    expect(await screen.findByTestId("grant-members-loading")).toBeInTheDocument();
    releaseMembers();
    expect(await screen.findByTestId("grant-human-select")).toBeInTheDocument();
    first.unmount();

    stubIdentityApi({ members: { acme: { members: [] } } });
    render(<FormHarness />);
    await user.selectOptions(await screen.findByTestId("grant-team-select"), "acme");
    expect(await screen.findByTestId("grant-members-empty")).toHaveTextContent("This team has no members");
  });

  it("offers a marked custom human ID when needed", async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn();
    stubIdentityApi();
    render(<FormHarness onSubmit={onSubmit} />);

    await user.selectOptions(await screen.findByTestId("grant-team-select"), "acme");
    await user.click(await screen.findByRole("button", { name: "Enter a custom human ID" }));
    expect(screen.getByTestId("grant-human-not-in-directory")).toHaveTextContent("Not in directory");
    await user.type(screen.getByTestId("grant-human-custom"), "external-user");
    await user.click(screen.getByTestId("grant-create-submit"));
    expect(screen.getByTestId("grant-human-custom")).toHaveValue("external-user");
    expect(onSubmit).toHaveBeenCalledWith(expect.objectContaining({ humanID: "external-user", teamID: "team-acme" }));
  });

  it("shows a member API error and offers the custom ID path", async () => {
    const user = userEvent.setup();
    stubIdentityApi({ failMembers: true });
    render(<FormHarness />);

    await user.selectOptions(await screen.findByTestId("grant-team-select"), "acme");
    expect(await screen.findByTestId("grant-members-error", {}, { timeout: 4000 })).toHaveTextContent("Team members could not be loaded");
    await user.click(screen.getByRole("button", { name: "Enter a custom human ID" }));
    expect(screen.getByTestId("grant-human-custom")).toBeInTheDocument();
  });
});
