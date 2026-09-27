import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ServersWorkspace } from "./ServersWorkspace";
import { AppProviders } from "../../providers/AppProviders";
import type { AuthStatus } from "../../api/types";

const ADMIN: AuthStatus = {
  authenticated: true,
  principal: { role: "admin", email: "admin@mcpruntime.org" },
};

// Redesign behaviour: scoped search semantics, filter dependencies, pagination,
// and inspector focus handling.

const NAMESPACES = { namespaces: [{ namespace: "mcp-servers" }, { namespace: "mcp-shared" }] };

const SERVERS = {
  servers: [
    {
      name: "workspace-assistant",
      namespace: "mcp-servers",
      ready: "1/1",
      status: "Running",
      description: "Workspace helper",
      endpoint: "http://localhost:18080/oauth-example-go-2025-11-25/mcp",
      image: "registry/workspace-assistant:1.2.0",
      authMode: "oauth",
      access_json: {
        mcpServers: {
          "workspace-assistant": {
            type: "http",
            url: "http://localhost:18080/oauth-example-go-2025-11-25/mcp",
          },
        },
      },
    },
    {
      name: "billing-bridge",
      namespace: "mcp-servers",
      ready: "0/1",
      status: "Pending",
      description: "Invoices and refunds",
    },
  ],
};

function tool(index: number, server: string) {
  return {
    tool_name: `tool_${String(index).padStart(2, "0")}`,
    description: `Tool number ${index}`,
    server_name: server,
    namespace: "mcp-servers",
    declared: true,
    live: true,
    drift_status: "declared",
    required_trust: "low",
    side_effect: "read",
    risk_level: "low",
  };
}

const MANY_TOOLS = {
  tools: [
    ...Array.from({ length: 30 }, (_, index) => tool(index, "workspace-assistant")),
    {
      tool_name: "refund_invoice",
      description: "Issues a refund",
      server_name: "billing-bridge",
      namespace: "mcp-servers",
      declared: true,
      live: false,
      drift_status: "missing",
      required_trust: "high",
      side_effect: "destructive",
      risk_level: "high",
    },
  ],
};

