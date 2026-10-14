import { useMemo, useState } from "react";
import { grafanaPath } from "../../api/config";
import { AsyncSection } from "./AsyncSection";
import { StatusBadge, decisionTone } from "../../ui/Badge";
import { Button, ButtonLink } from "../../ui/Button";
import { DataTable, buildColumns } from "../../ui/DataTable";
import { SelectField, TextField } from "../../ui/Field";
import { FilterBar } from "../../ui/FilterBar";
import { PageHeader } from "../../ui/PageHeader";
import { ProportionList } from "../../ui/ProportionBar";
import { MetricGrid } from "../../ui/MetricCard";
import { UsageMetrics } from "../usage/UsageMetrics";
import { UsageSeries } from "../usage/UsageSeries";
import { formatAbsolute, formatTimestamp } from "../../lib/format";
import { useAdminReload, useTeams, useUsage } from "../../hooks/useAdminData";
import type {
  RequestUsage,
  UsageFilterParams,
  UsageResponse,
} from "../../api/types";

type Props = { onSignIn: () => void };
type Actor = NonNullable<UsageResponse["actors"]>[number];
const WINDOW_OPTIONS = [
  { value: "1", label: "Last hour" },
  { value: "6", label: "Last 6 hours" },
  { value: "24", label: "Last 24 hours" },
  { value: "168", label: "Last 7 days" },
  { value: "720", label: "Last 30 days" },
];
const options = (values: string[], label: string, selected = "") => [
  { value: "", label },
  ...[...new Set([...values, selected].filter(Boolean))]
    .sort()
    .map((value) => ({ value, label: value })),
];
const ms = (value?: number) =>
  value === undefined
    ? "—"
    : `${value.toLocaleString(undefined, { maximumFractionDigits: 1 })} ms`;
const bytes = (value?: number) =>
  value === undefined
    ? "—"
    : value < 1024
      ? `${value} B`
      : value < 1048576
        ? `${(value / 1024).toFixed(1)} KiB`
        : `${(value / 1048576).toFixed(1)} MiB`;

export function traceExploreURL(traceID: string): string | undefined {
  if (!/^[0-9a-f]{32}$/i.test(traceID)) return undefined;
  const left = {
    datasource: "tempo",
    queries: [
      {
        refId: "A",
        queryType: "traceId",
        query: traceID,
        datasource: { type: "tempo", uid: "tempo" },
      },
    ],
    range: { from: "now-1h", to: "now" },
  };
  return `${grafanaPath()}/explore?left=${encodeURIComponent(JSON.stringify(left))}`;
}

