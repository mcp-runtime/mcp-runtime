import { fetchJSON, withQuery } from "./client";
import type { TeamMembership, UsageResponse, UserAPIKey } from "./types";

// Phase 3 reads and writes. Every call goes through the session-backed proxy;
// the cookie is the only credential and the CSRF token is attached by the
// client module on unsafe methods.

function asArray<T>(value: unknown, key: string): T[] {
  if (!value || typeof value !== "object") {
    return [];
  }
  const items = (value as Record<string, unknown>)[key];
  return Array.isArray(items) ? (items as T[]) : [];
}

export async function listUserAPIKeys(): Promise<UserAPIKey[]> {
  return asArray<UserAPIKey>(await fetchJSON("/user/api-keys"), "keys");
}

export type CreatedAPIKey = {
  key: UserAPIKey;
  // The cleartext value, returned exactly once by the API and never stored.
  oneTimeKey: string;
};

export async function createUserAPIKey(name: string): Promise<CreatedAPIKey> {
  const body = await fetchJSON("/user/api-keys", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ name }),
  });
  const record = (body || {}) as Record<string, unknown>;
  const oneTime = record.api_key ?? record.one_time_key;
  return {
    key: (record.key || {}) as UserAPIKey,
    oneTimeKey: typeof oneTime === "string" ? oneTime : "",
  };
}

export async function revokeUserAPIKey(id: string): Promise<void> {
  await fetchJSON(`/user/api-keys/${encodeURIComponent(id)}`, { method: "DELETE" });
}

export async function readUserUsage(windowDays: number, server?: string): Promise<UsageResponse> {
  const path = withQuery("/user/analytics/usage", {
    window_days: String(windowDays),
    server,
    limit: "25",
  });
  const body = (await fetchJSON(path)) as Partial<UsageResponse> | null;
  return {
    totals: body?.totals ?? {
      events: 0,
      allowed: 0,
      denied: 0,
      unique_servers: 0,
      unique_humans: 0,
      unique_agents: 0,
      unique_sessions: 0,
    },
    servers: Array.isArray(body?.servers) ? body.servers : [],
    tools: Array.isArray(body?.tools) ? body.tools : [],
    series: Array.isArray(body?.series) ? body.series : [],
    recent: Array.isArray(body?.recent) ? body.recent : [],
    filters: body?.filters || {},
    window_days: body?.window_days ?? windowDays,
  };
}

export async function readAgentUsage(agentID: string, hours: number): Promise<UsageResponse> {
  const body = await fetchJSON(withQuery("/user/analytics/usage", {
    agent_id: agentID, rpc_method: "tools/call", window_hours: String(hours), limit: "50",
  })) as UsageResponse;
  // An older service can ignore unknown filters. Never present its platform-
  // wide totals as if they belong to this agent.
  if (body?.filters?.agent_id !== agentID || !body.totals) {
    throw new Error("Agent activity is unavailable: the analytics service did not confirm the agent filter.");
  }
  return body;
}

export async function listTeams(): Promise<TeamMembership[]> {
  return asArray<TeamMembership>(await fetchJSON("/runtime/teams"), "teams");
}
