import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { axe } from "vitest-axe";

import { ServersWorkspace } from "./ServersWorkspace";
import { AppProviders } from "../../providers/AppProviders";
import type { AuthStatus } from "../../api/types";

function renderWorkspace(props: { authenticated: boolean; onSignIn?: () => void; auth?: AuthStatus }) {
  const auth: AuthStatus = props.auth ?? {
    authenticated: props.authenticated,
    principal: props.authenticated ? { role: "user", email: "test@mcpruntime.org" } : undefined,
  };
  return render(
    <AppProviders>
      <ServersWorkspace auth={auth} onSignIn={props.onSignIn ?? (() => {})} />
    </AppProviders>
  );
}

const NAMESPACES = { namespaces: [{ namespace: "mcp-servers" }, { namespace: "mcp-shared" }] };

const SERVERS = {
  servers: [
    {
      name: "workspace-assistant",
      namespace: "mcp-servers",
      ready: "1/1",
      status: "Ready",
      description: "Workspace helper",
      endpoint: "http://localhost:18080/oauth-example-go-2025-11-25/mcp",
    },
    {
      name: "degraded-server",
      namespace: "mcp-servers",
      ready: "0/1",
      status: "Pending",
    },
  ],
};

const TOOLS = {
  tools: [
    {
      tool_name: "add",
      description: "Add two numeric values",
      server_name: "workspace-assistant",
      namespace: "mcp-servers",
      declared: true,
      live: true,
      drift_status: "declared",
      required_trust: "low",
      side_effect: "read",
      risk_level: "low",
    },
    {
      tool_name: "purge_workspace",
      description: "Delete every workspace record",
      server_name: "workspace-assistant",
      namespace: "mcp-servers",
      declared: true,
      live: false,
      drift_status: "missing",
      required_trust: "high",
      side_effect: "destructive",
      risk_level: "high",
    },
    {
      tool_name: "probe",
      server_name: "degraded-server",
      namespace: "mcp-servers",
      declared: false,
      live: true,
      drift_status: "ungoverned",
    },
  ],
};

function stubCatalog(overrides: Record<string, unknown> = {}) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    const payload = url.includes("/runtime/namespaces")
      ? overrides.namespaces ?? NAMESPACES
      : url.includes("/runtime/servers")
        ? overrides.servers ?? SERVERS
        : overrides.tools ?? TOOLS;
    return { ok: true, status: 200, json: async () => payload } as unknown as Response;
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