export function UsageAnalyticsPanel({ onSignIn }: Props) {
  const [limit, setLimit] = useState("25");
  const [filters, setFilters] = useState<UsageFilterParams>({
    window_hours: "24",
  });
  const [teamSearch, setTeamSearch] = useState("");
  const reload = useAdminReload();
  const query = useUsage(true, limit, filters);
  const teamsQuery = useTeams(true);
  const usage = query.data;
  const teams = teamsQuery.data || [];
  const facets = usage?.facets;
  const change = (key: keyof UsageFilterParams, value: string) =>
    setFilters((current) => ({ ...current, [key]: value }));
  const teamNames = new Map(
    teams.map((team) => [team.id, team.name || team.slug]),
  );
  const matchingTeams = teams.filter(
    (team) =>
      team.id === filters.team_id ||
      `${team.name} ${team.slug}`
        .toLowerCase()
        .includes(teamSearch.toLowerCase().trim()),
  );
  const actorColumns = useMemo(
    () =>
      buildColumns<Actor>([
        {
          id: "human",
          header: "Human",
          rowHeader: true,
          sortValue: (r) => r.human_id || "",
          cell: (r) => r.human_id || "Anonymous / unreported",
        },
        {
          id: "agent",
          header: "Agent",
          sortValue: (r) => r.agent_id || "",
          cell: (r) => r.agent_id || "—",
        },
        {
          id: "events",
          header: "Requests",
          numeric: true,
          sortValue: (r) => r.events,
          cell: (r) => r.events.toLocaleString(),
        },
        {
          id: "servers",
          header: "Servers",
          numeric: true,
          sortValue: (r) => r.unique_servers,
          cell: (r) => String(r.unique_servers),
        },
        {
          id: "denied",
          header: "Denied",
          numeric: true,
          sortValue: (r) => r.denied,
          cell: (r) => r.denied.toLocaleString(),
        },
      ]),
    [],
  );
  const requestColumns = useMemo(
    () =>
      buildColumns<RequestUsage>([
        {
          id: "server",
          header: "Server",
          rowHeader: true,
          sortValue: (r) => `${r.namespace}/${r.server}`,
          cell: (r) => (
            <>
              {r.server}
              <span className="cell-detail">{r.namespace}</span>
            </>
          ),
        },
        {
          id: "team",
          header: "Team / scope",
          sortValue: (r) => teamNames.get(r.team_id || "") || r.namespace,
          cell: (r) =>
            teamNames.get(r.team_id || "") ||
            (r.namespace === "mcp-servers-public"
              ? "Public"
              : r.namespace === "mcp-servers-org"
                ? "Organization"
                : r.team_id || "Other"),
        },
        {
          id: "method",
          header: "MCP method",
          sortValue: (r) => r.rpc_method,
          cell: (r) => <code>{r.rpc_method || "HTTP transport"}</code>,
        },
        {
          id: "tool",
          header: "Tool",
          sortValue: (r) => r.tool_name || "",
          cell: (r) => r.tool_name || "—",
        },
        {
          id: "events",
          header: "Requests",
          numeric: true,
          sortValue: (r) => r.events,
          cell: (r) => r.events.toLocaleString(),
        },
        {
          id: "decision",
          header: "Policy decision",
          sortValue: (r) => r.decision,
          cell: (r) => (
            <StatusBadge tone={decisionTone(r.decision)}>
              {r.decision || "unknown"}
            </StatusBadge>
          ),
        },
        {
          id: "policy",
          header: "Policy version",
          sortValue: (r) => r.policy_version || "",
          cell: (r) => r.policy_version || "—",
        },
        {
          id: "errors",
          header: "HTTP errors",
          numeric: true,
          sortValue: (r) => r.http_failures,
          cell: (r) => r.http_failures.toLocaleString(),
        },
        {
          id: "latency",
          header: "p95 latency",
          numeric: true,
          sortValue: (r) => r.p95_latency_ms,
          cell: (r) => ms(r.p95_latency_ms),
        },
        {
          id: "last",
          header: "Last seen",
          sortValue: (r) => Date.parse(r.last_seen),
          cell: (r) => (
            <time dateTime={r.last_seen} title={formatAbsolute(r.last_seen)}>
              {formatTimestamp(r.last_seen)}
            </time>
          ),
        },
        {
          id: "trace",
          header: "Latest trace",
          cell: (r) => {
            const url = traceExploreURL(r.trace_id || "");
            return url ? (
              <a href={url} target="_blank" rel="noreferrer" title={r.trace_id}>
                Open trace
              </a>
            ) : (
              "—"
            );
          },
        },
      ]),
    [teamsQuery.data],
  );
  const decisions = usage?.decisions || [];
  const totals = usage?.totals;
  const reset = () => {
    setFilters({ window_hours: "24" });
    setTeamSearch("");
  };
  return (
    <>
      <PageHeader
        title="Usage analytics"
        breadcrumb={[{ label: "Administration" }, { label: "Usage analytics" }]}
        description="Request activity across public, organization and team servers. Filters apply to the chart, totals and request table."
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
      <FilterBar label="Analytics filters">
        <SelectField
          label="Time range"
          value={filters.window_hours || "24"}
          options={WINDOW_OPTIONS}
          onChange={(e) => change("window_hours", e.target.value)}
          data-testid="analytics-window"
        />
        <SelectField
          label="Scope"
          value={filters.scope || ""}
          options={[
            { value: "", label: "All scopes" },
            { value: "public", label: "Public" },
            { value: "org", label: "Organization" },
            { value: "tenant", label: "Teams" },
          ]}
          onChange={(e) => {
            setFilters((current) => ({
              ...current,
              scope: e.target.value,
              team_id: "",
              server: "",
              rpc_method: "",
              tool_name: "",
            }));
            setTeamSearch("");
          }}
          data-testid="analytics-scope"
        />
        {filters.scope === "tenant" ? (
          <>
            <TextField
              label="Find a team"
              type="search"
              value={teamSearch}
              onChange={(e) => setTeamSearch(e.target.value)}
              placeholder="Team name or slug"
              data-testid="analytics-team-search"
            />
            <SelectField
              label="Team"
              value={filters.team_id || ""}
              options={[
                {
                  value: "",
                  label: teamsQuery.isPending ? "Loading teams…" : "All teams",
                },
                ...matchingTeams.map((t) => ({
                  value: t.id,
                  label: t.name || t.slug,
                })),
              ]}
              disabled={teamsQuery.isPending || !!teamsQuery.error}
              onChange={(e) =>
                setFilters((current) => ({
                  ...current,
                  team_id: e.target.value,
                  server: "",
                  rpc_method: "",
                  tool_name: "",
                }))
              }
              data-testid="analytics-team"
            />
          </>
        ) : null}
        <SelectField
          label="Server"
          value={filters.server || ""}
          options={options(
            facets?.servers || [],
            "All servers",
            filters.server,
          )}
          onChange={(e) => change("server", e.target.value)}
          data-testid="analytics-server"
        />
        <SelectField
          label="MCP method"
          value={filters.rpc_method || ""}
          options={options(
            facets?.methods || [],
            "All methods",
            filters.rpc_method,
          )}
          onChange={(e) =>
            setFilters((current) => ({
              ...current,
              rpc_method: e.target.value,
              tool_name: "",
            }))
          }
          data-testid="analytics-method"
        />
        <SelectField
          label="Tool"
          value={filters.tool_name || ""}
          options={options(facets?.tools || [], "All tools", filters.tool_name)}
          disabled={!!filters.rpc_method && filters.rpc_method !== "tools/call"}
          onChange={(e) =>
            setFilters((current) => ({
              ...current,
              tool_name: e.target.value,
              rpc_method: e.target.value ? "tools/call" : current.rpc_method,
            }))
          }
          data-testid="analytics-tool"
        />
        <SelectField
          label="Policy decision"
          value={filters.decision || ""}
          options={[
            { value: "", label: "All decisions" },
            { value: "allow", label: "Allowed" },
            { value: "deny", label: "Denied" },
          ]}
          onChange={(e) => change("decision", e.target.value)}
          data-testid="analytics-decision"
        />
        <SelectField
          label="Policy version"
          value={filters.policy_version || ""}
          options={options(
            facets?.policy_versions || [],
            "All versions",
            filters.policy_version,
          )}
          onChange={(e) => change("policy_version", e.target.value)}
          data-testid="analytics-policy"
        />
        <SelectField
          label="HTTP outcome"
          value={filters.http_status || ""}
          options={[
            { value: "", label: "All outcomes" },
            { value: "2xx", label: "2xx success" },
            { value: "4xx", label: "4xx client errors" },
            { value: "5xx", label: "5xx server errors" },
          ]}
          onChange={(e) => change("http_status", e.target.value)}
          data-testid="analytics-http-status"
        />
        <SelectField
          label="Rows"
          value={limit}
          options={[
            { value: "10", label: "Top 10 groups" },
            { value: "25", label: "Top 25 groups" },
            { value: "50", label: "Top 50 groups" },
          ]}
          onChange={(e) => setLimit(e.target.value)}
          data-testid="analytics-limit"
        />
        <Button variant="ghost" onClick={reset} data-testid="analytics-clear">
          Reset filters
        </Button>
      </FilterBar>
      {filters.scope === "tenant" && teamsQuery.error ? (
        <p role="alert">Teams could not be loaded. Retry with Refresh.</p>
      ) : null}
      <AsyncSection
        query={query}
        loadingLabel="Loading usage analytics…"
        errorTitle="Analytics is unavailable."
        onRetry={reload}
        onSignIn={onSignIn}
        testId="analytics"
      >
        {usage ? (
          <>
            <p className="section-note">
              Recorded gateway requests only. Servers without a gateway and
              requests whose analytics were not delivered are not included.
            </p>
            <UsageMetrics
              totals={usage.totals}
              scope="the selected filters"
              testId="analytics-stats"
            />
            <MetricGrid
              label="Request reliability and usage"
              testId="analytics-reliability"
              metrics={[
                {
                  label: "HTTP errors",
                  value: totals?.http_failures,
                  hint: "Responses with HTTP status 400 or higher, including denials",
                },
                {
                  label: "Server errors",
                  value: totals?.server_errors,
                  hint: "HTTP 5xx responses",
                },
                { label: "p95 latency", value: ms(totals?.p95_latency_ms) },
                {
                  label: "Average latency",
                  value: ms(totals?.average_latency_ms),
                },
                {
                  label: "Active users",
                  value: totals?.unique_humans,
                  hint: "Identified callers; anonymous requests are not unique users",
                },
                { label: "Active agents", value: totals?.unique_agents },
                { label: "Sessions", value: totals?.unique_sessions },
                { label: "Response traffic", value: bytes(totals?.bytes_out) },
              ]}
            />
            {usage.series?.length ? (
              <UsageSeries
                points={usage.series}
                bucketSeconds={usage.bucket_seconds}
              />
            ) : null}
            <section
              className="section"
              aria-labelledby="analytics-requests-title"
            >
              <div className="section-head">
                <div>
                  <h2 className="section-title" id="analytics-requests-title">
                    Server requests
                  </h2>
                  <p className="section-note">
                    Requests grouped by server, MCP method, tool and policy. Top{" "}
                    {limit} groups by volume; refreshes every 15 seconds. Trace
                    links open the latest request in each group.
                  </p>
                </div>
              </div>
              <DataTable
                columns={requestColumns}
                rows={usage.requests || []}
                rowKey={(r) =>
                  JSON.stringify([
                    r.namespace,
                    r.server,
                    r.team_id,
                    r.rpc_method,
                    r.tool_name,
                    r.decision,
                    r.policy_version,
                  ])
                }
                caption="Server requests by MCP method, tool and policy decision for the selected filters."
                regionLabel="Server requests"
                testId="analytics-requests-table"
                rowTestId="analytics-request-row"
                minWidth={1250}
                emptyMessage="No server requests match these filters."
                pageNote={`Top ${limit} request groups in this time range; totals and chart include all matching requests.`}
              />
            </section>
            {decisions.length ? (
              <section className="section">
                <div className="section-head">
                  <h2 className="section-title">Policy decisions</h2>
                </div>
                <ProportionList
                  label="Policy decision breakdown"
                  unit="requests"
                  testId="analytics-decision-breakdown"
                  rows={decisions.map((r) => ({
                    id: r.decision,
                    name: r.decision,
                    value: r.events,
                    tone: r.decision === "deny" ? "danger" : "accent",
                  }))}
                />
              </section>
            ) : null}
            {usage.actors?.length ? (
              <section className="section">
                <div className="section-head">
                  <h2 className="section-title">Active callers</h2>
                </div>
                <DataTable
                  columns={actorColumns}
                  rows={usage.actors}
                  rowKey={(r) => `${r.human_id}/${r.agent_id}`}
                  caption="Identified callers ranked by request volume in the selected filters."
                  regionLabel="Actor usage"
                  testId="analytics-actors-table"
                  emptyMessage="No identified callers in this window."
                />
              </section>
            ) : null}
          </>
        ) : null}
      </AsyncSection>
    </>
  );
}
