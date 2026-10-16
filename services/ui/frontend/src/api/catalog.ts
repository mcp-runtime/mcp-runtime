import { fetchJSON, fetchPublicJSON, withQuery } from "./client";
import type { GatewayEvent, NamespaceEntry, PublishPolicy, ServerLogEntry, ServerObservabilityMetrics, ServerSummary, ToolRow } from "./types";

function asArray<T>(value: unknown, key: string): T[] {
  if (!value || typeof value !== "object") {
    return [];
  }
  const items = (value as Record<string, unknown>)[key];
  return Array.isArray(items) ? (items as T[]) : [];
}

export async function listNamespaces(): Promise<NamespaceEntry[]> {
  return asArray<NamespaceEntry>(await fetchJSON("/runtime/namespaces"), "namespaces");
}

export type ServerList = {
  servers: ServerSummary[];
  publishPolicy: PublishPolicy | null;
};

export async function listServers(namespace?: string): Promise<ServerList> {
  const path = withQuery("/runtime/servers", { namespace });
  const data = await fetchJSON(path);
  const record = data && typeof data === "object" ? (data as Record<string, unknown>) : {};
  return {
    servers: asArray<ServerSummary>(data, "servers"),
    publishPolicy: (record.publish_policy as PublishPolicy | undefined) ?? null,
  };
}

export async function listTools(namespace?: string): Promise<ToolRow[]> {
  const path = withQuery("/runtime/tools", { namespace });
  return asArray<ToolRow>(await fetchJSON(path), "tools");
}

// Anonymous public-mode catalog reads, for a signed-out visitor to a
// PLATFORM_MODE=public deployment. No namespace is passed - the backend
// (PublicCatalogFallback) defaults it to the deployment's public namespace,
// the same scope the removed legacy dashboard's synthesized "public preview"
// namespace pointed at.
export async function listPublicServers(): Promise<ServerSummary[]> {
  return asArray<ServerSummary>(await fetchPublicJSON("/servers"), "servers");
}

export async function listPublicTools(): Promise<ToolRow[]> {
  return asArray<ToolRow>(await fetchPublicJSON("/tools"), "tools");
}

// Available to any authenticated principal who owns or can publish to the
// server's namespace, not only admin (services/runtime-api's
// handleRuntimeServerDelete checks principalCanPublishNamespace /
// serverWritableByPrincipal, not role === admin).
export async function retireServer(namespace: string, name: string): Promise<void> {
  await fetchJSON(`/runtime/servers/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`, {
    method: "DELETE",
  });
}

export async function listServerEvents(namespace: string, server: string): Promise<GatewayEvent[]> {
  const data = await fetchJSON(withQuery("/runtime/server-events", { namespace, server, limit: "20" }));
  return asArray<GatewayEvent>(data, "events");
}

export async function listServerLogs(namespace: string, server: string): Promise<ServerLogEntry[]> {
  const data = await fetchJSON(withQuery("/runtime/observability/logs/query", { namespace, server }));
  return asArray<ServerLogEntry>(data, "entries");
}

export async function getServerObservabilityMetrics(namespace: string, server: string): Promise<ServerObservabilityMetrics> {
  return (await fetchJSON(withQuery("/runtime/observability/metrics", { namespace, server }))) as ServerObservabilityMetrics;
}
