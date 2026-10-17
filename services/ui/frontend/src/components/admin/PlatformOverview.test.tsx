import { render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { PlatformOverview } from "./PlatformOverview";
import { AppProviders } from "../../providers/AppProviders";

afterEach(() => vi.unstubAllGlobals());
it("uses authoritative totals above list page limits and keeps health visible when audit fails", async () => {
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input); const status = url.includes("/operations") ? 503 : 200;
    const body = url.includes("/overview/directory") ? { teams: 321, users: 1234, agents: 999, active_sessions: 543 } :
      url.includes("/overview/servers") ? { servers: 456, failures: [{ namespace: "team", name: "broken", status: "Error" }] } :
        { components: [{ key: "runtime", display: "Runtime API", status: "Ready", ready: "1/1" }] };
    return { ok: status < 400, status, json: async () => body, text: async () => JSON.stringify(body) } as Response;
  }));
  render(<AppProviders><PlatformOverview onSignIn={vi.fn()} /></AppProviders>);
  expect(await screen.findByText("1234")).toBeInTheDocument();
  expect(screen.getByText("543")).toBeInTheDocument();
  expect(await screen.findByRole("link", { name: "team/broken" })).toHaveAttribute("href", "#/servers?namespace=team&server=team%2Fbroken");
  expect(await screen.findByText(/Runtime API/)).toBeInTheDocument();
  expect(await screen.findByTestId("overview-audit-error")).toBeInTheDocument();
});
