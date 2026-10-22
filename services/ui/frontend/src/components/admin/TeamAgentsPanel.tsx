import { useInfiniteQuery } from "@tanstack/react-query";

import { listTeamAgents } from "../../api/admin";
import type { AgentRecord, TeamMembership, TeamRecord } from "../../api/types";
import { ADMIN_QUERY_KEY } from "../../hooks/useAdminData";
import { StatusBadge } from "../../ui/Badge";
import { Button } from "../../ui/Button";
import { CopyButton } from "../../ui/CopyButton";
import { DataTable, buildColumns } from "../../ui/DataTable";
import { MetricGrid } from "../../ui/MetricCard";
import { ErrorState, LoadingState } from "../../ui/States";
import { formatTimestamp } from "../../lib/format";

type Props = { team: TeamRecord; members?: TeamMembership[] };

export function TeamAgentsPanel({ team, members }: Props) {
  const query = useInfiniteQuery({
    queryKey: [ADMIN_QUERY_KEY, "team-overview-agents", team.id, team.slug],
    initialPageParam: "",
    queryFn: async ({ pageParam }) => {
      const page = await listTeamAgents(team.slug, { cursor: pageParam || undefined, limit: "100" });
      if (page.agents.some(agent => agent.team_id !== team.id || agent.team_slug !== team.slug)) {
        throw new Error("The agent directory returned a different team's records. Refresh to try again.");
      }
      return page;
    },
    getNextPageParam: (lastPage, _pages, _lastParam, params) => {
      const cursor = lastPage.next_cursor;
      // A repeated cursor indicates a service error, not another usable page.
      return cursor && !params.includes(cursor) ? cursor : undefined;
    },
  });
  const agents = [...new Map((query.data?.pages.flatMap(page => page.agents) ?? []).map(agent => [agent.id, agent])).values()];
  const columns = buildColumns<AgentRecord>([
    { id: "name", header: "Agent", rowHeader: true, sortValue: agent => agent.name, cell: agent => <a className="link-button" href={`#/agents?team=${encodeURIComponent(team.slug)}&agent=${encodeURIComponent(agent.id)}`}>{agent.name}</a> },
    { id: "status", header: "Status", cell: agent => <StatusBadge tone={agent.status === "active" ? "ready" : "attention"}>{agent.status}</StatusBadge> },
    { id: "id", header: "Stable ID", cell: agent => <span className="copy-row"><span className="copy-value" title={agent.id}>{agent.id}</span><CopyButton value={agent.id} label={`Copy agent ID ${agent.id}`} /></span> },
    { id: "creator", header: "Created by", cell: agent => {
      const member = members?.find(row => row.user_id === agent.created_by);
      return <>{member?.email || agent.created_by || "Not recorded"}{member?.email ? <span className="cell-detail copy-value">{agent.created_by}</span> : null}</>;
    } },
    { id: "created", header: "Created", cell: agent => agent.created_at ? formatTimestamp(agent.created_at) : "Not recorded" },
  ]);
  const lastCursor = query.data?.pages.at(-1)?.next_cursor;
  const repeatedCursor = Boolean(lastCursor && !query.hasNextPage);
  return <section className="section" aria-labelledby="team-agents-title">
    <div className="section-head">
      <h2 className="section-title" id="team-agents-title">Agents of {team.name || team.slug}</h2>
      <a className="link-button" href={`#/agents?team=${encodeURIComponent(team.slug)}`}>Manage team agents</a>
    </div>
    <p className="section-note">Includes active and inactive agents owned by this team. Open an agent for its grants, sessions, recorded certificates and tool activity. The creator identifies who registered the agent; it does not identify every human who uses it.</p>
    {query.isPending ? <LoadingState label={`Loading agents of ${team.name || team.slug}…`} testId="team-agents-loading" /> : null}
    {query.error ? <ErrorState title="Team agents could not be loaded." detail={query.error instanceof Error ? query.error.message : ""} onRetry={() => void (query.isFetchNextPageError ? query.fetchNextPage() : query.refetch())} testId="team-agents-error" /> : null}
    {query.data ? <>
      <MetricGrid label="Loaded team agents" metrics={[
        { label: "Agents loaded", value: agents.length },
        { label: "Active agents loaded", value: agents.filter(agent => agent.status === "active").length },
        { label: "Inactive agents loaded", value: agents.filter(agent => agent.status === "inactive").length },
      ]} />
      <p className="section-note" role="status" aria-label="Agent directory completeness">{query.hasNextPage || query.error || repeatedCursor ? `${agents.length} agents loaded. The directory is incomplete.` : `All ${agents.length} team agents loaded.`}</p>
      <DataTable columns={columns} rows={agents} rowKey={agent => agent.id} caption="Agents registered to the selected team, with lifecycle and creator attribution." regionLabel="Team agents" testId="team-agents-table" emptyMessage="No agents registered to this team." />
      {repeatedCursor ? <ErrorState title="The agent directory returned a repeated cursor." onRetry={() => void query.refetch()} testId="team-agents-cursor-error" /> : null}
      {query.hasNextPage ? <Button variant="secondary" busy={query.isFetchingNextPage} disabled={query.isFetching} onClick={() => void query.fetchNextPage()} data-testid="team-agents-more">{query.isFetchingNextPage ? "Loading agents…" : "Load more agents"}</Button> : null}
    </> : null}
  </section>;
}
