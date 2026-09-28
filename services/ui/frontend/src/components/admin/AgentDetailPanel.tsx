import { useState } from "react";
import { useQuery } from "@tanstack/react-query";

import { createGrant, getAgent, revokeGrantSessions, setGrantDisabled, setSessionRevoked } from "../../api/admin";
import { ForbiddenError, UnauthorizedError } from "../../api/client";
import type { AgentRecord, GrantSummary, SessionSummary } from "../../api/types";
import { useAdminReload, useTeams, ADMIN_QUERY_KEY } from "../../hooks/useAdminData";
import { useCatalog } from "../../hooks/useCatalog";
import { formatTimestamp } from "../../lib/format";
import { StatusBadge } from "../../ui/Badge";
import { Button } from "../../ui/Button";
import { ConfirmDialog, type ConfirmRequest } from "../../ui/ConfirmDialog";
import { CopyButton } from "../../ui/CopyButton";
import { DataTable, buildColumns } from "../../ui/DataTable";
import { PageHeader } from "../../ui/PageHeader";
import { ErrorState, LoadingState } from "../../ui/States";
import { GrantForm, type GrantDraft } from "./accessForms";
import { sessionState } from "./AccessControlPanel";

type Props = { id: string; platformAdmin: boolean; onBack: () => void };

function initialGrant(agent: AgentRecord, namespace: string): GrantDraft {
  return {
    name: "", namespace, server: "", humanID: "", agentID: agent.id,
    teamID: agent.team_id, maxTrust: "low", allowedSideEffects: ["read"], expiresAt: "",
  };
}

