import { grafanaPath } from "../../api/config";
import { useMemo, useState } from "react";

import { AsyncSection } from "./AsyncSection";
import { StatusBadge, decisionTone } from "../../ui/Badge";
import { Button, ButtonLink } from "../../ui/Button";
import { DataTable, buildColumns } from "../../ui/DataTable";
import { SelectField } from "../../ui/Field";
import { FilterBar } from "../../ui/FilterBar";
import { PageHeader } from "../../ui/PageHeader";
import { ProportionList } from "../../ui/ProportionBar";
import { ErrorState, LoadingState } from "../../ui/States";
import { UsageMetrics } from "../usage/UsageMetrics";
import { ServerUsageTable, ToolUsageTable } from "../usage/UsageTables";
import { UsageSeries } from "../usage/UsageSeries";
import { formatAbsolute, formatTimestamp } from "../../lib/format";
import { useAdminReload, useUsage } from "../../hooks/useAdminData";
import { useQuery } from "@tanstack/react-query";
import { listEvents } from "../../api/admin";
import { ADMIN_QUERY_KEY } from "../../hooks/useAdminData";
import type { GatewayEvent, UsageResponse } from "../../api/types";

type UsageAnalyticsPanelProps = { onSignIn: () => void };

const LIMIT_OPTIONS = [
  { value: "10", label: "Top 10" },
  { value: "25", label: "Top 25" },
  { value: "50", label: "Top 50" },
];

type Actor = NonNullable<UsageResponse["actors"]>[number];

