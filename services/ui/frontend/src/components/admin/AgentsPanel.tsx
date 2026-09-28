import { useEffect, useMemo, useState, type FormEvent } from "react";
import { useQuery } from "@tanstack/react-query";

import { createTeamAgent, listTeamAgents, renameAgent, setAgentActive } from "../../api/admin";
import { isAdmin, type AgentRecord, type AuthStatus } from "../../api/types";
import { ADMIN_QUERY_KEY, useAdminReload, useTeamAgents, useTeams } from "../../hooks/useAdminData";
import { AgentDetailPanel } from "./AgentDetailPanel";
import { AsyncSection } from "./AsyncSection";
import { StatusBadge } from "../../ui/Badge";
import { Button } from "../../ui/Button";
import { ConfirmDialog, type ConfirmRequest } from "../../ui/ConfirmDialog";
import { CopyButton } from "../../ui/CopyButton";
import { DataTable, buildColumns } from "../../ui/DataTable";
import { SelectField, TextField } from "../../ui/Field";
import { PageHeader } from "../../ui/PageHeader";
import { EmptyState, ErrorState, LoadingState } from "../../ui/States";

type AgentsPanelProps = { onSignIn: () => void; auth: AuthStatus; initialTeam?: string; initialAgent?: string; onTeamChange?: (team: string) => void; onAgentChange?: (agent: string) => void };

