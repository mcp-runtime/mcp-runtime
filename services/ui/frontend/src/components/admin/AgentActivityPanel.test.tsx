import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AppProviders } from "../../providers/AppProviders";
import { AgentActivityPanel } from "./AgentActivityPanel";
import { axe } from "vitest-axe";

afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); });

describe("AgentActivityPanel", () => {
  it("requests exact agent tool calls and changes the bounded time window", async () => {
    const fetchMock = vi.fn(async (_input: RequestInfo | URL) => ({ok:true,status:200,json:async()=>({filters:{agent_id:"agent-one"},totals:{events:12,allowed:10,denied:2,http_failures:2},requests:[],recent:[]})}));
    vi.stubGlobal("fetch",fetchMock);
    const { container } = render(<AppProviders><AgentActivityPanel agentID="agent-one" platformAdmin={false} /></AppProviders>);
    expect(await screen.findByRole("group",{name:"Agent tool call totals"})).toHaveTextContent("12");
    expect(String(fetchMock.mock.calls[0]?.[0])).toContain("agent_id=agent-one");
    expect(String(fetchMock.mock.calls[0]?.[0])).toContain("rpc_method=tools%2Fcall");
    expect(screen.getByText(/shared catalog servers may be excluded/)).toBeInTheDocument();
    await userEvent.selectOptions(screen.getByLabelText("Activity time range"),"24");
    await screen.findByRole("group",{name:"Agent tool call totals"});
    expect(fetchMock.mock.calls.some(call=>String(call[0]).includes("window_hours=24"))).toBe(true);
    expect(await axe(container, { rules: { "color-contrast": { enabled: false } } })).toHaveNoViolations();
  });

  it("does not attribute unfiltered backend totals to an agent", async () => {
    vi.stubGlobal("fetch",vi.fn(async()=>({ok:true,status:200,json:async()=>({filters:{},totals:{events:9999}})})));
    render(<AppProviders><AgentActivityPanel agentID="agent-one" platformAdmin={true} /></AppProviders>);
    expect(await screen.findByTestId("agent-activity-error")).toHaveTextContent("did not confirm the agent filter");
    expect(screen.queryByText("9,999")).not.toBeInTheDocument();
    expect(screen.queryByRole("group",{name:"Agent tool call totals"})).not.toBeInTheDocument();
  });
});
