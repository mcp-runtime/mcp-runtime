import { useMemo } from "react";

import { DataTable, buildColumns } from "../../ui/DataTable";
import { ProportionList } from "../../ui/ProportionBar";
import { formatAbsolute, formatTimestamp } from "../../lib/format";
import type { RecentActivity, ServerUsage, ToolUsage } from "../../api/types";

export function ServerUsageTable({ rows }: { rows: ServerUsage[] }) {
  const columns = useMemo(
    () =>
      buildColumns<ServerUsage>([
        {
          id: "server",
          header: "Server",
          rowHeader: true,
          sortValue: (row) => row.server,
          cell: (row) => (
            <>
              {row.server}
              <span className="cell-detail">{row.namespace}</span>
            </>
          ),
        },
        { id: "events", header: "Requests", numeric: true, sortValue: (row) => row.events, cell: (row) => row.events.toLocaleString() },
        { id: "allowed", header: "Allowed", numeric: true, sortValue: (row) => row.allowed, cell: (row) => row.allowed.toLocaleString() },
        { id: "denied", header: "Denied", numeric: true, sortValue: (row) => row.denied, cell: (row) => row.denied.toLocaleString() },
        {
          id: "last",
          header: "Last seen",
          sortValue: (row) => Date.parse(row.last_seen || "") || 0,
          cell: (row) => <span title={formatAbsolute(row.last_seen)}>{formatTimestamp(row.last_seen)}</span>,
        },
      ]),
    []
  );

  return (
    <DataTable
      columns={columns}
      rows={rows}
      rowKey={(row) => `${row.namespace}/${row.server}`}
      caption="MCP servers in scope, with request, allow, and deny counts."
      regionLabel="Server usage"
      testId="server-usage-table"
      rowTestId="server-usage-row"
      emptyTestId="server-usage-empty"
      emptyMessage="No server activity in this window."
    />
  );
}

export function ToolUsageTable({ rows }: { rows: ToolUsage[] }) {
  const columns = useMemo(
    () =>
      buildColumns<ToolUsage>([
        { id: "tool", header: "Tool", rowHeader: true, sortValue: (row) => row.tool_name, cell: (row) => row.tool_name },
        { id: "server", header: "Server", sortValue: (row) => row.server, cell: (row) => row.server },
        { id: "events", header: "Requests", numeric: true, sortValue: (row) => row.events, cell: (row) => row.events.toLocaleString() },
        { id: "denied", header: "Denied", numeric: true, sortValue: (row) => row.denied, cell: (row) => row.denied.toLocaleString() },
        {
          id: "last",
          header: "Last seen",
          sortValue: (row) => Date.parse(row.last_seen || "") || 0,
          cell: (row) => <span title={formatAbsolute(row.last_seen)}>{formatTimestamp(row.last_seen)}</span>,
        },
      ]),
    []
  );

  return (
    <DataTable
      columns={columns}
      rows={rows}
      rowKey={(row) => `${row.server}/${row.tool_name}/${row.agent_id || ""}/${row.human_id || ""}`}
      caption="Tools invoked in scope, with request and deny counts per server."
      regionLabel="Tool usage"
      testId="tool-usage-table"
      rowTestId="tool-usage-row"
      emptyTestId="tool-usage-empty"
      emptyMessage="No tool activity in this window."
    />
  );
}

export function RecentActivityTable({ rows }: { rows: RecentActivity[] }) {
  return (
    <div className="table-region" data-testid="recent-activity-scroll" tabIndex={0} role="region" aria-label="Recent gateway activity">
      <table className="data-table" data-testid="recent-activity-table">
        <caption className="visually-hidden">Recent gateway activity in your accessible namespaces.</caption>
        <thead><tr><th scope="col">Time</th><th scope="col">Server</th><th scope="col">Tool</th><th scope="col">Decision</th></tr></thead>
        <tbody>
          {rows.length === 0 ? <tr><td colSpan={4} className="table-empty">No recent activity in this window.</td></tr> : rows.map((row, index) => (
            <tr key={`${row.timestamp}-${row.server || ""}-${row.tool_name || ""}-${index}`}>
              <th scope="row"><time dateTime={row.timestamp}>{formatTimestamp(row.timestamp)}</time></th>
              <td>{row.server || "—"}<span className="cell-detail">{row.namespace || ""}</span></td>
              <td>{row.tool_name || row.event_type || "—"}</td>
              <td>{row.decision || "unknown"}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// Aggregate totals compared against each other. The usage API returns totals,
// not timestamped buckets, so there is no time series to draw here.
export function ServerUsageRanking({ rows }: { rows: ServerUsage[] }) {
  const ranked = useMemo(
    () => [...rows].sort((a, b) => b.events - a.events).slice(0, 8),
    [rows]
  );

  if (ranked.length === 0) {
    return null;
  }

  return (
    <ProportionList
      label="Requests by server"
      unit="requests"
      testId="server-usage-ranking"
      rows={ranked.map((row) => ({
        id: `${row.namespace}/${row.server}`,
        name: row.server,
        value: row.events,
        detail: row.denied > 0 ? `${row.denied.toLocaleString()} denied` : undefined,
        tone: row.denied > 0 ? "danger" : "accent",
      }))}
    />
  );
}
