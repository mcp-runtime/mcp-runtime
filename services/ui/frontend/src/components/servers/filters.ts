import { isServerReady, serverKey, type ServerSummary, type ToolRow } from "../../api/types";

export type ServerStatusFilter = "all" | "ready" | "attention";

export type ServerFilters = {
  /** Matches server metadata only. */
  search: string;
  namespace: string;
  status: ServerStatusFilter;
};

export type ToolFilters = {
  /** Matches tool metadata only. */
  search: string;
  risk: string;
  drift: string;
  /** "namespace/name" of the server the tool list is scoped to, if any. */
  serverKey: string;
};

export const EMPTY_SERVER_FILTERS: ServerFilters = { search: "", namespace: "", status: "all" };
export const EMPTY_TOOL_FILTERS: ToolFilters = { search: "", risk: "", drift: "", serverKey: "" };

function haystack(parts: Array<string | undefined>): string {
  return parts.filter(Boolean).join(" ").toLowerCase();
}

// Server search covers what a server card shows: identity, namespace, status,
// description, image, and endpoint. It never matches tool text, so the control
// can honestly be labelled "Search servers".
export function matchesServerSearch(server: ServerSummary, search: string): boolean {
  const term = search.trim().toLowerCase();
  if (!term) {
    return true;
  }
  return haystack([
    server.name,
    server.namespace,
    server.description,
    server.status,
    server.image,
    server.endpoint,
  ]).includes(term);
}

// Tool search covers tool metadata only: name, description, governance fields,
// and labels, plus the owning server so "which server is this on" still works.
export function matchesToolSearch(tool: ToolRow, search: string): boolean {
  const term = search.trim().toLowerCase();
  if (!term) {
    return true;
  }
  return haystack([
    tool.tool_name,
    tool.description,
    tool.server_name,
    tool.namespace,
    tool.required_trust,
    tool.side_effect,
    tool.risk_level,
    tool.drift_status,
    ...Object.entries(tool.labels || {}).flat(),
  ]).includes(term);
}

export function filterServers(servers: ServerSummary[], filters: ServerFilters): ServerSummary[] {
  return servers.filter((server) => {
    if (filters.namespace && server.namespace !== filters.namespace) {
      return false;
    }
    if (filters.status === "ready" && !isServerReady(server)) {
      return false;
    }
    if (filters.status === "attention" && isServerReady(server)) {
      return false;
    }
    return matchesServerSearch(server, filters.search);
  });
}

// Tools are always scoped to the servers currently in view, so a namespace or
// status filter cannot leave tools on screen whose server has been filtered out.
export function filterTools(
  tools: ToolRow[],
  filters: ToolFilters,
  visibleServerKeys: Set<string>
): ToolRow[] {
  return tools.filter((tool) => {
    const key = `${tool.namespace}/${tool.server_name}`;
    if (!visibleServerKeys.has(key)) {
      return false;
    }
    if (filters.serverKey && key !== filters.serverKey) {
      return false;
    }
    if (filters.risk && (tool.risk_level || "").toLowerCase() !== filters.risk) {
      return false;
    }
    if (filters.drift && (tool.drift_status || "").toLowerCase() !== filters.drift) {
      return false;
    }
    return matchesToolSearch(tool, filters.search);
  });
}

export function toolsInScope(tools: ToolRow[], visibleServerKeys: Set<string>): ToolRow[] {
  return tools.filter((tool) => visibleServerKeys.has(`${tool.namespace}/${tool.server_name}`));
}

export function countToolsByServer(tools: ToolRow[]): Record<string, number> {
  const counts: Record<string, number> = {};
  for (const tool of tools) {
    const key = `${tool.namespace}/${tool.server_name}`;
    counts[key] = (counts[key] || 0) + 1;
  }
  return counts;
}

// "Drift" is the two server-computed states that need attention: a declared
// tool the live inventory no longer reports (missing), and a live tool nothing
// declared (ungoverned). See services/runtime-api/internal/runtimeapi/tools.go.
export function hasDrift(tool: ToolRow): boolean {
  const drift = (tool.drift_status || "").toLowerCase();
  return drift === "missing" || drift === "ungoverned";
}

export function visibleServerKeySet(servers: ServerSummary[]): Set<string> {
  return new Set(servers.map((server) => serverKey(server)));
}
