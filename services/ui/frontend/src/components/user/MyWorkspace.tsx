import { useQuery } from "@tanstack/react-query";
import { listTeamAgents } from "../../api/admin";
import { readWorkspaceAccess, readWorkspaceServers, readWorkspaceTeams } from "../../api/workspace";
import { isAdmin, type AuthStatus, type TeamRecord } from "../../api/types";
import { readUserUsage } from "../../api/userWorkflows";
import { AsyncSection } from "../admin/AsyncSection";
import { TeamsPanel } from "../admin/TeamsPanel";
import { PageHeader } from "../../ui/PageHeader";
import { EmptyState } from "../../ui/States";
import { RecentActivityTable } from "../usage/UsageTables";

type Props = { auth: AuthStatus; onSignIn: () => void; section?: string; team?: string };

function TeamAgents({ team, admin, onSignIn }: { team: TeamRecord; admin: boolean; onSignIn: () => void }) {
  const query = useQuery({ queryKey: ["workspace", "agents", team.slug], queryFn: () => listTeamAgents(team.slug, { limit: "5" }) });
  return <section className="overview-card" aria-label={`Agents in ${team.name}`}>
    <h3>{team.name}</h3><p>{admin || team.role === "owner" ? "You can manage these team-owned agents." : "You can view permitted agents. Team owners manage them."}</p>
    <AsyncSection query={query} loadingLabel="Loading agents…" errorTitle="Agents are unavailable." onRetry={() => void query.refetch()} onSignIn={onSignIn} testId={`workspace-agents-${team.slug}`}>
      {query.data?.agents.length ? <ul>{query.data.agents.map(agent => <li key={agent.id}><a href={`#/agents?team=${encodeURIComponent(team.slug)}&agent=${encodeURIComponent(agent.id)}`}>{agent.name}</a> · {agent.status}</li>)}</ul> : <p>No permitted agents in this team.</p>}
      {query.data?.next_cursor ? <p>More permitted agents may be available in the directory.</p> : null}
    </AsyncSection>
    <a href={`#/agents?team=${encodeURIComponent(team.slug)}`}>View {team.name} agents</a>
  </section>;
}

