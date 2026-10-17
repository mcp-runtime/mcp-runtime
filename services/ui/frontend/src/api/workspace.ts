import { fetchJSON } from "./client";
import type { GrantSummary, SessionSummary, TeamRecord } from "./types";

export type WorkspaceServer = { name: string; namespace: string; status: string; can_manage: boolean };
export type WorkspaceServers = { servers: WorkspaceServer[]; deployment_attribution: "unavailable" };
export type WorkspaceAccess = { grants: GrantSummary[]; sessions: SessionSummary[] };
export type OverviewDirectory = { teams: number; users: number; agents: number; active_sessions: number };
export type OverviewServers = { servers: number; failures: WorkspaceServer[] };

export async function readWorkspaceTeams(): Promise<TeamRecord[]> {
  return ((await fetchJSON("/runtime/workspace/teams")) as { teams: TeamRecord[] }).teams;
}
export async function readWorkspaceServers(): Promise<WorkspaceServers> {
  return await fetchJSON("/runtime/workspace/servers") as WorkspaceServers;
}
export async function readWorkspaceAccess(): Promise<WorkspaceAccess> {
  return await fetchJSON("/runtime/workspace/access") as WorkspaceAccess;
}
export async function readOverviewDirectory(): Promise<OverviewDirectory> {
  return await fetchJSON("/admin/overview/directory") as OverviewDirectory;
}
export async function readOverviewServers(): Promise<OverviewServers> {
  return await fetchJSON("/admin/overview/servers") as OverviewServers;
}