export function AgentDetailPanel({ id, platformAdmin, onBack }: Props) {
  const reload = useAdminReload();
  const query = useQuery({ queryKey: [ADMIN_QUERY_KEY, "agent", id], queryFn: () => getAgent(id) });
  const teamsQuery = useTeams(true);
  const teamNamespace = teamsQuery.data?.find((team) => team.slug === query.data?.agent.team_slug)?.namespace || "";
  const catalog = useCatalog(Boolean(query.data?.can_manage) && Boolean(teamNamespace), platformAdmin ? "" : teamNamespace);
  const [draft, setDraft] = useState<GrantDraft | null>(null);
  const [confirm, setConfirm] = useState<ConfirmRequest | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  async function act(message: string, run: () => Promise<unknown>) {
    setBusy(true); setError(""); setNotice("");
    try { await run(); setNotice(message); setDraft(null); reload(); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "The action failed."); }
    finally { setBusy(false); setConfirm(null); }
  }

  if (query.isPending) return <LoadingState label="Loading agent details…" testId="agent-detail-loading" />;
  if (query.error) return <ErrorState title={query.error instanceof ForbiddenError ? "You cannot view this agent." : query.error instanceof UnauthorizedError ? "Sign in to view this agent." : "Agent details could not be loaded."} detail={query.error instanceof Error ? query.error.message : ""} onRetry={() => void query.refetch()} testId="agent-detail-error" />;
  const { agent, grants, sessions, can_manage: canManage } = query.data;
  const canManageResource = (namespace: string) => canManage && (platformAdmin || namespace === teamNamespace);
  const grantColumns = buildColumns<GrantSummary>([
    { id: "name", header: "Grant", rowHeader: true, cell: (grant) => <>{grant.name}<span className="cell-detail">{grant.namespace}</span></> },
    { id: "server", header: "Server", cell: (grant) => grant.serverRef?.name || "—" },
    { id: "subject", header: "Human", cell: (grant) => grant.subject?.humanID || "Any team member" },
    { id: "trust", header: "Trust", cell: (grant) => grant.maxTrust || "—" },
    { id: "effects", header: "Side effects", cell: (grant) => grant.allowedSideEffects?.join(", ") || "—" },
    { id: "expires", header: "Expires", cell: (grant) => grant.expiresAt ? formatTimestamp(grant.expiresAt) : "No expiry" },
    { id: "status", header: "Status", cell: (grant) => <StatusBadge tone={grant.disabled ? "attention" : "ready"}>{grant.disabled ? "Disabled" : "Active"}</StatusBadge> },
    { id: "actions", header: "Actions", cell: (grant) => canManageResource(grant.namespace) ? <div className="cell-actions">
      <Button variant="ghost" size="sm" disabled={busy} onClick={() => setConfirm({ title: `${grant.disabled ? "Enable" : "Disable"} grant “${grant.name}”?`, body: "This changes whether new sessions can use the grant.", confirmLabel: grant.disabled ? "Enable grant" : "Disable grant", destructive: !grant.disabled, onConfirm: () => act(`Grant “${grant.name}” ${grant.disabled ? "enabled" : "disabled"}.`, () => setGrantDisabled(grant.namespace, grant.name, !grant.disabled)) })}>{grant.disabled ? "Enable" : "Disable"}</Button>
      <Button variant="ghost" size="sm" disabled={busy} onClick={() => setConfirm({ title: `Revoke sessions for grant “${grant.name}”?`, body: "Matching active sessions will be revoked.", confirmLabel: "Revoke sessions", destructive: true, onConfirm: () => act(`Sessions for grant “${grant.name}” revoked.`, () => revokeGrantSessions(grant.namespace, grant.name)) })}>Revoke sessions</Button>
    </div> : null },
  ]);
  const sessionColumns = buildColumns<SessionSummary>([
    { id: "name", header: "Session", rowHeader: true, cell: (session) => <>{session.name}<span className="cell-detail">{session.namespace}</span></> },
    { id: "server", header: "Server", cell: (session) => session.serverRef?.name || "—" },
    { id: "human", header: "Human", cell: (session) => session.subject?.humanID || "—" },
    { id: "expires", header: "Expires", cell: (session) => session.expiresAt ? formatTimestamp(session.expiresAt) : "No expiry" },
    { id: "status", header: "Status", cell: (session) => { const state = sessionState(session); return <StatusBadge tone={state.tone}>{state.label}</StatusBadge>; } },
    { id: "actions", header: "Actions", cell: (session) => canManageResource(session.namespace) && !session.revoked ? <Button variant="ghost" size="sm" disabled={busy} onClick={() => setConfirm({ title: `Revoke session “${session.name}”?`, body: "Future tool calls made with this session will be denied.", confirmLabel: "Revoke session", destructive: true, onConfirm: () => act(`Session “${session.name}” revoked.`, () => setSessionRevoked(session.namespace, session.name, true)) })}>Revoke</Button> : null },
  ]);

  return <div data-testid="agent-detail">
    <PageHeader title={agent.name} breadcrumb={[{ label: "Agents", onClick: onBack }, { label: agent.name }]} actions={<Button variant="secondary" icon="chevronLeft" onClick={onBack}>Back to agents</Button>} />
    <dl className="detail-grid">
      <div><dt>Stable ID</dt><dd className="copy-row"><span className="copy-value">{agent.id}</span><CopyButton value={agent.id} label="Copy agent ID" /></dd></div>
      <div><dt>Owning team</dt><dd>{agent.team_slug}</dd></div>
      <div><dt>Status</dt><dd><StatusBadge tone={agent.status === "active" ? "ready" : "attention"}>{agent.status}</StatusBadge></dd></div>
    </dl>
    {notice ? <p className="notice notice-success" role="status">{notice}</p> : null}
    {error ? <p className="notice notice-danger" role="alert">{error}</p> : null}
    {canManage && agent.status === "active" && teamsQuery.isPending ? <LoadingState label="Loading agent team…" testId="agent-team-loading" /> : null}
    {canManage && agent.status === "active" && teamsQuery.error ? <ErrorState title="Agent team could not be loaded." onRetry={() => void teamsQuery.refetch()} testId="agent-team-error" /> : null}
    {canManage && agent.status === "active" && teamNamespace ? <section className="section">
      <Button variant="primary" icon="plus" onClick={() => setDraft(initialGrant(agent, teamNamespace))} data-testid="agent-grant-access">Grant access</Button>
      {draft && catalog.status === "loading" ? <LoadingState label="Loading servers for grant…" testId="agent-grant-catalog-loading" /> : null}
      {draft && catalog.status === "error" ? <ErrorState title="Servers could not be loaded." detail={catalog.error} onRetry={catalog.reload} testId="agent-grant-catalog-error" /> : null}
      {draft && catalog.status === "unauthorized" ? <ErrorState title="Sign in to load servers." testId="agent-grant-catalog-unauthorized" /> : null}
      {draft && catalog.status === "ready" ? <GrantForm draft={draft} fixedAgent={{ id: agent.id, teamID: agent.team_id, teamSlug: agent.team_slug }} servers={catalog.servers} namespaces={catalog.namespaces.map((item) => item.namespace)} busy={busy} submitError={error} onChange={setDraft} onCancel={() => setDraft(null)} onSubmit={() => void act(`Grant “${draft.name}” created.`, () => createGrant({ name: draft.name.trim(), namespace: draft.namespace, serverRef: { name: draft.server.trim() }, subject: { agentID: agent.id, teamID: agent.team_id }, maxTrust: draft.maxTrust, allowedSideEffects: draft.allowedSideEffects, expiresAt: draft.expiresAt ? new Date(draft.expiresAt).toISOString() : undefined }))} /> : null}
    </section> : null}
    <section className="section"><h2 className="section-title">Applicable grants</h2><DataTable columns={grantColumns} rows={grants} rowKey={(grant) => `${grant.namespace}/${grant.name}`} caption="Grants applicable to this agent and visible to you." regionLabel="Agent grants" testId="agent-grants-table" emptyMessage="No applicable grants." /></section>
    <section className="section"><h2 className="section-title">Sessions</h2><DataTable columns={sessionColumns} rows={sessions} rowKey={(session) => `${session.namespace}/${session.name}`} caption="Agent sessions visible to you, including expired and revoked history." regionLabel="Agent sessions" testId="agent-sessions-table" emptyMessage="No sessions visible." /></section>
    <section className="section"><h2 className="section-title">Connect this agent</h2><ol><li>Have a team owner or platform admin create an enabled grant for this agent and the target server.</li><li>Sign in with <code>mcp-runtime auth login --api-url &lt;platform-url&gt;</code>.</li><li>Start <code>mcp-runtime adapter proxy --runtime-url &lt;https-mcp-url&gt; --server &lt;server&gt; --namespace &lt;namespace&gt; --agent {agent.id} --auto-refresh</code>. It creates an authorized session and enrolls a certificate in memory. For a saved certificate, use <code>mcp-runtime adapter enroll</code> with the same server, namespace, and agent.</li></ol><p className="section-note">The private key stays local. OAuth targets also require a bearer token from the MCP client. A grant alone does not establish a connection; direct clients follow the server’s gateway policy.</p></section>
    {confirm ? <ConfirmDialog {...confirm} busy={busy} onCancel={() => setConfirm(null)} /> : null}
  </div>;
}
