// Response shapes for the migrated Servers workspace. Field names mirror the
// runtime API JSON tags in services/runtime-api/internal/runtimeapi.

export type NamespaceEntry = {
  namespace: string;
  is_shared?: boolean;
  is_public?: boolean;
  scope?: string;
  scope_name?: string;
  team_id?: string;
  team_name?: string;
  team_slug?: string;
};

export type InventoryItem = {
  name: string;
  description?: string;
  labels?: Record<string, string>;
};

// GET /runtime/servers's liveInventory field
// (services/runtime-api/internal/runtimeapi/live_inventory.go): what the
// server itself reported the last time its live MCP session was inspected,
// as opposed to `prompts`/`resources`/`tasks` below, which are declared on
// the MCPServer spec and may drift from what the server actually serves.
export type LiveInventory = {
  fetchedAt?: string;
  protocolVersion?: string;
  tools?: Array<{ name: string; description?: string }>;
  prompts?: InventoryItem[];
  resources?: InventoryItem[];
};

export type ObservabilityPrometheusQueryLink = {
  id: string;
  name: string;
  description?: string;
  url: string;
  query?: string;
};

// GET /runtime/servers's observability field
// (services/runtime-api/internal/runtimeapi/observability.go). Omitted by
// the backend entirely when the requesting principal can't observe this
// server (serverInfoObservableByPrincipal) - its presence is itself the
// access check, not just its contents.
export type ObservabilityLinks = {
  namespace: string;
  server: string;
  team_id?: string;
  prometheus: {
    queries: ObservabilityPrometheusQueryLink[];
    direct_admin_only: boolean;
  };
  grafana: {
    available: boolean;
    url?: string;
    direct_admin_only: boolean;
    reason?: string;
  };
};

export type ServerSummary = {
  name: string;
  namespace: string;
  uid?: string;
  team_id?: string;
  image?: string;
  description?: string;
  ready: string;
  status: string;
  age?: string;
  endpoint?: string;
  // spec.auth.mode: "oauth" | "header" | "none". Omitted by runtime-api
  // builds older than the ServerInfoFromMCPServer projection.
  authMode?: string;
  // The server's declared tools, with the governance metadata the gateway
  // enforces. Same shape as the catalog rows, scoped to this server.
  tools?: Array<{
    name?: string;
    description?: string;
    requiredTrust?: string;
    sideEffect?: string;
    riskLevel?: string;
  }>;
  prompts?: InventoryItem[];
  resources?: InventoryItem[];
  tasks?: InventoryItem[];
  liveInventory?: LiveInventory | null;
  liveInventoryError?: string;
  // The full MCP client config for this server ({"mcpServers": {...}}), only
  // present when the runtime resolved a public connect endpoint for it.
  access_json?: Record<string, unknown>;
  observability?: ObservabilityLinks;
};

// GET /runtime/servers's publish_policy field
// (services/runtime-api/internal/runtimeapi/servers.go). Only meaningful for
// a non-admin principal - the runtime does not cap admin publishing.
export type PublishPolicy = {
  active_server_limit_enabled?: boolean;
  active_server_count?: number;
  active_server_limit?: number;
};

export type ToolRow = {
  tool_name: string;
  description?: string;
  server_name: string;
  namespace: string;
  team_id?: string;
  endpoint_url?: string;
  declared: boolean;
  live: boolean;
  drift_status: string;
  required_trust?: string;
  side_effect?: string;
  risk_level?: string;
  labels?: Record<string, string>;
  // Identical to the owning server's access_json - repeated per tool so a
  // tool-level copy action doesn't need the server record in scope.
  connect_config?: Record<string, unknown>;
};

export type Principal = {
  role?: string;
  subject?: string;
  email?: string;
  auth_type?: string;
};

export type AuthStatus = {
  authenticated: boolean;
  principal?: Principal;
};

export function serverKey(server: Pick<ServerSummary, "name" | "namespace">): string {
  return `${server.namespace}/${server.name}`;
}

