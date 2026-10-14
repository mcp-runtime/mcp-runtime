import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { AppProviders } from "../../providers/AppProviders";
import { UsageAnalyticsPanel, traceExploreURL } from "./UsageAnalyticsPanel";

const usage = {
  totals: {
    events: 30,
    allowed: 28,
    denied: 2,
    unique_servers: 2,
    unique_humans: 1,
    unique_agents: 1,
    unique_sessions: 2,
    http_failures: 2,
    server_errors: 1,
    average_latency_ms: 5,
    p95_latency_ms: 14,
    bytes_out: 2048,
  },
  servers: [],
  tools: [],
  actors: [],
  decisions: [],
  bucket_seconds: 60,
  series: [
    {
      bucket: "2026-10-14T10:00:00Z",
      events: 30,
      allowed: 28,
      denied: 2,
      requests_per_second: 0.5,
    },
  ],
  facets: {
    servers: ["demo", "billing"],
    methods: ["tools/call", "tools/list"],
    tools: ["echo"],
    policy_versions: ["v1"],
  },
  requests: [
    {
      server: "demo",
      namespace: "mcp-servers-public",
      rpc_method: "tools/call",
      tool_name: "echo",
      decision: "allow",
      policy_version: "v1",
      events: 30,
      http_failures: 0,
      p95_latency_ms: 14,
      last_seen: "2026-10-14T10:00:00Z",
      trace_id: "a".repeat(32),
    },
  ],
};
function setup() {
  const fetchMock = vi.fn(
    async (input: RequestInfo | URL) =>
      ({
        ok: true,
        status: 200,
        json: async () =>
          String(input).includes("/analytics/usage")
            ? usage
            : {
                teams: [
                  {
                    id: "a",
                    name: "Acme",
                    slug: "acme",
                    namespace: "mcp-team-acme",
                  },
                  {
                    id: "b",
                    name: "Billing Team",
                    slug: "billing",
                    namespace: "mcp-team-billing",
                  },
                ],
              },
      }) as Response,
  );
  vi.stubGlobal("fetch", fetchMock);
  render(
    <AppProviders>
      <UsageAnalyticsPanel onSignIn={() => {}} />
    </AppProviders>,
  );
  return fetchMock;
}
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});
it("combines server, method and tool activity with reliability and trace links", async () => {
  setup();
  const table = await screen.findByTestId("analytics-requests-table");
  expect(table).toHaveTextContent("tools/call");
  expect(table).toHaveTextContent("echo");
  expect(
    screen.queryByRole("heading", { name: "Tools", exact: true }),
  ).not.toBeInTheDocument();
  expect(screen.getByTestId("analytics-reliability")).toHaveTextContent(
    "p95 latency",
  );
  expect(screen.getByTestId("analytics-reliability")).toHaveTextContent(
    "14 ms",
  );
  expect(
    within(table).getByRole("link", { name: "Open trace" }),
  ).toHaveAttribute("href", traceExploreURL("a".repeat(32)));
});
it("queries the selected team, server, method and tool and clears dependent filters", async () => {
  const user = userEvent.setup();
  const fetchMock = setup();
  await screen.findByTestId("analytics-requests-table");
  await user.selectOptions(screen.getByTestId("analytics-scope"), "tenant");
  await user.type(await screen.findByTestId("analytics-team-search"), "Acme");
  expect(
    within(screen.getByTestId("analytics-team")).queryByRole("option", {
      name: "Billing Team",
    }),
  ).not.toBeInTheDocument();
  await user.selectOptions(screen.getByTestId("analytics-team"), "a");
  await user.selectOptions(screen.getByTestId("analytics-server"), "demo");
  await user.selectOptions(
    screen.getByTestId("analytics-method"),
    "tools/call",
  );
  await user.selectOptions(screen.getByTestId("analytics-tool"), "echo");
  await waitFor(() =>
    expect(
      fetchMock.mock.calls.some(([url]) => {
        const q = new URL(String(url), "https://example.test").searchParams;
        return (
          q.get("scope") === "tenant" &&
          q.get("team_id") === "a" &&
          q.get("server") === "demo" &&
          q.get("rpc_method") === "tools/call" &&
          q.get("tool_name") === "echo"
        );
      }),
    ).toBe(true),
  );
  await user.selectOptions(
    screen.getByTestId("analytics-method"),
    "tools/list",
  );
  expect(screen.getByTestId("analytics-tool")).toBeDisabled();
  expect(screen.getByTestId("analytics-tool")).toHaveValue("");
  await user.selectOptions(screen.getByTestId("analytics-scope"), "public");
  await waitFor(() =>
    expect(
      fetchMock.mock.calls.some(([url]) => {
        const q = new URL(String(url), "https://example.test").searchParams;
        return (
          q.get("scope") === "public" &&
          !q.has("team_id") &&
          !q.has("server") &&
          !q.has("tool_name")
        );
      }),
    ).toBe(true),
  );
});
it("applies the time range and HTTP outcome to the analytics query", async () => {
  const user = userEvent.setup();
  const fetchMock = setup();
  await screen.findByTestId("analytics-requests-table");
  await user.selectOptions(screen.getByTestId("analytics-window"), "1");
  await user.selectOptions(screen.getByTestId("analytics-http-status"), "5xx");
  await waitFor(() =>
    expect(
      fetchMock.mock.calls.some(
        ([url]) =>
          String(url).includes("window_hours=1") &&
          String(url).includes("http_status=5xx"),
      ),
    ).toBe(true),
  );
});
it("never constructs links from arbitrary trace values", () => {
  expect(traceExploreURL("javascript:alert(1)")).toBeUndefined();
  expect(traceExploreURL("")).toBeUndefined();
});
