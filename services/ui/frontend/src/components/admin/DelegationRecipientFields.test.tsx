import { useState } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { AppProviders } from "../../providers/AppProviders";
import { GrantForm, type GrantDraft } from "./accessForms";

const initial: GrantDraft = { name: "delegate", namespace: "team-a", server: "demo", humanID: "", agentID: "", teamID: "", maxTrust: "low", allowedSideEffects: ["read"], expiresAt: "" };
function setup(status = 200) {
  const submit = vi.fn();
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.includes("/delegation-recipients")) return new Response(JSON.stringify(status === 200 ? { team: { id: "B", slug: "b", name: "Team B" }, members: [{ user_id: "bob" }], agents: [{ id: "agt-b", name: "Build", status: "active", team_id: "B", team_slug: "b" }], agent_next_cursor: url.includes("agent_cursor") ? undefined : "next-agent" } : { message: "resource namespace management is required" }), { status });
    return new Response(JSON.stringify({ teams: [] }));
  });
  vi.stubGlobal("fetch", fetchMock);
  function Harness() {
    const [draft, setDraft] = useState(initial);
    return <AppProviders><GrantForm draft={draft} servers={[]} namespaces={["team-a", "team-c"]} busy={false} submitError="" onChange={setDraft} onCancel={() => {}} onSubmit={() => submit(draft)} /><output data-testid="draft">{JSON.stringify(draft)}</output></AppProviders>;
  }
  render(<Harness />);
  return { fetchMock, submit };
}
afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); });
async function lookup() {
  const user = userEvent.setup();
  await user.click(screen.getByRole("checkbox", { name: "Delegate to another team" }));
  await user.type(screen.getByTestId("delegation-team-slug"), "b");
  await user.click(screen.getByTestId("delegation-lookup"));
  return user;
}

it("looks up an exact recipient using resource authority and submits selected stable IDs with expiry", async () => {
  const { fetchMock, submit } = setup();
  const user = await lookup();
  await user.selectOptions(await screen.findByTestId("delegation-agent"), "agt-b");
  await user.click(screen.getByTestId("grant-create-submit"));
  expect(submit).toHaveBeenCalledWith(expect.objectContaining({ teamID: "B", agentID: "agt-b", humanID: "", expiresAt: expect.any(String) }));
  expect(submit.mock.calls[0][0].expiresAt).not.toBe("");
  const url = new URL(String(fetchMock.mock.calls.find(([url]) => String(url).includes("delegation-recipients"))![0]), "http://localhost");
  expect(url.pathname).toBe("/api/ui/v1/runtime/delegation-recipients");
  expect(url.searchParams.get("namespace")).toBe("team-a");
  expect(url.searchParams.get("team")).toBe("b");
  expect(fetchMock.mock.calls.some(([url]) => String(url).includes("/teams/b/"))).toBe(false);
});

it("does not submit an incomplete human-and-agent choice as a team-wide grant", async () => {
  const { submit } = setup();
  const user = await lookup();
  await user.selectOptions(await screen.findByTestId("delegation-mode"), "human_agent");
  await user.selectOptions(screen.getByTestId("delegation-human"), "bob");
  await user.click(screen.getByTestId("grant-create-submit"));
  expect(submit).not.toHaveBeenCalled();
  await user.selectOptions(screen.getByTestId("delegation-agent"), "agt-b");
  await user.click(screen.getByTestId("grant-create-submit"));
  expect(submit).toHaveBeenCalledWith(expect.objectContaining({ humanID: "bob", agentID: "agt-b", teamID: "B" }));
});

it("clears recipient IDs when resource scope changes or the lookup text changes", async () => {
  setup();
  const user = await lookup();
  await user.selectOptions(await screen.findByTestId("delegation-agent"), "agt-b");
  await user.type(screen.getByTestId("delegation-team-slug"), "c");
  expect(screen.getByTestId("draft")).toHaveTextContent('"teamID":""');
  expect(screen.queryByTestId("delegation-agent")).not.toBeInTheDocument();
  await user.selectOptions(screen.getByTestId("grant-namespace"), "team-c");
  expect(screen.getByTestId("draft")).toHaveTextContent('"agentID":""');
  expect(screen.getByTestId("delegation-team-slug")).toHaveValue("");
});

it("shows lookup authorization errors and provides no free-text agent escape", async () => {
  setup(403);
  await lookup();
  expect(await screen.findByText("Recipient lookup failed.")).toBeInTheDocument();
  expect(screen.queryByTestId("delegation-agent")).not.toBeInTheDocument();
  expect(screen.queryByTestId("grant-agent-custom")).not.toBeInTheDocument();
});

it("requests the next active-agent page without losing the chosen member", async () => {
  const { fetchMock } = setup();
  const user = await lookup();
  await user.selectOptions(await screen.findByTestId("delegation-mode"), "human_agent");
  await user.selectOptions(screen.getByTestId("delegation-human"), "bob");
  await user.click(screen.getByRole("button", { name: "Next agents", exact: true }));
  await screen.findByTestId("delegation-agent");
  expect(fetchMock.mock.calls.some(([url]) => String(url).includes("agent_cursor=next-agent"))).toBe(true);
  expect(screen.getByTestId("delegation-human")).toHaveValue("bob");
});