// Prompts and resources can be declared on the MCPServer spec, reported by
// the server's own live MCP session, both, or neither - union by name so a
// live-only or declared-only entry isn't dropped. Tasks have no live source,
// so they're declared-only.
function mergedInventoryNames(declared: InventoryItem[] | undefined, live: Array<{ name: string }> | undefined): string[] {
  const names = new Set<string>();
  for (const item of declared || []) {
    names.add(item.name);
  }
  for (const item of live || []) {
    names.add(item.name);
  }
  return Array.from(names).sort();
}

export type AuthModeInfo = {
  label: string;
  tone: "info" | "warning" | "neutral" | "unknown";
  detail: string;
};

// What a client has to present to reach this server. Unset means the runtime
// API did not report a mode, which is not the same as "no auth required".
export function authModeInfo(mode: string | undefined): AuthModeInfo {
  switch ((mode || "").trim().toLowerCase()) {
    case "oauth":
      return {
        label: "OAuth",
        tone: "info",
        detail:
          "Clients use a bearer token from the server's configured issuer. Adapters may authenticate with a session-bound workload certificate.",
      };
    case "header":
      return {
        label: "Header identity",
        tone: "neutral",
        detail:
          "The gateway reads identity from request headers. There is no token exchange, so the headers must come from a trusted hop.",
      };
    case "none":
      return {
        label: "No auth",
        tone: "warning",
        detail: "The gateway does not authenticate callers for this server.",
      };
    default:
      return {
        label: "Auth not reported",
        tone: "unknown",
        detail:
          "This runtime-api build did not report an auth mode for the server. Check the MCPServer spec.auth.mode directly.",
      };
  }
}

export function serverPrompts(server: ServerSummary): string[] {
  return mergedInventoryNames(server.prompts, server.liveInventory?.prompts);
}

export function serverResources(server: ServerSummary): string[] {
  return mergedInventoryNames(server.resources, server.liveInventory?.resources);
}

export function serverTasks(server: ServerSummary): string[] {
  return (server.tasks || []).map((item) => item.name).sort();
}

export function toolKey(tool: ToolRow): string {
  return `${tool.namespace}/${tool.server_name}/${tool.tool_name}`;
}

// "count/limit" once the runtime enforces a cap, otherwise "off":
// "off" when the limit isn't enforced, otherwise "count/limit". Callers
// gate visibility themselves - the runtime only enforces this for non-admin
// principals, so it is only meaningful (and only ever visible)
// for a tenant user.
export function formatPublishQuota(policy: PublishPolicy | null | undefined): string {
  if (!policy || policy.active_server_limit_enabled !== true) {
    return "off";
  }
  const limit = Number(policy.active_server_limit || 0);
  if (!limit) {
    return "off";
  }
  const count = Number(policy.active_server_count || 0);
  return `${count}/${limit}`;
}

// A server is "ready" when its readiness string reports every replica up.
export function isServerReady(server: ServerSummary): boolean {
  const ready = (server.ready || "").trim();
  const [current, desired] = ready.split("/");
  if (!desired) {
    return ready.toLowerCase() === "true";
  }
  return current === desired && Number(desired) > 0;
}

// --- Phase 4: admin governance and operations -------------------------------

export type SubjectRef = {
  humanID?: string;
  agentID?: string;
  teamID?: string;
};

export type ServerRef = {
  name?: string;
  namespace?: string;
};

export type GrantSummary = {
  name: string;
  namespace: string;
  serverRef?: ServerRef;
  subject?: SubjectRef;
  maxTrust?: string;
  allowedSideEffects?: string[];
  expiresAt?: string;
  disabled: boolean;
  age?: string;
};

export type SessionSummary = {
  name: string;
  namespace: string;
  serverRef?: ServerRef;
  subject?: SubjectRef;
  consentedTrust?: string;
  revoked: boolean;
  expiresAt?: string;
  age?: string;
};

export type TeamRecord = {
  id: string;
  slug: string;
  name: string;
  namespace: string;
  created_at?: string;
};

export type TeamMembership = {
  team_id?: string;
  team_slug?: string;
  team_name?: string;
  team_namespace?: string;
  user_id: string;
  email?: string;
  role: string;
  created_at?: string;
  id?: string;
  slug?: string;
  name?: string;
  namespace?: string;
};

export type AgentRecord = {
  id: string;
  team_id: string;
  team_slug: string;
  name: string;
  status: "active" | "inactive";
  created_at?: string;
  updated_at?: string;
};

