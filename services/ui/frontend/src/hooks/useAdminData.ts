import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback } from "react";

import {
  listComponents,
  listTeamAgents,
  listEvents,
  listGrants,
  listSessions,
  listTeamMembers,
  listTeams,
  listUsage,
  readOperations,
} from "../api/admin";
import { UnauthorizedError } from "../api/client";

export const ADMIN_QUERY_KEY = "admin";

export type AdminStatus = "loading" | "ready" | "error" | "unauthorized";

export function adminStatusOf(query: {
  isPending: boolean;
  error: unknown;
}): AdminStatus {
  if (query.error instanceof UnauthorizedError) {
    return "unauthorized";
  }
  if (query.error) {
    return "error";
  }
  return query.isPending ? "loading" : "ready";
}

export function adminErrorMessage(error: unknown): string {
  if (error instanceof Error && error.message.trim()) {
    return error.message.trim();
  }
  return "";
}

// `enabled` is driven by the server-returned principal. A non-admin session
// never issues these requests at all, so the guard fails closed in the network
// layer as well as in the rendered tree.
export function useGrants(enabled: boolean, namespace: string) {
  return useQuery({
    queryKey: [ADMIN_QUERY_KEY, "grants", namespace],
    queryFn: () => listGrants(namespace),
    enabled,
  });
}

export function useSessions(enabled: boolean, namespace: string) {
  return useQuery({
    queryKey: [ADMIN_QUERY_KEY, "sessions", namespace],
    queryFn: () => listSessions(namespace),
    enabled,
  });
}

export function useTeams(enabled: boolean) {
  return useQuery({
    queryKey: [ADMIN_QUERY_KEY, "teams"],
    queryFn: listTeams,
    enabled,
  });
}

// Keyed by slug, so switching teams never shows the previous team's members:
// the new key has no data yet and the panel renders its own loading state.
export function useTeamMembers(enabled: boolean, slug: string) {
  return useQuery({
    queryKey: [ADMIN_QUERY_KEY, "team-members", slug],
    queryFn: () => listTeamMembers(slug),
    enabled: enabled && Boolean(slug),
  });
}

export function useTeamAgents(
  enabled: boolean,
  slug: string,
  filters: { status?: string; q?: string; cursor?: string } = {}
) {
  return useQuery({
    queryKey: [ADMIN_QUERY_KEY, "team-agents", slug, filters.status ?? "all", filters.q ?? "", filters.cursor ?? ""],
    queryFn: () => listTeamAgents(slug, { ...filters, limit: "100" }),
    enabled: enabled && Boolean(slug),
  });
}

export function useComponents(enabled: boolean) {
  return useQuery({
    queryKey: [ADMIN_QUERY_KEY, "components"],
    queryFn: listComponents,
    enabled,
  });
}

export const OPERATIONS_LIMIT = "100";

export type OperationsFilters = { user: string; since: string; until: string };

// The API returns at most OPERATIONS_LIMIT rows per collection, so anything the
// panel paginates is a loaded window, not the complete history.
export function useOperations(enabled: boolean, filters: OperationsFilters) {
  return useQuery({
    queryKey: [ADMIN_QUERY_KEY, "operations", filters.user, filters.since, filters.until],
    queryFn: () =>
      readOperations({
        user: filters.user,
        since: filters.since,
        until: filters.until,
        limit: OPERATIONS_LIMIT,
      }),
    enabled,
  });
}

export function useUsage(enabled: boolean, limit: string) {
  return useQuery({
    queryKey: [ADMIN_QUERY_KEY, "usage", limit],
    queryFn: () => listUsage(limit),
    enabled,
  });
}

export function useAccessActivity(
  enabled: boolean,
  kind: "grant" | "session",
  name: string
) {
  return useQuery({
    queryKey: [ADMIN_QUERY_KEY, "events", kind, name],
    queryFn: () =>
      listEvents(kind === "session" ? { limit: "1000", session_id: name } : { limit: "1000" }),
    enabled: enabled && Boolean(name),
  });
}

export function useAdminReload() {
  const queryClient = useQueryClient();
  return useCallback(() => {
    void queryClient.invalidateQueries({ queryKey: [ADMIN_QUERY_KEY] });
  }, [queryClient]);
}