export function AgentsPanel({ onSignIn, auth, initialTeam, initialAgent, onTeamChange, onAgentChange }: AgentsPanelProps) {
  const platformAdmin = isAdmin(auth);
  const reload = useAdminReload();
  const teamsQuery = useTeams(true);
  const teams = useMemo(() => teamsQuery.data ?? [], [teamsQuery.data]);
  const [teamSlug, setTeamSlug] = useState(initialTeam || (platformAdmin ? "*" : ""));
  const [selectedAgent, setSelectedAgent] = useState(initialAgent || "");
  const [status, setStatus] = useState("");
  const [search, setSearch] = useState("");
  const [cursors, setCursors] = useState<string[]>([]);
  const [allPage, setAllPage] = useState(0);
  const [createName, setCreateName] = useState("");
  const [editing, setEditing] = useState<{ id: string; name: string } | null>(null);
  const [confirm, setConfirm] = useState<ConfirmRequest | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  useEffect(() => {
    if (teamsQuery.isPending) return;
    if (platformAdmin && teamSlug === "*") return;
    if (teamSlug && teams.some((team) => team.slug === teamSlug)) return;
    setTeamSlug(platformAdmin ? "*" : teams[0]?.slug ?? "");
    setCursors([]);
  }, [teams, teamSlug, platformAdmin, teamsQuery.isPending]);

  useEffect(() => {
    setTeamSlug(initialTeam || (platformAdmin ? "*" : ""));
    setCursors([]);
    setAllPage(0);
  }, [initialTeam, platformAdmin]);

  useEffect(() => { setSelectedAgent(initialAgent || ""); }, [initialAgent]);

  const cursor = cursors[cursors.length - 1] ?? "";
  const agentsQuery = useTeamAgents(teamSlug !== "*", teamSlug, { status: status || undefined, q: search.trim() || undefined, cursor: cursor || undefined });
  const allAgentsQuery = useQuery({
    queryKey: [ADMIN_QUERY_KEY, "all-agents", teams.map((team) => team.slug).join(","), status, search.trim()],
    enabled: platformAdmin && teamSlug === "*" && teamsQuery.isSuccess,
    queryFn: async () => {
      const pages = await Promise.all(teams.map(async (team) => {
        const items: AgentRecord[] = [];
        let next = "";
        for (let i = 0; i < 100; i++) {
          const page = await listTeamAgents(team.slug, { status: status || undefined, q: search.trim() || undefined, cursor: next || undefined, limit: "200" });
          items.push(...page.agents);
          if (!page.next_cursor) return items;
          next = page.next_cursor;
        }
        throw new Error(`Agent directory for ${team.slug} exceeds the supported page window.`);
      }));
      return pages.flat().sort((a, b) => (b.created_at || "").localeCompare(a.created_at || "") || b.id.localeCompare(a.id));
    },
  });
  const allAgents = allAgentsQuery.data ?? [];
  const agents = teamSlug === "*" ? allAgents.slice(allPage * 100, (allPage + 1) * 100) : agentsQuery.data?.agents ?? [];
  const selectedTeam = teams.find((team) => team.slug === teamSlug);
  const canManageTeam = platformAdmin || selectedTeam?.role === "owner";

  async function runAction(message: string, action: () => Promise<unknown>) {
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await action();
      setNotice(message);
      setEditing(null);
      setCreateName("");
      await reload();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "The change could not be applied.");
    } finally {
      setBusy(false);
      setConfirm(null);
    }
  }

  function submitCreate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const name = createName.trim();
    if (!selectedTeam || !canManageTeam || !name || name.length > 64 || busy) {
      setError("Choose a team and enter an agent name from 1 to 64 characters.");
      return;
    }
    void runAction(`Agent “${name}” created.`, () => createTeamAgent(selectedTeam.slug, name));
  }

  function submitRename(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!editing || busy) return;
    const name = editing.name.trim();
    if (!name || name.length > 64) {
      setError("Agent names must contain 1 to 64 characters.");
      return;
    }
    void runAction(`Agent renamed to “${name}”.`, () => renameAgent(editing.id, name));
  }

  const columns = buildColumns<AgentRecord>([
    {
      id: "name",
      header: "Agent",
      rowHeader: true,
      sortValue: (agent) => agent.name.toLocaleLowerCase(),
      cell: (agent) => editing?.id === agent.id ? (
        <form className="inline-actions" onSubmit={submitRename}>
          <TextField label="New agent name" value={editing.name} data-testid="agent-rename-input" onChange={(event) => setEditing({ ...editing, name: event.target.value })} />
          <Button type="submit" size="sm" busy={busy} data-testid="agent-rename-save">Save</Button>
          <Button type="button" variant="ghost" size="sm" onClick={() => setEditing(null)}>Cancel</Button>
        </form>
      ) : <><button type="button" className="link-button" data-testid="agent-open" onClick={() => { setSelectedAgent(agent.id); onAgentChange?.(agent.id); }}>{agent.name}</button><span className="cell-detail">{agent.team_slug}</span></>,
    },
    {
      id: "id",
      header: "Immutable ID",
      cell: (agent) => <span className="copy-row"><span className="copy-value" title={agent.id}>{agent.id}</span><CopyButton value={agent.id} label={`Copy agent ID ${agent.id}`} /></span>,
    },
    {
      id: "status",
      header: "Status",
      cell: (agent) => <StatusBadge tone={agent.status === "active" ? "ready" : "attention"}>{agent.status}</StatusBadge>,
    },
    {
      id: "actions",
      header: "Actions",
      cell: (agent) => (platformAdmin || teams.find((team) => team.slug === agent.team_slug)?.role === "owner") ? (
        <div className="cell-actions">
          <Button variant="ghost" size="sm" disabled={busy} data-testid="agent-rename" onClick={() => setEditing({ id: agent.id, name: agent.name })}>Rename</Button>
          <Button
            variant="ghost"
            size="sm"
            disabled={busy}
            data-testid={agent.status === "active" ? "agent-deactivate" : "agent-reactivate"}
            onClick={() => {
              const active = agent.status === "active";
              setConfirm({
                title: `${active ? "Deactivate" : "Reactivate"} agent “${agent.name}”?`,
                body: active ? "The ID and history remain. All active sessions for this agent are revoked immediately." : "The existing ID can be selected for new grants and sessions again. Previously revoked sessions stay revoked.",
                confirmLabel: active ? "Deactivate agent" : "Reactivate agent",
                destructive: active,
                onConfirm: () => runAction(`Agent “${agent.name}” ${active ? "deactivated" : "reactivated"}.`, () => setAgentActive(agent.id, !active)),
              });
            }}
          >{agent.status === "active" ? "Deactivate" : "Reactivate"}</Button>
        </div>
      ) : null,
    },
  ]);

  const hasNext = teamSlug === "*" ? (allPage + 1) * 100 < allAgents.length : Boolean(agentsQuery.data?.next_cursor);

  if (selectedAgent) return <AgentDetailPanel id={selectedAgent} platformAdmin={platformAdmin} onBack={() => { setSelectedAgent(""); onAgentChange?.(""); }} />;

  return (
    <>
      <PageHeader title="Agent Management" description="Browse team-owned agent identities, access, sessions, and connection steps." />
      {notice ? <p className="notice notice-success" role="status" data-testid="agent-action-notice">{notice}</p> : null}
      {error ? <p className="notice notice-danger" role="alert" data-testid="agent-action-error">{error}</p> : null}

      <section className="section">
        <div className="form-grid">
          <SelectField
            label="Team"
            value={teamSlug}
            data-testid="agent-team-select"
            options={[...(platformAdmin ? [{ value: "*", label: "All teams" }] : []), ...teams.map((team) => ({ value: team.slug, label: `${team.name || team.slug} (${team.slug})` }))]}
            onChange={(event) => { setTeamSlug(event.target.value); onTeamChange?.(event.target.value); setCursors([]); setAllPage(0); setEditing(null); }}
          />
          <SelectField
            label="Status"
            value={status}
            data-testid="agent-status-filter"
            options={[{ value: "", label: "All agents" }, { value: "active", label: "Active" }, { value: "inactive", label: "Inactive" }]}
            onChange={(event) => { setStatus(event.target.value); setCursors([]); setAllPage(0); }}
          />
          <TextField label="Search agents" type="search" value={search} data-testid="agent-search" onChange={(event) => { setSearch(event.target.value); setCursors([]); setAllPage(0); }} />
        </div>
        {selectedTeam && canManageTeam ? <form className="inline-actions" onSubmit={submitCreate}>
          <TextField label="New agent name" value={createName} maxLength={64} disabled={!teamSlug || busy} data-testid="agent-create-name" hint={selectedTeam ? `New identity in ${selectedTeam.name || selectedTeam.slug}.` : "Select a team first."} onChange={(event) => setCreateName(event.target.value)} />
          <Button type="submit" disabled={!teamSlug} busy={busy} data-testid="agent-create-submit">Create agent</Button>
        </form> : null}
      </section>

      <section className="section" aria-label="Agents">
        {teamsQuery.isPending ? <LoadingState label="Loading teams…" testId="agents-teams-loading" /> : teamsQuery.error ? <ErrorState title="Teams could not be loaded." detail="Retry the request before viewing agents." onRetry={() => void teamsQuery.refetch()} testId="agents-teams-error" /> : !teamSlug ? <EmptyState title="No teams are available." testId="agents-no-team" /> : (
          <AsyncSection query={teamSlug === "*" ? allAgentsQuery : agentsQuery} loadingLabel="Loading agents…" errorTitle="Agents could not be loaded." onRetry={() => void (teamSlug === "*" ? allAgentsQuery.refetch() : agentsQuery.refetch())} onSignIn={onSignIn} testId="agents">
            <DataTable columns={columns} rows={agents} rowKey={(agent) => agent.id} caption="Team agent directory with immutable IDs, lifecycle status, and management actions." regionLabel="Team agents" testId="agents-table" emptyMessage="No agents match this team and filter." />
            <div className="inline-actions" aria-label="Agent directory pagination">
              <Button variant="secondary" disabled={teamSlug === "*" ? allPage === 0 : cursors.length === 0} data-testid="agents-previous" onClick={() => teamSlug === "*" ? setAllPage((current) => current - 1) : setCursors((current) => current.slice(0, -1))}>Previous</Button>
              <Button variant="secondary" disabled={!hasNext} data-testid="agents-next" onClick={() => teamSlug === "*" ? setAllPage((current) => current + 1) : setCursors((current) => [...current, agentsQuery.data?.next_cursor ?? ""])}>Next</Button>
            </div>
          </AsyncSection>
        )}
      </section>

      {confirm ? <ConfirmDialog {...confirm} busy={busy} onCancel={() => setConfirm(null)} testId="agent-confirm" confirmTestId="agent-confirm-yes" cancelTestId="agent-confirm-cancel" /> : null}
    </>
  );
}
