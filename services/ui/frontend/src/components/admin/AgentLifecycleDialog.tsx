import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { getAgent, setAgentActive } from "../../api/admin";
import type { AgentRecord, SessionSummary } from "../../api/types";
import { ADMIN_QUERY_KEY } from "../../hooks/useAdminData";
import { ConfirmDialog } from "../../ui/ConfirmDialog";
import { ErrorState, LoadingState } from "../../ui/States";
import { AccessLink, revocationLabel } from "../access/accessLinks";
import { sessionState } from "./AccessControlPanel";

export function AgentLifecycleDialog({ agent, onClose, onChanged }: { agent: AgentRecord; onClose: () => void; onChanged: () => void }) {
  const snapshot = useQuery({ queryKey: [ADMIN_QUERY_KEY, "agent-lifecycle", agent.id], queryFn: () => getAgent(agent.id), staleTime: 0, retry: false });
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<{ sessions: SessionSummary[]; before: SessionSummary[]; error: string; verified: boolean; status?: string } | null>(null);
  const [deactivating] = useState(agent.status === "active");
  const sessions = snapshot.data?.sessions ?? [];
  async function apply() {
    if (!snapshot.data?.can_manage || busy) return;
    const before = snapshot.data.sessions;
    setBusy(true);
    let error = "";
    try { await setAgentActive(agent.id, !deactivating); }
    catch (cause) { error = cause instanceof Error ? cause.message : "Agent update failed."; }
    // A failed response can follow a committed change: re-read final state.
    try {
      const after = await getAgent(agent.id);
      setResult({ sessions: after.sessions, before, error, verified: true, status: after.agent.status });
    } catch {
      setResult({ sessions: [], before, error: `${error ? `${error} ` : ""}Final agent and session states could not be verified. Refresh before retrying.`, verified: false });
    }
    onChanged();
    setBusy(false);
  }
  if (result) {
    const changed = result.sessions.filter((session) => session.revoked && session.revocationCause === "agent_deactivated" && result.before.some((prior) => prior.namespace === session.namespace && prior.name === session.name && !prior.revoked));
    return <ConfirmDialog title="Agent lifecycle result" body={<>
      {result.error ? <p role="alert">{result.error}</p> : null}
      {result.verified ? <><p>Final agent status: {result.status}. {deactivating ? `${changed.length} visible sessions newly recorded as revoked by agent deactivation.` : "Previously revoked sessions remain revoked."}</p>
        <p>Results cover sessions visible to you. Concurrent changes may also appear.</p>
        <ul>{result.sessions.map((session) => <li key={`${session.namespace}/${session.name}`}><AccessLink kind="session" namespace={session.namespace} name={session.name} agent={agent.id} />: {sessionState(session).label}{session.revoked ? ` — ${revocationLabel(session)}` : ""}</li>)}</ul>
      </> : null}
    </>} confirmLabel="Done" onConfirm={onClose} onCancel={onClose} testId="agent-lifecycle-result" />;
  }
  const active = sessions.filter((session) => sessionState(session).label === "Active");
  return <ConfirmDialog title={`${deactivating ? "Deactivate" : "Reactivate"} agent “${agent.name}”?`} body={<>
    {snapshot.isPending ? <LoadingState label="Loading visible session impact…" /> : snapshot.error ? <ErrorState title="Session impact could not be loaded." onRetry={() => void snapshot.refetch()} /> : <>
      <p>{deactivating ? `${active.length} visible active sessions will lose access. Deactivation revokes all sessions for this agent, including sessions outside this view.` : "The existing ID can be used for new sessions. Previously revoked sessions stay revoked."}</p>
      {deactivating ? <ul>{sessions.filter((session) => !session.revoked).map((session) => <li key={`${session.namespace}/${session.name}`}>{session.namespace}/{session.name} — {sessionState(session).label}</li>)}</ul> : null}
      <p>The immutable ID and session history remain.</p>
    </>}
  </>} confirmLabel={deactivating ? "Deactivate agent" : "Reactivate agent"} destructive={deactivating} busy={busy} confirmDisabled={!snapshot.data?.can_manage || snapshot.isFetching || Boolean(snapshot.error)} onConfirm={apply} onCancel={onClose} testId="agent-confirm" confirmTestId="agent-confirm-yes" cancelTestId="agent-confirm-cancel" />;
}
