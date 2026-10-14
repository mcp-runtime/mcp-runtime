import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { delegationRecipients } from "../../api/admin";
import { ADMIN_QUERY_KEY } from "../../hooks/useAdminData";
import { Button } from "../../ui/Button";
import { SelectField, TextField } from "../../ui/Field";
import { ErrorState, LoadingState } from "../../ui/States";

type Subject = { teamID: string; humanID: string; agentID: string };
type Mode = "agent" | "human" | "human_agent" | "team";
const EMPTY: Subject = { teamID: "", humanID: "", agentID: "" };

// Recipient lookup is an explicit, owner-authorized exact-team request. It does
// not broaden the caller's ordinary team directory or accept free-text agents.
export function DelegationRecipientFields({ namespace, onChange }: { namespace: string; onChange: (subject: Subject) => void }) {
  const [team, setTeam] = useState("");
  const [lookup, setLookup] = useState("");
  const [agentCursors, setAgentCursors] = useState<string[]>([]);
  const [memberCursors, setMemberCursors] = useState<string[]>([]);
  const [mode, setMode] = useState<Mode>("agent");
  const [human, setHuman] = useState("");
  const [agent, setAgent] = useState("");
  const agentCursor = agentCursors.at(-1) || "", memberCursor = memberCursors.at(-1) || "";
  const query = useQuery({
    queryKey: [ADMIN_QUERY_KEY, "delegation-recipients", namespace, lookup, agentCursor, memberCursor],
    queryFn: () => delegationRecipients(namespace, lookup, agentCursor, memberCursor),
    enabled: Boolean(namespace && lookup), retry: false,
  });
  function clear() { setAgent(""); setHuman(""); onChange(EMPTY); }
  function publish(nextMode: Mode, nextHuman: string, nextAgent: string) {
    setMode(nextMode); setHuman(nextHuman); setAgent(nextAgent);
    const complete = (nextMode === "team" || nextMode === "agent" || Boolean(nextHuman)) && (nextMode === "team" || nextMode === "human" || Boolean(nextAgent));
    onChange(complete && query.data ? { teamID: query.data.team.id, humanID: nextHuman, agentID: nextAgent } : EMPTY);
  }
  const agents = query.data?.agents.filter((item) => item.status === "active") ?? [];
  const members = query.data?.members ?? [];
  return <div style={{ gridColumn: "1 / -1" }}>
    <div className="form-grid" style={{ alignItems: "end" }}>
    <TextField label="Recipient team slug" value={team} placeholder="Exact team slug, for example finance" onChange={(event) => { setTeam(event.target.value); setLookup(""); clear(); }} data-testid="delegation-team-slug" />
    <div><Button variant="secondary" disabled={!namespace || !team.trim()} onClick={() => { clear(); setAgentCursors([]); setMemberCursors([]); if (lookup === team.trim()) void query.refetch(); else setLookup(team.trim()); }} data-testid="delegation-lookup">Look up recipient team</Button></div>
    </div>
    <p className="field-hint">Only resource owners and administrators can look up a known recipient team. The lookup returns stable member IDs and active agents; it does not grant access.</p>
    {lookup && query.isPending ? <LoadingState label="Looking up recipient team…" /> : lookup && query.error ? <ErrorState title="Recipient lookup failed." detail={query.error instanceof Error ? query.error.message : "Retry the lookup."} onRetry={() => void query.refetch()} /> : lookup && query.data ? <>
      <p>Recipient: {query.data.team.name || query.data.team.slug} ({query.data.team.id})</p>
      <div className="form-grid">
      <SelectField label="Delegate to" value={mode} options={[{ value: "agent", label: "Active agent" }, { value: "human", label: "Human member" }, { value: "human_agent", label: "Human and agent" }, { value: "team", label: "Entire recipient team" }]} onChange={(event) => { setMode(event.target.value as Mode); clear(); }} data-testid="delegation-mode" />
      {mode === "team" ? <Button variant="secondary" onClick={() => publish(mode, "", "")}>Select entire recipient team</Button> : null}
      {mode === "human" || mode === "human_agent" ? <div>
        <SelectField label="Recipient member" value={human} options={[{ value: "", label: "Select a stable member ID" }, ...(human && !members.some((member) => member.user_id === human) ? [{ value: human, label: human }] : []), ...members.map((member) => ({ value: member.user_id, label: member.user_id }))]} onChange={(event) => publish(mode, event.target.value, agent)} data-testid="delegation-human" />
        <div className="inline-actions"><Button variant="ghost" disabled={!memberCursors.length || query.isFetching} onClick={() => setMemberCursors((cursors) => cursors.slice(0, -1))}>Previous members</Button><Button variant="ghost" disabled={!query.data.member_next_cursor || query.isFetching} onClick={() => setMemberCursors((cursors) => [...cursors, query.data!.member_next_cursor!])}>Next members</Button></div>
      </div> : null}
      {mode === "agent" || mode === "human_agent" ? <div>
        <SelectField label="Recipient agent" value={agent} options={[{ value: "", label: "Select an active agent" }, ...(agent && !agents.some((item) => item.id === agent) ? [{ value: agent, label: agent }] : []), ...agents.map((item) => ({ value: item.id, label: `${item.name} (${item.id})` }))]} onChange={(event) => publish(mode, human, event.target.value)} data-testid="delegation-agent" />
        <div className="inline-actions"><Button variant="ghost" disabled={!agentCursors.length || query.isFetching} onClick={() => setAgentCursors((cursors) => cursors.slice(0, -1))}>Previous agents</Button><Button variant="ghost" disabled={!query.data.agent_next_cursor || query.isFetching} onClick={() => setAgentCursors((cursors) => [...cursors, query.data!.agent_next_cursor!])}>Next agents</Button></div>
      </div> : null}
      </div>
    </> : null}
  </div>;
}