beforeEach(() => {
  delete window.MCP_API_BASE;
  delete window.MCP_PLATFORM_MODE;
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("ServersWorkspace", () => {
  it("shows a sign-in prompt and fetches nothing while signed out", () => {
    const fetchMock = stubCatalog();

    renderWorkspace({ authenticated: false });

    expect(screen.getByTestId("catalog-signed-out")).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("renders a loading state before the catalog resolves", () => {
    stubCatalog();

    renderWorkspace({ authenticated: true });

    expect(screen.getByTestId("catalog-loading")).toBeInTheDocument();
  });

  it("reads the catalog through the session-backed UI proxy", async () => {
    const fetchMock = stubCatalog();

    renderWorkspace({ authenticated: true });
    await screen.findByTestId("server-list");

    const urls = fetchMock.mock.calls.map((call) => String(call[0]));
    expect(urls).toEqual(
      expect.arrayContaining([
        "/api/ui/v1/runtime/namespaces",
        "/api/ui/v1/runtime/servers",
        "/api/ui/v1/runtime/tools",
      ])
    );
    for (const call of fetchMock.mock.calls) {
      const init = call[1] as RequestInit;
      expect(init.credentials).toBe("same-origin");
      const headers = new Headers(init.headers);
      expect(headers.get("authorization")).toBeNull();
      expect(headers.get("x-api-key")).toBeNull();
    }
  });

  it("renders servers and the tool table on success", async () => {
    stubCatalog();

    renderWorkspace({ authenticated: true });

    expect(await screen.findAllByTestId("server-card")).toHaveLength(2);
    expect(screen.getAllByTestId("tool-row")).toHaveLength(3);
    expect(screen.getByTestId("catalog-summary")).toHaveTextContent("3 tools");
    expect(screen.getByTestId("server-stats")).toHaveTextContent("2");

    const table = screen.getByTestId("tool-table");
    const headers = within(table)
      .getAllByRole("columnheader")
      .map((cell) => (cell.textContent || "").split(",")[0]);
    expect(headers).toEqual(["Tool", "Server", "Trust", "Side effect", "Risk", "Drift"]);
    for (const header of within(table).getAllByRole("columnheader")) {
      expect(header).toHaveAttribute("aria-sort", "none");
    }
    expect(within(table).getByText("Add two numeric values")).toBeInTheDocument();
    expect(within(table).getByText("ungoverned")).toBeInTheDocument();
  });

  it("renders an empty state when no servers or tools exist", async () => {
    stubCatalog({ servers: { servers: [] }, tools: { tools: [] } });

    renderWorkspace({ authenticated: true });

    expect(await screen.findByTestId("server-list-empty")).toBeInTheDocument();
    expect(screen.getByTestId("tool-table-empty")).toHaveTextContent(
      "No tools are published in this scope."
    );
  });

  it("renders an error state and retries on demand", async () => {
    const user = userEvent.setup();
    let failing = true;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        if (failing) {
          return { ok: false, status: 500, text: async () => "upstream exploded" } as unknown as Response;
        }
        const url = String(input);
        const payload = url.includes("namespaces")
          ? NAMESPACES
          : url.includes("servers")
            ? SERVERS
            : TOOLS;
        return { ok: true, status: 200, json: async () => payload } as unknown as Response;
      })
    );

    renderWorkspace({ authenticated: true });

    // TanStack Query retries once with backoff before the error surfaces.
    const error = await screen.findByTestId("catalog-error", {}, { timeout: 5000 });
    expect(error).toHaveTextContent("The server catalog could not be loaded.");
    expect(error).toHaveTextContent("upstream exploded");
    expect(error).toHaveAttribute("role", "alert");

    failing = false;
    await user.click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(screen.getByTestId("server-list")).toBeInTheDocument(), {
      timeout: 5000,
    });
  });

  it("renders a session-expired state on 401", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ({ ok: false, status: 401, text: async () => "unauthorized" }) as unknown as Response)
    );

    renderWorkspace({ authenticated: true });

    expect(await screen.findByTestId("catalog-unauthorized")).toHaveTextContent(
      "Your session expired."
    );
  });

  it("filters the rendered tool rows by search term", async () => {
    const user = userEvent.setup();
    stubCatalog();

    renderWorkspace({ authenticated: true });
    await screen.findByTestId("server-list");

    await user.clear(screen.getByTestId("tool-search"));
    await user.type(screen.getByTestId("tool-search"), "purge");
    expect(screen.getAllByTestId("tool-row")).toHaveLength(1);
    expect(screen.getByTestId("catalog-summary")).toHaveTextContent("1 of 3 tools");

    await user.clear(screen.getByTestId("tool-search"));
    await user.type(screen.getByTestId("tool-search"), "zzz-no-match");
    expect(screen.queryAllByTestId("tool-row")).toHaveLength(0);
    expect(screen.getByTestId("tool-table-empty")).toHaveTextContent(
      "No tools match these filters."
    );
  });

  it("filters tools by risk level", async () => {
    const user = userEvent.setup();
    stubCatalog();

    renderWorkspace({ authenticated: true });
    await screen.findByTestId("server-list");

    await user.selectOptions(screen.getByTestId("tool-risk-filter"), "high");
    expect(screen.getAllByTestId("tool-row")).toHaveLength(1);
    expect(screen.getByTestId("tool-table")).toHaveTextContent("purge_workspace");
  });

  it("narrows the catalog when a server is selected", async () => {
    const user = userEvent.setup();
    stubCatalog();

    renderWorkspace({ authenticated: true });
    await screen.findByTestId("server-list");
    expect(screen.getAllByTestId("tool-row")).toHaveLength(3);

    const [firstCard] = screen.getAllByTestId("server-card");
    await user.click(within(firstCard).getByTestId("server-card-select"));

    expect(screen.getAllByTestId("tool-row")).toHaveLength(2);
    expect(screen.getByTestId("tool-table")).not.toHaveTextContent("probe");
    expect(within(firstCard).getByTestId("server-card-select")).toHaveAttribute(
      "aria-pressed",
      "true"
    );
  });

  it("hides servers that are not ready behind the status filter", async () => {
    const user = userEvent.setup();
    stubCatalog();

    renderWorkspace({ authenticated: true });
    await screen.findByTestId("server-list");

    await user.click(screen.getByTestId("server-status-ready"));
    expect(screen.getAllByTestId("server-card")).toHaveLength(1);
    expect(screen.getAllByTestId("tool-row")).toHaveLength(2);
  });

  it("opens and closes the selected tool detail", async () => {
    const user = userEvent.setup();
    stubCatalog();

    renderWorkspace({ authenticated: true });
    await screen.findByTestId("server-list");

    await user.click(screen.getAllByTestId("tool-row-select")[1]);
    const detail = screen.getByTestId("tool-detail");
    expect(detail).toHaveTextContent("purge_workspace");
    expect(detail).toHaveTextContent("destructive");

    await user.click(screen.getByTestId("tool-detail-close"));
    expect(screen.queryByTestId("tool-detail")).not.toBeInTheDocument();
  });

  it("refetches scoped to the namespace when the namespace filter changes", async () => {
    const user = userEvent.setup();
    const fetchMock = stubCatalog();

    renderWorkspace({ authenticated: true });
    await screen.findByTestId("server-list");

    await user.selectOptions(screen.getByTestId("namespace-filter"), "mcp-shared");

    await waitFor(() => {
      const urls = fetchMock.mock.calls.map((call) => String(call[0]));
      expect(urls).toContain("/api/ui/v1/runtime/servers?namespace=mcp-shared");
      expect(urls).toContain("/api/ui/v1/runtime/tools?namespace=mcp-shared");
    });
  });

  it("sorts the table when a column header is activated", async () => {
    const user = userEvent.setup();
    stubCatalog();

    renderWorkspace({ authenticated: true });
    await screen.findByTestId("server-list");

    const names = () =>
      screen.getAllByTestId("tool-row-select").map((button) => button.textContent);
    expect(names()).toEqual(["add", "purge_workspace", "probe"]);

    await user.click(screen.getByTestId("tool-sort-tool_name"));
    expect(names()).toEqual(["add", "probe", "purge_workspace"]);
    expect(screen.getAllByRole("columnheader")[0]).toHaveAttribute("aria-sort", "ascending");

    await user.click(screen.getByTestId("tool-sort-tool_name"));
    expect(names()).toEqual(["purge_workspace", "probe", "add"]);
    expect(screen.getAllByRole("columnheader")[0]).toHaveAttribute("aria-sort", "descending");
  });

  it("sorts risk by severity rather than alphabetically", async () => {
    const user = userEvent.setup();
    stubCatalog();

    renderWorkspace({ authenticated: true });
    await screen.findByTestId("server-list");

    await user.click(screen.getByTestId("tool-sort-risk_level"));
    const risks = screen
      .getAllByTestId("tool-row")
      .map((row) => row.cells[4].textContent);
    // "" (unrated) then low then high - never alphabetical "high" before "low".
    expect(risks).toEqual(["unrated", "low", "high"]);
  });
});

