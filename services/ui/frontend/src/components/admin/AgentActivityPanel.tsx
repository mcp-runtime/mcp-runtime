import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { readAgentUsage } from "../../api/userWorkflows";
import type { RecentActivity, RequestUsage } from "../../api/types";
import { formatTimestamp } from "../../lib/format";
import { MetricGrid } from "../../ui/MetricCard";
import { DataTable, buildColumns } from "../../ui/DataTable";
import { Button } from "../../ui/Button";
import { SelectField } from "../../ui/Field";
import { ErrorState, LoadingState } from "../../ui/States";
import { ADMIN_QUERY_KEY } from "../../hooks/useAdminData";

export function AgentActivityPanel({ agentID, platformAdmin }: { agentID: string; platformAdmin: boolean }) {
  const [hours, setHours] = useState("168");
  const query = useQuery({ queryKey: [ADMIN_QUERY_KEY, "agent-mcp-activity", agentID, hours, platformAdmin], queryFn: () => readAgentUsage(agentID, Number(hours)) });
  const columns = buildColumns<RequestUsage>([
    { id: "tool", header: "Tool", rowHeader: true, cell: row => row.tool_name || "Unknown tool" },
    { id: "server", header: "Server", cell: row => `${row.namespace}/${row.server}` },
    { id: "decision", header: "Gateway decision", cell: row => row.decision || "Unreported" },
    { id: "calls", header: "Calls", cell: row => row.events },
    { id: "failures", header: "HTTP failures", cell: row => row.http_failures },
    { id: "last", header: "Last seen", cell: row => formatTimestamp(row.last_seen) },
    { id: "trace", header: "Latest trace ID", cell: row => <span className="copy-value">{row.trace_id || "Unreported"}</span> },
  ]);
  const recentColumns = buildColumns<RecentActivity>([
    { id: "when", header: "Time", rowHeader: true, cell: row => formatTimestamp(row.timestamp) },
    { id: "tool", header: "Tool", cell: row => row.tool_name || "Unknown tool" },
    { id: "server", header: "Server", cell: row => `${row.namespace || "?"}/${row.server || "?"}` },
    { id: "human", header: "Delegating human", cell: row => row.human_id || "Unreported" },
    { id: "session", header: "Session", cell: row => row.session_id || "Unreported" },
    { id: "decision", header: "Gateway decision", cell: row => row.decision || "Unreported" },
  ]);
  const recent = query.data?.recent ?? [];
  const lastSeen = recent.reduce((last, row) => row.timestamp > last ? row.timestamp : last, "");
  return <section className="section" aria-label="Agent MCP activity" data-testid="agent-mcp-activity">
    <h2 className="section-title">MCP tool activity</h2>
    <p className="section-note">{platformAdmin ? "Recorded platform activity for this exact agent." : "Activity for this exact agent within namespaces and resource teams visible to your account. Calls to other teams or shared catalog servers may be excluded."} This shows observed MCP calls, not the agent’s complete task or intent.</p>
    <div className="inline-actions">
      <SelectField label="Activity time range" value={hours} options={[{ value: "1", label: "Last hour" }, { value: "24", label: "Last 24 hours" }, { value: "168", label: "Last 7 days" }]} onChange={event => setHours(event.target.value)} />
      <Button variant="secondary" onClick={() => void query.refetch()}>Refresh activity</Button>
    </div>
    {query.isPending ? <LoadingState label="Loading agent activity…" /> : query.error ? <ErrorState title="Agent activity is unavailable." detail={query.error instanceof Error ? query.error.message : ""} onRetry={() => void query.refetch()} testId="agent-activity-error" /> : <>
      <MetricGrid label="Agent tool call totals" metrics={[
        { label: "Tool calls", value: query.data?.totals.events },
        { label: "Gateway allowed", value: query.data?.totals.allowed },
        { label: "Gateway denied", value: query.data?.totals.denied },
        { label: "HTTP failures", value: query.data?.totals.http_failures },
      ]} />
      <p>Last observed tool call: {lastSeen ? formatTimestamp(lastSeen) : "None recorded in this window"}.</p>
      <p className="section-note">Allowed is a gateway policy decision, not proof the tool succeeded. MCP errors can use HTTP 200. Totals cover the selected window; each table shows at most 50 rows.</p>
      <DataTable columns={columns} rows={query.data?.requests ?? []} rowKey={row => JSON.stringify([row.namespace, row.server, row.rpc_method, row.tool_name, row.decision, row.policy_version])} caption="Tool calls grouped by server, tool and gateway decision." regionLabel="Agent tool call groups" testId="agent-tool-calls" emptyMessage="No tool calls recorded in this window." />
      <DataTable columns={recentColumns} rows={recent} rowKey={row => JSON.stringify(row)} caption="Recent tool calls and their human and session attribution." regionLabel="Recent agent tool calls" testId="agent-recent-calls" emptyMessage="No recent tool calls recorded." />
    </>}
  </section>;
}
