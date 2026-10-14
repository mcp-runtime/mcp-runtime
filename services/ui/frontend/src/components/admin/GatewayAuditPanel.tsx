import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { listEvents } from "../../api/admin";
import type { GatewayEvent } from "../../api/types";
import { ADMIN_QUERY_KEY } from "../../hooks/useAdminData";
import { StatusBadge, decisionTone } from "../../ui/Badge";
import { Button } from "../../ui/Button";
import { DataTable, buildColumns } from "../../ui/DataTable";
import { ErrorState, LoadingState } from "../../ui/States";
import { formatTimestamp } from "../../lib/format";
import { AccessLink } from "../access/accessLinks";

export function EventAccessLink({ event, kind }: { event: GatewayEvent; kind: "grant" | "session" }) {
  const name = event.payload?.[`matched_${kind}`];
  const namespace = event.payload?.[`matched_${kind}_namespace`];
  return typeof name === "string" && name && typeof namespace === "string" && namespace
    ? <AccessLink kind={kind} name={name} namespace={namespace} />
    : <span>{typeof name === "string" && name ? `${name} (namespace unreported)` : "Unreported"}</span>;
}

export function GatewayAuditPanel() {
  const [enabled, setEnabled] = useState(false);
  const query = useQuery({ queryKey: [ADMIN_QUERY_KEY, "gateway-audit"], queryFn: () => listEvents({ limit: "100" }), enabled });
  const columns = buildColumns<GatewayEvent>([
    { id: "time", header: "Time", rowHeader: true, cell: (event) => formatTimestamp(event.timestamp) },
    { id: "server", header: "Server", cell: (event) => `${event.namespace || "?"}/${event.server || "?"}` },
    { id: "tool", header: "Tool / method", cell: (event) => event.tool_name || String(event.payload?.rpc_method || "—") },
    { id: "decision", header: "Decision", cell: (event) => <StatusBadge tone={decisionTone(event.decision)}>{event.decision || "unknown"}</StatusBadge> },
    { id: "actor", header: "Actor team", cell: (event) => event.subject_team_id || String(event.payload?.subject_team_id || "Unknown") },
    { id: "authority", header: "Resource team", cell: (event) => event.resource_team_id || String(event.payload?.resource_team_id || "Unknown") },
    { id: "grant", header: "Matched grant", cell: (event) => <EventAccessLink event={event} kind="grant" /> },
    { id: "session", header: "Matched session", cell: (event) => <EventAccessLink event={event} kind="session" /> },
  ]);
  return <section className="section" aria-label="Gateway audit">
    <h2 className="section-title">Gateway audit</h2>
    <p className="section-note">The latest 100 gateway events across the platform. These are independent of the usage filters above. Access links use the exact recorded namespace and name.</p>
    <Button variant="secondary" onClick={() => { if (enabled) void query.refetch(); else setEnabled(true); }}>Load gateway decisions</Button>
    {enabled ? query.isPending ? <LoadingState label="Loading gateway audit…" /> : query.error ? <ErrorState title="Gateway audit could not be loaded." onRetry={() => void query.refetch()} /> : <DataTable columns={columns} rows={query.data ?? []} rowKey={(event) => JSON.stringify(event)} caption="Recent gateway decisions and their recorded access resources." regionLabel="Gateway audit events" testId="gateway-audit-table" emptyMessage="No recorded gateway events." /> : null}
  </section>;
}