// The Servers tab shows a tenant-only "count/limit" (or "off") publish
// quota stat, sourced from GET /runtime/servers's publish_policy field
// (services/runtime-api/internal/runtimeapi/servers.go). The runtime does
// not enforce this for admin, so admin never sees it either.
describe("ServersWorkspace publish quota", () => {
  it("shows a tenant's publish quota once the limit is enabled", async () => {
    stubCatalog({
      servers: {
        ...SERVERS,
        publish_policy: {
          active_server_limit_enabled: true,
          active_server_count: 2,
          active_server_limit: 5,
        },
      },
    });

    renderWorkspace({
      authenticated: true,
      auth: { authenticated: true, principal: { role: "user", email: "test@mcpruntime.org" } },
    });
    await screen.findByTestId("server-list");

    expect(screen.getByTestId("server-quota")).toHaveTextContent("2/5");
  });

  it("shows \"off\" when the limit isn't enforced", async () => {
    stubCatalog({ servers: { ...SERVERS, publish_policy: { active_server_limit_enabled: false } } });

    renderWorkspace({
      authenticated: true,
      auth: { authenticated: true, principal: { role: "user", email: "test@mcpruntime.org" } },
    });
    await screen.findByTestId("server-list");

    expect(screen.getByTestId("server-quota")).toHaveTextContent("off");
  });

  it("never shows the quota stat to an admin", async () => {
    stubCatalog({
      servers: {
        ...SERVERS,
        publish_policy: {
          active_server_limit_enabled: true,
          active_server_count: 2,
          active_server_limit: 5,
        },
      },
    });

    renderWorkspace({
      authenticated: true,
      auth: { authenticated: true, principal: { role: "admin", email: "admin@mcpruntime.org" } },
    });
    await screen.findByTestId("server-list");

    expect(screen.queryByTestId("server-quota")).not.toBeInTheDocument();
  });
});