export function UsageAnalyticsPanel({ onSignIn }: UsageAnalyticsPanelProps) {
  const [limit, setLimit] = useState("10");
  const reload = useAdminReload();
  const query = useUsage(true, limit);
  const usage = query.data;

  // Recent gateway decisions, the one legacy-dashboard view that had no React
  // equivalent. Same authorized /events read the access drill-down uses.
  const decisionsQuery = useQuery({
    queryKey: [ADMIN_QUERY_KEY, "recent-events", limit],
    queryFn: () => listEvents({ limit: String(Math.max(Number(limit) || 10, 25)) }),
  });

  const actorColumns = useMemo(
    () =>
      buildColumns<Actor>([
        {
          id: "human",
          header: "Human",
          rowHeader: true,
          sortValue: (row) => row.human_id || "",
          cell: (row) => row.human_id || "—",
        },
        { id: "agent", header: "Agent", sortValue: (row) => row.agent_id || "", cell: (row) => row.agent_id || "—" },
        { id: "events", header: "Requests", numeric: true, sortValue: (row) => row.events, cell: (row) => row.events.toLocaleString() },
        { id: "servers", header: "Servers", numeric: true, sortValue: (row) => row.unique_servers, cell: (row) => String(row.unique_servers) },
        { id: "tools", header: "Tools", numeric: true, sortValue: (row) => row.unique_tools, cell: (row) => String(row.unique_tools) },
        { id: "denied", header: "Denied", numeric: true, sortValue: (row) => row.denied, cell: (row) => row.denied.toLocaleString() },
      ]),
    []
  );

  const eventColumns = useMemo(
    () =>
      buildColumns<GatewayEvent>([
        {
          id: "time",
          header: "Time",
          rowHeader: true,
          sortValue: (event) => Date.parse(event.timestamp || "") || 0,
          cell: (event) => (
            <span title={formatAbsolute(event.timestamp)}>{formatTimestamp(event.timestamp)}</span>
          ),
        },
        { id: "namespace", header: "Namespace", sortValue: (event) => event.namespace || "", cell: (event) => event.namespace || "—" },
        { id: "tool", header: "Tool", sortValue: (event) => event.tool_name || "", cell: (event) => event.tool_name || "—" },
        {
          id: "decision",
          header: "Decision",
          sortValue: (event) => event.decision || "",
          cell: (event) => (
            <StatusBadge tone={decisionTone(event.decision)}>{event.decision || "unknown"}</StatusBadge>
          ),
        },
        {
          id: "reason",
          header: "Reason",
          cell: (event) => <span className="wrap-anywhere">{(event.payload?.reason as string) || "—"}</span>,
        },
      ]),
    []
  );

  const decisions = usage?.decisions ?? [];
  const decisionTotal = decisions.reduce((sum, row) => sum + row.events, 0);

  return (
    <>
      <PageHeader
        title="Usage analytics"
        breadcrumb={[{ label: "Administration" }, { label: "Usage analytics" }]}
        description="Gateway events aggregated across the platform by server, actor, tool, and decision."
        actions={
          <>
            <ButtonLink
              variant="secondary"
              href={`${grafanaPath()}/explore`}
              target="_blank"
              rel="noreferrer"
              trailingIcon="external"
              data-testid="analytics-detailed-activity"
            >
              Detailed activity
            </ButtonLink>
            <Button
              variant="secondary"
              icon="refresh"
              onClick={reload}
              busy={query.isFetching && !query.isPending}
              data-testid="analytics-refresh"
            >
              Refresh
            </Button>
          </>
        }
      />

      <FilterBar label="Analytics options">
        <SelectField
          label="Rows"
          value={limit}
          options={LIMIT_OPTIONS}
          hint="How many rows the analytics API returns per table."
          data-testid="analytics-limit"
          onChange={(event) => setLimit(event.target.value)}
        />
      </FilterBar>

      <AsyncSection
        query={query}
        loadingLabel="Loading usage analytics…"
        errorTitle="Analytics is unavailable."
        onRetry={reload}
        onSignIn={onSignIn}
        testId="analytics"
      >
        <>
          {usage ? <UsageMetrics totals={usage.totals} scope="the whole platform" testId="analytics-stats" /> : null}

          {usage?.series?.length ? <UsageSeries points={usage.series} /> : null}

          {decisions.length > 0 ? (
            <section className="section">
              <div className="section-head">
                <h2 className="section-title" id="analytics-decisions-title">
                  Decisions
                </h2>
                <p className="section-note">Totals across the window the analytics API aggregated.</p>
              </div>
              <ProportionList
                label="Gateway decisions"
                unit="events"
                testId="analytics-decision-breakdown"
                rows={decisions.map((row) => ({
                  id: row.decision,
                  name: row.decision,
                  value: row.events,
                  detail: decisionTotal ? `${Math.round((row.events / decisionTotal) * 100)}%` : undefined,
                  tone: row.decision.toLowerCase().startsWith("deny") ? "danger" : "accent",
                }))}
              />
            </section>
          ) : null}

          <section className="section">
            <div className="section-head">
              <h2 className="section-title" id="analytics-servers-title">
                Servers
              </h2>
            </div>
            <ServerUsageTable rows={usage?.servers ?? []} />
          </section>

          <section className="section">
            <div className="section-head">
              <h2 className="section-title" id="analytics-tools-title">
                Tools
              </h2>
            </div>
            <ToolUsageTable rows={usage?.tools ?? []} />
          </section>

          {usage?.actors && usage.actors.length > 0 ? (
            <section className="section">
              <div className="section-head">
                <h2 className="section-title" id="analytics-actors-title">
                  Actors
                </h2>
              </div>
              <DataTable
                columns={actorColumns}
                rows={usage.actors}
                rowKey={(row) => `${row.human_id}/${row.agent_id}`}
                caption="Humans and agents by request volume."
                regionLabel="Actor usage"
                testId="analytics-actors-table"
                emptyMessage="No actor activity recorded."
              />
            </section>
          ) : null}
        </>
      </AsyncSection>

      <section className="section">
        <div className="section-head">
          <h2 className="section-title" id="analytics-events-title">
            Recent policy decisions
          </h2>
          <p className="section-note">The most recent gateway decisions the analytics service has stored.</p>
        </div>
        {decisionsQuery.isPending ? (
          <LoadingState label="Loading recent decisions…" testId="analytics-events-loading" />
        ) : decisionsQuery.error ? (
          <ErrorState
            title="Recent decisions are unavailable."
            detail="Gateway decision events come from the analytics service, which is not answering on this cluster. This is not the same as no traffic."
            testId="analytics-events-error"
          />
        ) : (
          <DataTable
            columns={eventColumns}
            rows={decisionsQuery.data ?? []}
            rowKey={(event) =>
              `${event.timestamp || ""}-${event.tool_name || ""}-${event.decision || ""}-${event.namespace || ""}`
            }
            caption="Recent gateway decisions with namespace, tool, decision, and reason."
            regionLabel="Recent policy decisions"
            testId="analytics-events-table"
            emptyMessage="No gateway decisions recorded yet."
          />
        )}
      </section>
    </>
  );
}