export function MyWorkspace({ auth, onSignIn, section, team }: Props) {
  const enabled = auth.authenticated;
  const admin = isAdmin(auth);
  const teams = useQuery({ queryKey: ["workspace", "teams"], queryFn: readWorkspaceTeams, enabled });
  const servers = useQuery({ queryKey: ["workspace", "servers"], queryFn: readWorkspaceServers, enabled: enabled && section !== "team" });
  const access = useQuery({ queryKey: ["workspace", "access"], queryFn: readWorkspaceAccess, enabled: enabled && section !== "team" });
  const activity = useQuery({ queryKey: ["workspace", "activity"], queryFn: () => readUserUsage(7), enabled: enabled && !admin && section !== "team" });
  if (!enabled) return <EmptyState title="Sign in to see My workspace." />;

  if (section === "team") {
    const manageable = (teams.data ?? []).filter(item => admin || item.role === "owner");
    return <><PageHeader title="Team management" description="Membership management for teams you administer." />
      <a href="#/workspace">Back to My workspace</a>
      <AsyncSection query={teams} loadingLabel="Checking team management access…" errorTitle="Teams are unavailable." onRetry={() => void teams.refetch()} onSignIn={onSignIn} testId="workspace-management">
        {manageable.some(item => item.slug === team) ? <TeamsPanel onSignIn={onSignIn} managedTeams={manageable} initialTeam={team} /> : <EmptyState title="You cannot manage this team." detail="Team ownership or platform administrator authority is required." />}
      </AsyncSection></>;
  }
  const rows = servers.data?.servers ?? [];
  return <>
    <PageHeader title="My workspace" description="Your memberships, permitted resources, and management responsibilities." />
    {admin ? <p className="notice">Administrator authority is platform-wide; it does not make every team your membership or every deployment yours. <a href="#/admin/overview">Platform overview</a></p> : null}
    <section className="section" aria-labelledby="workspace-teams-title"><h2 id="workspace-teams-title">My teams</h2>
      <AsyncSection query={teams} loadingLabel="Loading your memberships…" errorTitle="Memberships are unavailable." onRetry={() => void teams.refetch()} onSignIn={onSignIn} testId="workspace-teams">
        {teams.data?.length ? <ul className="overview-grid">{teams.data.map(item => <li className="overview-card" key={item.id}><h3>{item.name}</h3><p>Your role: {item.role || "unknown"}</p><p>{item.namespace}</p>
          <a href={`#/agents?team=${encodeURIComponent(item.slug)}`}>View agents</a>{admin || item.role === "owner" ? <> · <a href={`#/workspace/team?team=${encodeURIComponent(item.slug)}`}>Manage members</a></> : null}
        </li>)}</ul> : <p>You are not a member of any team.</p>}
      </AsyncSection>
      {admin ? <a href="#/admin/teams">Manage platform teams</a> : null}
    </section>
    <section className="section" aria-labelledby="workspace-agents-title"><h2 id="workspace-agents-title">Agents in my teams</h2><p>These identities belong to teams, rather than individual members.</p>
      <AsyncSection query={teams} loadingLabel="Loading agent teams…" errorTitle="Agent teams are unavailable." onRetry={() => void teams.refetch()} onSignIn={onSignIn} testId="workspace-agent-teams">
        <div className="overview-grid">{teams.data?.map(item => <TeamAgents key={item.id} team={item} admin={admin} onSignIn={onSignIn} />)}</div>
        {!teams.data?.length ? <p>No team-owned agents to summarize.</p> : null}
      </AsyncSection>
    </section>
    <section className="section" aria-labelledby="workspace-servers-title"><h2 id="workspace-servers-title">Servers</h2>
      <AsyncSection query={servers} loadingLabel="Loading permitted servers…" errorTitle="Servers are unavailable." onRetry={() => void servers.refetch()} onSignIn={onSignIn} testId="workspace-servers">
        <p><strong>Accessible to me: {rows.length}</strong>{admin ? " · Platform administrator visibility" : " · Visible catalog, subject to tool grants and session policy"}</p>
        <p><strong>Manageable by me: {rows.filter(item => item.can_manage).length}</strong> · Server management authority is checked by the API.</p>
        {rows.length ? <ul className="overview-resource-list">{rows.slice(0, 8).map(item => <li key={`${item.namespace}/${item.name}`}><a href={`#/servers?namespace=${encodeURIComponent(item.namespace)}&server=${encodeURIComponent(`${item.namespace}/${item.name}`)}`}>{item.namespace}/{item.name}</a> · {item.status || "Unknown"} · {item.can_manage ? "Can manage" : "Catalog access"}</li>)}</ul> : <p>No accessible servers.</p>}
        {rows.length > 8 ? <p>Showing eight of {rows.length} accessible servers.</p> : null}
      </AsyncSection>
      <p>Deployed by me: unavailable. Deployment actors are not recorded in the server API; ownership and visibility do not establish historical attribution.</p>
      <a href="#/servers">Open server catalog</a>
    </section>
    <section className="section" aria-labelledby="workspace-access-title"><h2 id="workspace-access-title">My access</h2><p>Grants for your human identity or actual team memberships, plus sessions for your human identity. Agent-specific access is shown in agent details.</p>
      <AsyncSection query={access} loadingLabel="Loading your access…" errorTitle="Access records are unavailable." onRetry={() => void access.refetch()} onSignIn={onSignIn} testId="workspace-access">
        {!access.data?.grants.length && !access.data?.sessions.length ? <p>No personal grants or sessions.</p> : <ul className="overview-resource-list">
          {access.data?.grants.map(item => <li key={`grant:${item.namespace}/${item.name}`}>Grant {item.namespace}/{item.name} · {item.disabled ? "Disabled" : "Enabled"} · Expires {item.expiresAt || "never"}</li>)}
          {access.data?.sessions.map(item => <li key={`session:${item.namespace}/${item.name}`}>Session {item.namespace}/{item.name} · {item.revoked ? "Revoked" : "Issued"} · Expires {item.expiresAt || "never"}</li>)}
        </ul>}
      </AsyncSection><a href="#/access">Open Access control</a>
    </section>
    <section className="section" aria-labelledby="workspace-activity-title"><h2 id="workspace-activity-title">Recent activity</h2>
      {admin ? <p>Platform-wide activity belongs in <a href="#/admin/analytics">Usage analytics</a>. It is not attributed to your own actions.</p> : <><p>Gateway activity across your user and team namespaces in the last seven days. These are not exclusively your actions.</p>
        <AsyncSection query={activity} loadingLabel="Loading namespace activity…" errorTitle="Namespace activity is unavailable." onRetry={() => void activity.refetch()} onSignIn={onSignIn} testId="workspace-activity"><RecentActivityTable rows={activity.data?.recent ?? []} /></AsyncSection>
        <a href="#/activity">View namespace activity</a></>}
    </section>
  </>;
}