// GET /runtime/servers/{ns}/{name} and DELETE both go through
// rr.auth (services/runtime-api/routes.go), not adminOnly - any principal
// who owns/can-publish the namespace can retire their own server, not only
// admin. handleRuntimeServerDelete enforces that ownership check itself.
describe("ServersWorkspace server retire", () => {
  it("confirms with the exact namespace and name before retiring", async () => {
    const user = userEvent.setup();
    const fetchMock = stubCatalog();


    renderWorkspace({ authenticated: true });
    await screen.findByTestId("server-list");

    await user.click(screen.getAllByTestId("server-card-retire")[0]);

    // An accessible dialog rather than window.confirm, but it still names the
    // server and the namespace and still blocks the write until confirmed.
    const dialog = await screen.findByTestId("server-retire-confirm");
    expect(dialog).toHaveTextContent("workspace-assistant");
    expect(dialog).toHaveTextContent("mcp-servers");
    expect(fetchMock.mock.calls.some((call) => (call[1] as RequestInit)?.method === "DELETE")).toBe(
      false
    );
  });

  it("retires through the CSRF-backed proxy and refreshes the catalog on success", async () => {
    const user = userEvent.setup();
    let retired = false;
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input);
      if (init.method === "DELETE" && url.includes("/runtime/servers/")) {
        retired = true;
        return { ok: true, status: 200, json: async () => ({ success: true }) } as unknown as Response;
      }
      const payload = url.includes("/runtime/namespaces")
        ? NAMESPACES
        : url.includes("/runtime/servers")
          ? retired
            ? { servers: [] }
            : SERVERS
          : TOOLS;
      return { ok: true, status: 200, json: async () => payload } as unknown as Response;
    });
    vi.stubGlobal("fetch", fetchMock);


    renderWorkspace({ authenticated: true });
    await screen.findByTestId("server-list");
    expect(screen.getAllByTestId("server-card")).toHaveLength(2);

    await user.click(screen.getAllByTestId("server-card-retire")[0]);
    await user.click(await screen.findByTestId("server-retire-confirm-yes"));

    await waitFor(() => expect(screen.getByTestId("server-list-empty")).toBeInTheDocument());

    const deleteCall = fetchMock.mock.calls.find(
      (call) => (call[1] as RequestInit)?.method === "DELETE"
    );
    expect(String(deleteCall?.[0])).toBe("/api/ui/v1/runtime/servers/mcp-servers/workspace-assistant");
    const init = deleteCall?.[1] as RequestInit;
    expect(init.credentials).toBe("same-origin");
    expect(new Headers(init.headers).get("x-api-key")).toBeNull();
  });

  it("clears a stale selected-server filter when the retired server was the active filter", async () => {
    const user = userEvent.setup();
    let retired = false;
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input);
      if (init.method === "DELETE" && url.includes("/runtime/servers/")) {
        retired = true;
        return { ok: true, status: 200, json: async () => ({ success: true }) } as unknown as Response;
      }
      const payload = url.includes("/runtime/namespaces")
        ? NAMESPACES
        : url.includes("/runtime/servers")
          ? retired
            ? { servers: [SERVERS.servers[1]] }
            : SERVERS
          : retired
            ? { tools: [TOOLS.tools[2]] }
            : TOOLS;
      return { ok: true, status: 200, json: async () => payload } as unknown as Response;
    });
    vi.stubGlobal("fetch", fetchMock);


    renderWorkspace({ authenticated: true });
    await screen.findByTestId("server-list");

    const [firstCard] = screen.getAllByTestId("server-card");
    await user.click(within(firstCard).getByTestId("server-card-select"));
    expect(screen.getAllByTestId("tool-row")).toHaveLength(2);

    await user.click(within(firstCard).getByTestId("server-card-retire"));
    await user.click(await screen.findByTestId("server-retire-confirm-yes"));

    await waitFor(() => expect(screen.getAllByTestId("server-card")).toHaveLength(1));
    // Previously the stale selectedServerKey (the retired server's) kept
    // filtering every remaining tool out, leaving an apparently empty
    // catalog even though degraded-server's "probe" tool is still there.
    expect(screen.getByTestId("tool-table")).toHaveTextContent("probe");
    expect(screen.getAllByTestId("tool-row")).toHaveLength(1);
  });

  it("shows an inline error and keeps the server listed when retire fails", async () => {
    const user = userEvent.setup();
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input);
      if (init.method === "DELETE") {
        return {
          ok: false,
          status: 403,
          text: async () => JSON.stringify({ error: "server is not owned by this user" }),
        } as unknown as Response;
      }
      const payload = url.includes("/runtime/namespaces")
        ? NAMESPACES
        : url.includes("/runtime/servers")
          ? SERVERS
          : TOOLS;
      return { ok: true, status: 200, json: async () => payload } as unknown as Response;
    });
    vi.stubGlobal("fetch", fetchMock);


    renderWorkspace({ authenticated: true });
    await screen.findByTestId("server-list");

    await user.click(screen.getAllByTestId("server-card-retire")[0]);
    await user.click(await screen.findByTestId("server-retire-confirm-yes"));

    const error = await screen.findByTestId("server-retire-error");
    expect(error).toHaveTextContent("server is not owned by this user");
    expect(error).toHaveAttribute("role", "alert");
    expect(screen.getAllByTestId("server-card")).toHaveLength(2);
  });

  it("does not call the API when the confirmation is declined", async () => {
    const user = userEvent.setup();
    const fetchMock = stubCatalog();

    renderWorkspace({ authenticated: true });
    await screen.findByTestId("server-list");
    fetchMock.mockClear();

    await user.click(screen.getAllByTestId("server-card-retire")[0]);
    await user.click(await screen.findByTestId("server-retire-confirm-cancel"));

    expect(screen.queryByTestId("server-retire-confirm")).not.toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
    expect(screen.getAllByTestId("server-card")).toHaveLength(2);
  });
});

