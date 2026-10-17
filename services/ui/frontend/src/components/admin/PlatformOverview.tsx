import { useQuery } from "@tanstack/react-query";
import { readOverviewDirectory, readOverviewServers } from "../../api/workspace";
import { listComponents, readOperations } from "../../api/admin";
import { AsyncSection } from "./AsyncSection";
import { PageHeader } from "../../ui/PageHeader";

export function PlatformOverview({ onSignIn }: { onSignIn: () => void }) {
  const directory = useQuery({ queryKey: ["admin", "overview", "directory"], queryFn: readOverviewDirectory });
  const servers = useQuery({ queryKey: ["admin", "overview", "servers"], queryFn: readOverviewServers });
  const health = useQuery({ queryKey: ["admin", "overview", "health"], queryFn: listComponents });
  const operations = useQuery({ queryKey: ["admin", "overview", "audit"], queryFn: () => readOperations({ limit: "8" }) });
  return <>
    <PageHeader title="Platform overview" description="Platform-wide resources, deployment failures, health, and administrative audit changes." />
    <p><a href="#/workspace">My workspace</a> shows your personal membership separately from administrator authority.</p>
    <section className="section" aria-labelledby="overview-counts-title"><h2 id="overview-counts-title">Platform-wide totals</h2>
      <AsyncSection query={directory} loadingLabel="Loading directory totals…" errorTitle="Directory totals are unavailable." onRetry={() => void directory.refetch()} onSignIn={onSignIn} testId="overview-directory">
        <dl className="overview-grid">{directory.data ? [
          ["Teams", directory.data.teams, "#/admin/teams"], ["Users", directory.data.users, "#/admin/operations"],
          ["Agents", directory.data.agents, "#/agents"], ["Active sessions", directory.data.active_sessions, "#/access"],
        ].map(([label, count, href]) => <div className="overview-card" key={label}><dt><a href={String(href)}>{label}</a></dt><dd>{count}</dd></div>) : null}</dl>
        <p>Directory totals include non-deleted teams/users and all agents of current teams. Active sessions exclude revoked, expired, retired-server and inactive-agent sessions.</p>
      </AsyncSection>
      <AsyncSection query={servers} loadingLabel="Loading server totals…" errorTitle="Server totals are unavailable." onRetry={() => void servers.refetch()} onSignIn={onSignIn} testId="overview-servers">
        <p>Servers: <strong>{servers.data?.servers}</strong> · <a href="#/servers">Open server catalog</a></p>
        <h3>Deployment failures</h3>{servers.data?.failures.length ? <ul>{servers.data.failures.map(item => <li key={`${item.namespace}/${item.name}`}><a href={`#/servers?namespace=${encodeURIComponent(item.namespace)}&server=${encodeURIComponent(`${item.namespace}/${item.name}`)}`}>{item.namespace}/{item.name}</a> · {item.status}</li>)}</ul> : <p>No reported deployment failures.</p>}
      </AsyncSection>
    </section>
    <section className="section" aria-labelledby="overview-health-title"><h2 id="overview-health-title">Platform health</h2>
      <AsyncSection query={health} loadingLabel="Loading health…" errorTitle="Platform health is unavailable." onRetry={() => void health.refetch()} onSignIn={onSignIn} testId="overview-health">
        {health.data?.length ? <ul>{health.data.map(item => <li key={item.key}>{item.display} · {item.status} {item.ready}</li>)}</ul> : <p>No platform components reported.</p>}
      </AsyncSection><a href="#/admin/platform">Inspect component health</a>
    </section>
    <section className="section" aria-labelledby="overview-audit-title"><h2 id="overview-audit-title">Recent administrative audit changes</h2><p>Recent platform audit records, including administration and resource management by authorized users.</p>
      <AsyncSection query={operations} loadingLabel="Loading audit changes…" errorTitle="Audit changes are unavailable." onRetry={() => void operations.refetch()} onSignIn={onSignIn} testId="overview-audit">
        {operations.data?.audit_logs.length ? <ul className="overview-resource-list">{operations.data.audit_logs.slice(0, 8).map((item, index) => <li key={`${item.created_at}:${index}`}>{item.created_at} · {item.action} · {item.resource} · {item.status}</li>)}</ul> : <p>No recent administrative audit records.</p>}
      </AsyncSection><a href="#/admin/operations">Open Operations audit trail</a> · <a href="#/admin/analytics">Open Usage analytics</a>
    </section>
  </>;
}