function stubCatalog() {
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    const payload = url.includes("/runtime/namespaces")
      ? NAMESPACES
      : url.includes("/runtime/servers")
        ? SERVERS
        : MANY_TOOLS;
    return { ok: true, status: 200, json: async () => payload } as unknown as Response;
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function renderWorkspace() {
  return render(
    <AppProviders>
      <ServersWorkspace auth={ADMIN} onSignIn={() => {}} />
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

describe("catalog search semantics", () => {
  it("server search matches server metadata and never tool text", async () => {
    const user = userEvent.setup();
    stubCatalog();

    renderWorkspace();
    await screen.findByTestId("server-list");

    // "refund_invoice" is a tool name, so it must not match any server.
    await user.type(screen.getByTestId("server-search"), "refund_invoice");
    expect(await screen.findByTestId("server-list-empty")).toBeInTheDocument();

    await user.clear(screen.getByTestId("server-search"));
    // An image reference is server metadata and must match.
    await user.type(screen.getByTestId("server-search"), "registry/workspace-assistant");
    expect(screen.getAllByTestId("server-card")).toHaveLength(1);
  });

  it("tool search matches tool metadata and leaves the server list alone", async () => {
    const user = userEvent.setup();
    stubCatalog();

    renderWorkspace();
    await screen.findByTestId("server-list");

    await user.type(screen.getByTestId("tool-search"), "refund");

    expect(screen.getAllByTestId("tool-row")).toHaveLength(1);
    expect(screen.getAllByTestId("server-card")).toHaveLength(2);
  });

  it("filters tools by drift state", async () => {
    const user = userEvent.setup();
    stubCatalog();

    renderWorkspace();
    await screen.findByTestId("server-list");

    await user.selectOptions(screen.getByTestId("tool-drift-filter"), "missing");
    expect(screen.getAllByTestId("tool-row")).toHaveLength(1);
    expect(screen.getByTestId("tool-table")).toHaveTextContent("refund_invoice");
  });

  it("counts tools with drift rather than calling everything healthy", async () => {
    stubCatalog();

    renderWorkspace();
    await screen.findByTestId("server-list");

    const stats = screen.getByTestId("server-stats");
    expect(within(stats).getByText("Tools with drift").parentElement).toHaveTextContent("1");
  });
});

describe("filter dependencies", () => {
  it("drops the server scope when the status filter hides that server", async () => {
    const user = userEvent.setup();
    stubCatalog();

    renderWorkspace();
    await screen.findByTestId("server-list");

    const cards = screen.getAllByTestId("server-card");
    const billing = cards.find((card) => card.dataset.serverKey === "mcp-servers/billing-bridge");
    await user.click(within(billing as HTMLElement).getByTestId("server-card-select"));
    expect(screen.getAllByTestId("tool-row")).toHaveLength(1);

    // Ready-only hides billing-bridge, so its tool scope must not survive.
    await user.click(screen.getByTestId("server-status-ready"));
    expect(screen.getAllByTestId("server-card")).toHaveLength(1);
    expect(screen.getByTestId("catalog-summary")).toHaveTextContent("30 tools");
  });

  it("refetches scoped to the namespace and clears the selection", async () => {
    const user = userEvent.setup();
    const fetchMock = stubCatalog();

    renderWorkspace();
    await screen.findByTestId("server-list");
    await user.click(screen.getAllByTestId("tool-row-select")[0]);
    expect(screen.getByTestId("tool-detail")).toBeInTheDocument();

    await user.selectOptions(screen.getByTestId("namespace-filter"), "mcp-shared");

    await waitFor(() => {
      const urls = fetchMock.mock.calls.map((call) => String(call[0]));
      expect(urls).toContain("/api/ui/v1/runtime/tools?namespace=mcp-shared");
    });
    expect(screen.queryByTestId("tool-detail")).not.toBeInTheDocument();
  });
});

describe("pagination", () => {
  it("pages long tool lists and reports the visible window", async () => {
    const user = userEvent.setup();
    stubCatalog();

    renderWorkspace();
    await screen.findByTestId("server-list");

    expect(screen.getAllByTestId("tool-row")).toHaveLength(25);
    expect(screen.getByTestId("tool-table-pagination-status")).toHaveTextContent("Showing 1–25 of 31");

    await user.click(screen.getByTestId("tool-table-next-page"));
    expect(screen.getAllByTestId("tool-row")).toHaveLength(6);
    expect(screen.getByTestId("tool-table-pagination-status")).toHaveTextContent("Showing 26–31 of 31");
  });

  it("returns to the first page when a filter shrinks the results", async () => {
    const user = userEvent.setup();
    stubCatalog();

    renderWorkspace();
    await screen.findByTestId("server-list");
    await user.click(screen.getByTestId("tool-table-next-page"));

    await user.type(screen.getByTestId("tool-search"), "refund");

    await waitFor(() => expect(screen.getAllByTestId("tool-row")).toHaveLength(1));
  });
});

describe("inspector overlay", () => {
  it("moves focus in, closes on Escape, and restores focus to the trigger", async () => {
    const user = userEvent.setup();
    stubCatalog();

    renderWorkspace();
    await screen.findByTestId("server-list");

    const trigger = screen.getAllByTestId("tool-row-select")[0];
    await user.click(trigger);

    const sheet = await screen.findByTestId("tool-detail");
    expect(sheet).toHaveAttribute("role", "dialog");
    await waitFor(() =>
      expect(within(sheet).getByRole("button", { name: "Close tool details" })).toHaveFocus()
    );

    await user.keyboard("{Escape}");

    await waitFor(() => expect(screen.queryByTestId("tool-detail")).not.toBeInTheDocument());
    expect(trigger).toHaveFocus();
  });

  it("opens server details without losing the tool filters", async () => {
    const user = userEvent.setup();
    stubCatalog();

    renderWorkspace();
    await screen.findByTestId("server-list");

    await user.type(screen.getByTestId("tool-search"), "refund");
    await user.click(screen.getAllByTestId("server-card-details")[0]);

    expect(await screen.findByTestId("server-detail")).toBeInTheDocument();
    expect(screen.getByTestId("tool-search")).toHaveValue("refund");

    await user.click(screen.getByTestId("server-detail-close"));
    expect(screen.queryByTestId("server-detail")).not.toBeInTheDocument();
  });
});

describe("server inspector", () => {
  it("states the auth mode on the card and explains it in the inspector", async () => {
    const user = userEvent.setup();
    stubCatalog();

    renderWorkspace();
    await screen.findByTestId("server-list");

    const cards = screen.getAllByTestId("server-card");
    const oauthCard = cards.find((card) => card.dataset.serverKey === "mcp-servers/workspace-assistant");
    expect(within(oauthCard as HTMLElement).getByText("OAuth")).toBeInTheDocument();

    // A server whose runtime-api build did not report a mode must not be
    // presented as unauthenticated.
    const unknownCard = cards.find((card) => card.dataset.serverKey === "mcp-servers/billing-bridge");
    expect(within(unknownCard as HTMLElement).getByText("Auth not reported")).toBeInTheDocument();

    await user.click(within(oauthCard as HTMLElement).getByTestId("server-card-details"));
    expect(await screen.findByTestId("server-detail-auth")).toHaveTextContent("bearer token");
  });

  it("offers the full MCP client config, selectable and copyable", async () => {
    const user = userEvent.setup();
    stubCatalog();

    renderWorkspace();
    await screen.findByTestId("server-list");
    await user.click(screen.getAllByTestId("server-card-details")[0]);

    const config = await screen.findByTestId("server-detail-config");
    expect(config).toHaveTextContent('"mcpServers"');
    expect(config).toHaveTextContent("workspace-assistant");
    expect(screen.getByTestId("server-detail-copy-config")).toBeInTheDocument();
  });

  it("lists every tool the server publishes in one scrollable region", async () => {
    const user = userEvent.setup();
    stubCatalog();

    renderWorkspace();
    await screen.findByTestId("server-list");

    // Narrow the catalog first: the inspector must still show all 30 tools for
    // the server, not just the ones matching the active tool filter.
    await user.type(screen.getByTestId("tool-search"), "tool_01");
    await user.click(screen.getAllByTestId("server-card-details")[0]);

    const list = await screen.findByTestId("server-detail-tools");
    expect(within(list).getAllByTestId("server-detail-tool")).toHaveLength(30);
  });

  it("says so honestly when there is no connect config", async () => {
    const user = userEvent.setup();
    stubCatalog();

    renderWorkspace();
    await screen.findByTestId("server-list");

    const cards = screen.getAllByTestId("server-card");
    const plain = cards.find((card) => card.dataset.serverKey === "mcp-servers/billing-bridge");
    await user.click(within(plain as HTMLElement).getByTestId("server-card-details"));

    expect(await screen.findByTestId("server-detail-no-config")).toBeInTheDocument();
  });
});