describe("ServersWorkspace connect config, protocol inventory, and observability", () => {
  const dashboardURL = "/grafana/d/mcp-server/mcp-server?var-namespace=mcp-servers&var-server=workspace-assistant";
  const RICH_SERVERS = {
    servers: [
      {
        ...SERVERS.servers[0],
        access_json: { mcpServers: { "workspace-assistant": { type: "http", url: "http://x/mcp" } } },
        prompts: [{ name: "summarize", description: "Summarize workspace notes", labels: { category: "writing" } }],
        resources: [{ name: "workspace-doc", description: "Workspace documentation" }],
        tasks: [{ name: "nightly-sync", description: "Prepare the nightly sync" }],
        liveInventory: {
          prompts: [
            { name: "summarize", description: "Summarize current workspace", arguments: [{ name: "topic", description: "Topic to summarize", required: true }] },
            { name: "translate", description: "Translate text" },
          ],
          resources: [{ name: "workspace-doc", uri: "file:///workspace/doc.md", mimeType: "text/markdown" }],
        },
        observability: {
          namespace: "mcp-servers",
          server: "workspace-assistant",
          prometheus: {
            queries: [
              { id: "up", name: "Target health", url: "http://prom/query?up", grafana_url: `${dashboardURL}&viewPanel=1` },
              { id: "request_rate", name: "Request rate", url: "http://prom/query?request_rate", grafana_url: `${dashboardURL}&viewPanel=2` },
              { id: "deny_rate", name: "Deny rate", url: "http://prom/query?deny_rate", grafana_url: `${dashboardURL}&viewPanel=3` },
              { id: "latency_p95", name: "p95 latency", url: "http://prom/query?latency_p95", grafana_url: `${dashboardURL}&viewPanel=4` },
            ],
            direct_admin_only: true,
          },
          grafana: { available: true, url: dashboardURL, direct_admin_only: true },
        },
      },
      SERVERS.servers[1],
    ],
  };

  function stubRichCatalog() {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      const payload = url.includes("/runtime/namespaces")
        ? NAMESPACES
        : url.includes("/runtime/servers")
          ? RICH_SERVERS
          : TOOLS;
      return { ok: true, status: 200, json: async () => payload } as unknown as Response;
    });
    vi.stubGlobal("fetch", fetchMock);
    return fetchMock;
  }

  it("copies the connect config to the clipboard and offers it only when access_json is present", async () => {
    const user = userEvent.setup();
    stubRichCatalog();
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText },
      configurable: true,
    });

    renderWorkspace({ authenticated: true });
    await screen.findByTestId("server-list");

    const [richCard, plainCard] = screen.getAllByTestId("server-card");
    expect(within(plainCard).queryByTestId("server-card-copy-connect-config")).not.toBeInTheDocument();

    await user.click(within(richCard).getByTestId("server-card-copy-connect-config"));

    expect(writeText).toHaveBeenCalledWith(
      JSON.stringify(RICH_SERVERS.servers[0].access_json, null, 2)
    );
    expect(within(richCard).getByTestId("server-card-copy-connect-config")).toHaveTextContent("Copied");
  });

  it("shows the merged declared/live protocol inventory and hides it when there is none", async () => {
    const user = userEvent.setup();
    stubRichCatalog();

    renderWorkspace({ authenticated: true });
    await screen.findByTestId("server-list");

    const [richCard, plainCard] = screen.getAllByTestId("server-card");
    expect(within(plainCard).queryByTestId("server-card-inventory")).not.toBeInTheDocument();

    const inventory = within(richCard).getByTestId("server-card-inventory");
    await user.click(within(inventory).getByText(/Protocol inventory/));
    // Declared "summarize" and live "summarize" appear as one item.
    expect(within(inventory).getAllByRole("button")).toHaveLength(4);
    const summarize = within(inventory).getByRole("button", { name: /summarize/i });
    expect(summarize).toHaveTextContent("Summarize current workspace");
    await user.click(summarize);

    const details = await screen.findByTestId("server-detail-inventory");
    const selected = within(details).getByText("summarize").closest("details");
    expect(selected).toHaveAttribute("open");
    expect(selected).toHaveTextContent("topic (required)");
    expect(selected).toHaveTextContent("category: writing");
    expect(details).toHaveTextContent("file:///workspace/doc.md");
    expect(details).toHaveTextContent("Prepare the nightly sync");
    expect(within(details).queryByRole("button", { name: /download/i })).not.toBeInTheDocument();
    expect(selected).toHaveTextContent("Server metadata and latest probe");
    await user.click(within(selected as HTMLElement).getByText("summarize"));
    expect(selected).not.toHaveAttribute("open");
  });

  it("opens resource and task details from the server card", async () => {
    const user = userEvent.setup();
    stubRichCatalog();
    renderWorkspace({ authenticated: true });
    const [richCard] = await screen.findAllByTestId("server-card");
    const inventory = within(richCard).getByTestId("server-card-inventory");
    await user.click(within(inventory).getByText(/Protocol inventory/));

    await user.click(within(inventory).getByRole("button", { name: /workspace-doc/i }));
    let details = await screen.findByTestId("server-detail-inventory");
    let selected = within(details).getByText("workspace-doc").closest("details");
    expect(selected).toHaveAttribute("open");
    expect(selected).toHaveTextContent("Workspace documentation");
    expect(selected).toHaveTextContent("file:///workspace/doc.md");
    expect(selected).toHaveTextContent("text/markdown");
    expect(selected).toHaveTextContent("Server metadata and latest probe");

    await user.click(screen.getByTestId("server-detail-close"));
    await user.click(within(inventory).getByRole("button", { name: /nightly-sync/i }));
    details = await screen.findByTestId("server-detail-inventory");
    selected = within(details).getByText("nightly-sync").closest("details");
    expect(selected).toHaveAttribute("open");
    expect(selected).toHaveTextContent("Prepare the nightly sync");
    expect(selected).toHaveTextContent("Server metadata");
    expect(selected).not.toHaveTextContent("Latest probe");
  });

  it("shows live-only prompts and keeps the unselected detail entries collapsed", async () => {
    const user = userEvent.setup();
    stubRichCatalog();
    renderWorkspace({ authenticated: true });
    const [richCard] = await screen.findAllByTestId("server-card");
    await user.click(within(richCard).getByTestId("server-card-details"));

    const details = await screen.findByTestId("server-detail-inventory");
    const translate = within(details).getByText("translate").closest("details");
    const summarize = within(details).getByText("summarize").closest("details");
    expect(translate).not.toHaveAttribute("open");
    expect(summarize).not.toHaveAttribute("open");
    await user.click(within(translate as HTMLElement).getByText("translate"));
    expect(translate).toHaveAttribute("open");
    expect(translate).toHaveTextContent("Latest probe");
    expect(translate).toHaveTextContent("Translate text");
  });

  it("keeps inventory disclosures accessible by name and keyboard", async () => {
    const user = userEvent.setup();
    stubRichCatalog();
    const { container } = renderWorkspace({ authenticated: true });
    const [richCard] = await screen.findAllByTestId("server-card");
    await user.click(within(richCard).getByText(/Protocol inventory/));
    const prompt = within(richCard).getByRole("button", { name: /summarize/i });
    prompt.focus();
    await user.keyboard("{Enter}");

    const details = await screen.findByTestId("server-detail-inventory");
    expect(within(details).getByText("summarize").closest("details")).toHaveAttribute("open");
    expect(await axe(container, { rules: { "color-contrast": { enabled: false } } })).toHaveNoViolations();
  });

  it("opens the dashboard and all four metrics in real Grafana panels", async () => {
    stubRichCatalog();

    renderWorkspace({ authenticated: true, auth: { authenticated: true, principal: { role: "admin" } } });
    await screen.findByTestId("server-list");

    const [richCard, plainCard] = screen.getAllByTestId("server-card");
    expect(within(plainCard).queryByTestId("server-card-observability")).not.toBeInTheDocument();

    const observability = within(richCard).getByTestId("server-card-observability");
    const grafanaLink = within(observability).getByTestId("server-card-grafana-link");
    expect(grafanaLink).toHaveAttribute("href", dashboardURL);
    for (const [name, panel] of [["Target health", 1], ["Request rate", 2], ["Deny rate", 3], ["p95 latency", 4]] as const) {
      const link = within(observability).getByRole("link", { name: new RegExp(name, "i") });
      expect(link).toHaveAttribute("href", `${dashboardURL}&viewPanel=${panel}`);
      expect(link).toHaveAttribute("target", "_blank");
      expect(link).toHaveAttribute("rel", "noreferrer");
    }
    expect(within(observability).getAllByRole("link")).toHaveLength(5);
    expect(observability.innerHTML).not.toContain("http://prom/query");
  });

  it("does not render Grafana or raw Prometheus query links when Grafana access is unavailable", async () => {
    const server = RICH_SERVERS.servers[0];
    stubCatalog({ servers: { servers: [{
      ...server,
      observability: {
        ...server.observability,
        grafana: { available: false, direct_admin_only: true, reason: "admin only" },
        prometheus: {
          ...server.observability.prometheus,
          queries: server.observability.prometheus.queries.map(({ grafana_url: _grafanaURL, ...query }) => query),
        },
      },
    }, RICH_SERVERS.servers[1]] } });

    renderWorkspace({ authenticated: true });
    const [richCard] = await screen.findAllByTestId("server-card");
    expect(within(richCard).queryByTestId("server-card-observability")).not.toBeInTheDocument();
    expect(within(richCard).queryByRole("link", { name: /grafana|target health|request rate|deny rate|p95 latency/i })).not.toBeInTheDocument();
  });

  it("drops panel deep links when the Grafana dashboard target is unavailable", async () => {
    const server = RICH_SERVERS.servers[0];
    stubCatalog({ servers: { servers: [{
      ...server,
      observability: {
        ...server.observability,
        grafana: { available: false, direct_admin_only: true, reason: "dashboard not provisioned" },
      },
    }, RICH_SERVERS.servers[1]] } });

    renderWorkspace({ authenticated: true, auth: { authenticated: true, principal: { role: "admin" } } });
    const [richCard] = await screen.findAllByTestId("server-card");
    expect(within(richCard).queryByTestId("server-card-observability")).not.toBeInTheDocument();
    expect(within(richCard).queryByRole("link", { name: /grafana|target health|request rate|deny rate|p95 latency/i })).not.toBeInTheDocument();
  });
});

