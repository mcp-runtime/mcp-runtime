import { useQuery } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";

import { TeamMembershipPanel } from "./TeamMembershipPanel";
import { UsageMetrics } from "../usage/UsageMetrics";
import { RecentActivityTable, ServerUsageRanking, ServerUsageTable, ToolUsageTable } from "../usage/UsageTables";
import { Button } from "../../ui/Button";
import { SelectField } from "../../ui/Field";
import { FilterBar } from "../../ui/FilterBar";
import { PageHeader } from "../../ui/PageHeader";
import { EmptyState, ErrorState, LoadingState } from "../../ui/States";
import { UnauthorizedError } from "../../api/client";
import { listTeams, readUserUsage } from "../../api/userWorkflows";
import type { AuthStatus } from "../../api/types";
import { isTenantUser } from "../../api/types";

const WINDOW_OPTIONS = [
  { value: "1", label: "Last 24 hours" },
  { value: "7", label: "Last 7 days" },
  { value: "30", label: "Last 30 days" },
  { value: "90", label: "Last 90 days" },
];

type ActivityWorkspaceProps = {
  auth: AuthStatus;
  onSignIn: () => void;
};

export function ActivityWorkspace({ auth, onSignIn }: ActivityWorkspaceProps) {
  const [windowDays, setWindowDays] = useState(7);
  const [server, setServer] = useState("");

  // Activity is the tenant view. Admins use the
  // org-wide Analytics surface instead.
  const enabled = isTenantUser(auth);

  const usageQuery = useQuery({
    queryKey: ["user", "usage", windowDays, server],
    queryFn: () => readUserUsage(windowDays, server || undefined),
    enabled,
  });

  const teamsQuery = useQuery({
    queryKey: ["user", "teams"],
    queryFn: listTeams,
    enabled,
  });

  // A response filtered to one server no longer lists the others, so the
  // options are remembered from the unfiltered reads instead of collapsing to
  // the single selected server.
  const [knownServers, setKnownServers] = useState<string[]>([]);
  useEffect(() => {
    const names = (usageQuery.data?.servers ?? []).map((row) => row.server).filter(Boolean);
    if (names.length === 0) {
      return;
    }
    setKnownServers((current) => {
      const merged = new Set([...current, ...names]);
      const next = [...merged].sort();
      return next.length === current.length && next.every((name, index) => name === current[index])
        ? current
        : next;
    });
  }, [usageQuery.data]);

  const serverOptions = useMemo(
    () => [
      { value: "", label: "All servers" },
      ...knownServers.map((name) => ({ value: name, label: name })),
    ],
    [knownServers]
  );

  if (!auth.authenticated) {
    return (
      <>
        <PageHeader title="Activity" />
        <EmptyState
          icon="activity"
          title="Sign in to see your activity."
          detail="Usage is scoped to the MCP servers in your user and team namespaces."
          testId="activity-signed-out"
          action={
            <Button variant="primary" icon="login" onClick={onSignIn}>
              Sign in
            </Button>
          }
        />
      </>
    );
  }

  if (!enabled) {
    return (
      <>
        <PageHeader title="Activity" />
        <EmptyState
          icon="activity"
          title="This view is for tenant accounts."
          detail="Administrators see org-wide usage in Administration → Usage analytics."
          testId="activity-admin-hidden"
        />
      </>
    );
  }

  const windowLabel = WINDOW_OPTIONS.find((option) => option.value === String(windowDays))?.label ?? "";
  const scope = server ? `${server}, ${windowLabel.toLowerCase()}` : `your namespaces, ${windowLabel.toLowerCase()}`;

  const header = (
    <PageHeader
      title="Activity"
      description="Gateway decisions for the MCP servers in your user and team namespaces."
      actions={
        <Button
          variant="secondary"
          icon="refresh"
          busy={usageQuery.isFetching && !usageQuery.isPending}
          onClick={() => void usageQuery.refetch()}
          data-testid="activity-refresh"
        >
          {usageQuery.isFetching && !usageQuery.isPending ? "Refreshing…" : "Refresh"}
        </Button>
      }
    />
  );

  const membershipSection = (<>
    {teamsQuery.isPending ? (
        <LoadingState label="Loading your teams…" testId="teams-loading" />
      ) : teamsQuery.error instanceof UnauthorizedError ? (
        <ErrorState title="Your session expired." detail="Sign in again to see team memberships." onRetry={onSignIn} retryLabel="Sign in" testId="teams-unauthorized" />
      ) : teamsQuery.error ? (
        <ErrorState
          title="Your team membership could not be loaded."
          detail={teamsQuery.error instanceof Error ? teamsQuery.error.message : ""}
          onRetry={() => void teamsQuery.refetch()}
          testId="teams-error"
        />
      ) : (
        <TeamMembershipPanel teams={teamsQuery.data ?? []} />
      )}
  </>);

  if (usageQuery.isPending) {
    return (
      <>
        {header}
        {membershipSection}
        <LoadingState label="Loading your activity…" testId="activity-loading" />
      </>
    );
  }

  if (usageQuery.error instanceof UnauthorizedError) {
    return (
      <>
        {header}
        {membershipSection}
        <ErrorState
          title="Your session expired."
          detail="Sign in again to see your activity."
          onRetry={onSignIn}
          retryLabel="Sign in"
          testId="activity-unauthorized"
        />
      </>
    );
  }

  if (usageQuery.error) {
    return (
      <>
        {header}
        {membershipSection}
        <ErrorState
          title="Activity is unavailable."
          detail={
            usageQuery.error instanceof Error
              ? `The analytics service did not answer: ${usageQuery.error.message}`
              : "The analytics service did not respond."
          }
          onRetry={() => void usageQuery.refetch()}
          testId="activity-error"
        />
      </>
    );
  }

  const usage = usageQuery.data;
  const servers = usage?.servers ?? [];
  const tools = usage?.tools ?? [];

  return (
    <>
      {header}

      <FilterBar label="Filter activity">
        <SelectField
          label="Server"
          value={server}
          options={serverOptions}
          data-testid="activity-server-filter"
          onChange={(event) => setServer(event.target.value)}
        />
        <SelectField
          label="Time range"
          value={String(windowDays)}
          options={WINDOW_OPTIONS}
          data-testid="activity-window-filter"
          onChange={(event) => setWindowDays(Number(event.target.value))}
        />
      </FilterBar>

      {usage ? <UsageMetrics totals={usage.totals} scope={scope} /> : null}

      {servers.length > 0 ? (
        <section className="section">
          <div className="section-head">
            <h2 className="section-title" id="activity-ranking-title">
              Requests by server
            </h2>
            <p className="section-note">Totals for the selected window, not a trend over time.</p>
          </div>
          <ServerUsageRanking rows={servers} />
        </section>
      ) : null}

      <section className="section">
        <div className="section-head">
          <h2 className="section-title" id="activity-servers-title">
            Servers
          </h2>
        </div>
        <ServerUsageTable rows={servers} />
      </section>

      <section className="section">
        <div className="section-head">
          <h2 className="section-title" id="activity-tools-title">
            Tools
          </h2>
        </div>
        <ToolUsageTable rows={tools} />
      </section>

      <section className="section">
        <div className="section-head">
          <h2 className="section-title" id="activity-recent-title">Recent activity</h2>
        </div>
        <RecentActivityTable rows={usage?.recent ?? []} />
      </section>

      {membershipSection}
    </>
  );
}
