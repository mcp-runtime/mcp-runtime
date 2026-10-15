import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { AppProviders } from "../../providers/AppProviders";
import { AccessWorkspace } from "./AccessWorkspace";
import { AuthorizingGrant } from "./accessLinks";
import { EventAccessLink, GatewayAuditPanel } from "../admin/GatewayAuditPanel";
import { filterGrantActivity, filterSessionTimeline } from "../admin/AccessDetail";
import type { GatewayEvent } from "../../api/types";

afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); });
const now = Date.now();
const event: GatewayEvent = { timestamp: new Date(now).toISOString(), namespace: "resource-a", decision: "allow", payload: { matched_grant: "same", matched_grant_namespace: "grant-a", matched_session: "sess", matched_session_namespace: "session-b" } };

it("uses explicit access attribution without guessing from the server namespace", () => {
  const unknown = { ...event, payload: { matched_grant: "same", matched_session: "sess" } };
  expect(filterGrantActivity([event, unknown], "same", "grant-a", now)).toEqual([event]);
  expect(filterGrantActivity([event], "same", "other", now)).toEqual([]);
  expect(filterGrantActivity([{ ...event, timestamp: "invalid" }], "same", "grant-a", now)).toEqual([]);
  expect(filterGrantActivity([{ ...event, timestamp: new Date(now - 8 * 86400000).toISOString() }], "same", "grant-a", now)).toEqual([]);
  expect(filterSessionTimeline([event, unknown], "session-b", "sess")).toEqual([event]);
  expect(filterSessionTimeline([event], "session-b", "other")).toEqual([]);
});

it("links exact recorded grants and labels unlinked sessions explicitly", () => {
  const view = render(<AuthorizingGrant session={{ name: "s", namespace: "n", revoked: false }} />);
  expect(screen.getByText(/Unknown — no grant linkage/)).toBeInTheDocument();
  view.rerender(<AuthorizingGrant session={{ name: "s", namespace: "n", revoked: false, grantName: "same", grantNamespace: "grant-a" }} />);
  expect(screen.getByRole("link")).toHaveAttribute("href", "#/access?kind=grant&namespace=grant-a&name=same");
  view.rerender(<EventAccessLink event={{ ...event, payload: { matched_grant: "same" } }} kind="grant" />);
  expect(screen.queryByRole("link")).not.toBeInTheDocument();
  expect(screen.getByText("same (namespace unreported)")).toBeInTheDocument();
});

it("resolves shared deep links within the agent API scope and queries exact activity", async () => {
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    const data = url.includes("/runtime/agents/agt-b") ? { agent: {}, grants: [], can_manage: false, sessions: [
      { name: "sess", namespace: "session-b", revoked: false, subject: { humanID: "alice" }, grantName: "same", grantNamespace: "grant-a" },
      { name: "sess", namespace: "other", revoked: true },
    ] } : url.includes("/events") ? { events: [event] } : {};
    return new Response(JSON.stringify(data));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<AppProviders><AccessWorkspace auth={{ authenticated: true, principal: { role: "user" } }} onSignIn={() => {}} params={{ kind: "session", namespace: "session-b", name: "sess", agent: "agt-b" }} /></AppProviders>);
  expect(await screen.findByTestId("access-detail-kicker")).toHaveTextContent("session-b");
  expect(screen.getByRole("link", { name: "grant-a/same" })).toBeInTheDocument();
  await screen.findByTestId("access-activity-table");
  const urls = fetchMock.mock.calls.map(([url]) => String(url));
  const query = new URL(urls.find((url) => url.includes("/events"))!, "http://localhost").searchParams;
  expect(query.get("matched_session")).toBe("sess");
  expect(query.get("matched_session_namespace")).toBe("session-b");
  expect(query.has("since")).toBe(true);
  expect(urls.some((url) => url.includes("/runtime/sessions"))).toBe(false);
});

it("loads audit links to the same grant and session pages", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ events: [event] }))));
  const user = userEvent.setup();
  render(<AppProviders><GatewayAuditPanel /></AppProviders>);
  await user.click(screen.getByRole("button", { name: "Load gateway decisions" }));
  await screen.findByTestId("gateway-audit-table");
  expect(screen.getByRole("link", { name: "grant-a/same" })).toHaveAttribute("href", "#/access?kind=grant&namespace=grant-a&name=same");
  expect(screen.getByRole("link", { name: "session-b/sess" })).toHaveAttribute("href", "#/access?kind=session&namespace=session-b&name=sess");
});