export type AgentPage = {
  agents: AgentRecord[];
  next_cursor?: string;
};

export type ComponentStatus = {
  key: string;
  display: string;
  namespace: string;
  kind: string;
  resource: string;
  status: string;
  ready: string;
  message?: string;
};

export type UserActivity = {
  id: string;
  email: string;
  role: string;
  namespace?: string;
  last_login_at?: string;
  last_activity_at?: string;
  login_count: number;
  failed_action_count: number;
  registry_credentials: number;
  api_keys: number;
};

export type AuditLogEntry = {
  user_id?: string;
  action: string;
  resource: string;
  namespace?: string;
  status: string;
  message?: string;
  actor_ip?: string;
  source?: string;
  auth_identity?: string;
  image_ref?: string;
  server_name?: string;
  created_at?: string;
};

export type ImageActivity = {
  email?: string;
  namespace?: string;
  image_ref: string;
  server_name?: string;
  deployment_target?: string;
  action: string;
  status: string;
  created_at?: string;
};

export type AdminOperations = {
  users: UserActivity[];
  audit_logs: AuditLogEntry[];
  images: ImageActivity[];
};

export type GatewayEvent = {
  timestamp?: string;
  namespace?: string;
  tool_name?: string;
  decision?: string;
  source?: string;
  event_type?: string;
  payload?: Record<string, unknown>;
};

// The authenticated principal returned by the server is the only source of
// truth for admin access. Anything else must fail closed.
export function isAdmin(status: AuthStatus): boolean {
  return status.authenticated === true && status.principal?.role === "admin";
}

export function subjectLabel(subject: SubjectRef | undefined): string {
  if (!subject) {
    return "—";
  }
  return [subject.humanID, subject.agentID, subject.teamID].filter(Boolean).join(" / ") || "—";
}

export function accessKey(item: { name: string; namespace: string }): string {
  return `${item.namespace}/${item.name}`;
}

// --- Phase 3: user workflows -------------------------------------------------

// Mirrors platformclient.APIKeySummary. The raw key value is deliberately not
// part of this type: it exists only in the one-time create response.
export type UserAPIKey = {
  id: string;
  name: string;
  prefix: string;
  created_at: string;
  revoked: boolean;
  revoked_at?: string;
};

export type UsageTotals = {
  events: number;
  allowed: number;
  denied: number;
  unique_servers: number;
  unique_humans: number;
  unique_agents: number;
  unique_sessions?: number;
};

export type ServerUsage = {
  server: string;
  namespace: string;
  team_id?: string;
  events: number;
  allowed: number;
  denied: number;
  unique_humans: number;
  unique_agents: number;
  last_seen?: string;
};

export type ToolUsage = {
  server: string;
  tool_name: string;
  human_id: string;
  team_id: string;
  agent_id: string;
  events: number;
  denied: number;
  last_seen?: string;
};

export type UsageResponse = {
  totals: UsageTotals;
  servers: ServerUsage[];
  tools: ToolUsage[];
  window_days?: number;
  actors?: Array<{ human_id: string; agent_id: string; events: number; unique_servers: number; unique_tools: number; denied: number }>;
  decisions?: Array<{ decision: string; events: number }>;
  series?: UsageTimePoint[];
  recent?: RecentActivity[];
  filters?: {
    namespaces?: string[];
    team_ids?: string[];
    server?: string;
    decision?: string;
    tool_name?: string;
  };
};

export type UsageTimePoint = {
  bucket: string;
  events: number;
  allowed: number;
  denied: number;
};

export type RecentActivity = {
  timestamp: string;
  server?: string;
  namespace?: string;
  human_id?: string;
  agent_id?: string;
  session_id?: string;
  decision?: string;
  tool_name?: string;
  event_type?: string;
};

// Role gating helpers shared across every workspace.
// Activity is tenant-only; API keys additionally require a user identity.
export function isAdminPrincipal(status: AuthStatus): boolean {
  return status.authenticated && status.principal?.role === "admin";
}

export function isTenantUser(status: AuthStatus): boolean {
  return status.authenticated && !isAdminPrincipal(status);
}

export function hasUserIdentity(status: AuthStatus): boolean {
  return status.authenticated && (status.principal?.subject || "").trim() !== "";
}