describe("ServersWorkspace public-mode anonymous browsing", () => {
  function stubPublicCatalog() {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (!url.startsWith("/api/public/v1/")) {
        throw new Error(`unexpected authenticated fetch in public mode: ${url}`);
      }
      const payload = url.includes("/servers") ? SERVERS : TOOLS;
      return { ok: true, status: 200, json: async () => payload } as unknown as Response;
    });
    vi.stubGlobal("fetch", fetchMock);
    return fetchMock;
  }

  it("still shows the sign-in prompt outside public mode", () => {
    const fetchMock = stubCatalog();

    renderWorkspace({ authenticated: false });

    expect(screen.getByTestId("catalog-signed-out")).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("browses the catalog anonymously in public mode, with no credentials and no retire action", async () => {
    window.MCP_PLATFORM_MODE = "public";
    const fetchMock = stubPublicCatalog();

    renderWorkspace({ authenticated: false });

    await screen.findByTestId("server-list");
    expect(screen.getAllByTestId("server-card")).toHaveLength(2);
    expect(screen.getAllByTestId("tool-row")).toHaveLength(3);
    expect(screen.queryByTestId("server-card-retire")).not.toBeInTheDocument();
    expect(screen.getByTestId("public-catalog-sign-in")).toBeInTheDocument();

    for (const call of fetchMock.mock.calls) {
      const init = call[1] as RequestInit | undefined;
      expect(init?.credentials).not.toBe("same-origin");
      expect(new Headers(init?.headers).get("x-api-key")).toBeNull();
    }
  });

  it("narrows the public tool catalog when a server is selected", async () => {
    window.MCP_PLATFORM_MODE = "public";
    stubPublicCatalog();
    const user = userEvent.setup();

    renderWorkspace({ authenticated: false });
    await screen.findByTestId("server-list");
    expect(screen.getAllByTestId("tool-row")).toHaveLength(3);

    const [firstCard] = screen.getAllByTestId("server-card");
    await user.click(within(firstCard).getByTestId("server-card-select"));

    expect(screen.getAllByTestId("tool-row")).toHaveLength(2);
    expect(screen.getByTestId("tool-table")).not.toHaveTextContent("probe");
  });

  it("surfaces a load failure instead of a blank public catalog", async () => {
    window.MCP_PLATFORM_MODE = "public";
    const fetchMock = vi.fn(async () => ({ ok: false, status: 502, text: async () => "upstream_error" }) as unknown as Response);
    vi.stubGlobal("fetch", fetchMock);

    renderWorkspace({ authenticated: false });

    // TanStack Query retries once with backoff before the error surfaces.
    expect(await screen.findByTestId("public-catalog-error", {}, { timeout: 5000 })).toBeInTheDocument();
  });
});
