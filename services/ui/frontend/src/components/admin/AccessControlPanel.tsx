import { useMemo, useState } from "react";

import { GrantForm, SessionForm, type GrantDraft, type SessionDraft } from "./accessForms";
import { AsyncSection } from "./AsyncSection";
import { StatusBadge } from "../../ui/Badge";
import { Button } from "../../ui/Button";
import { ConfirmDialog, type ConfirmRequest } from "../../ui/ConfirmDialog";
import { DataTable, buildColumns } from "../../ui/DataTable";
import { SelectField, TextField } from "../../ui/Field";
import { FilterBar, FilterSummary, type FilterChip } from "../../ui/FilterBar";
import { Icon } from "../../ui/Icon";
import { MetricGrid } from "../../ui/MetricCard";
import { PageHeader } from "../../ui/PageHeader";
import { expiryState, formatAbsolute, formatTimestamp } from "../../lib/format";
import { useAdminReload, useGrants, useSessions } from "../../hooks/useAdminData";
import { useCatalog } from "../../hooks/useCatalog";
import { createGrant, createSession, revokeGrantSessions, setGrantDisabled, setSessionRevoked } from "../../api/admin";
import { readRuntimeConfig } from "../../api/config";
import { accessKey, subjectLabel } from "../../api/types";
import type { GrantSummary, SessionSummary } from "../../api/types";

export type AccessSelection =
  | { kind: "grant"; item: GrantSummary }
  | { kind: "session"; item: SessionSummary };

type AccessControlPanelProps = {
  namespace: string;
  onNamespaceChange: (namespace: string) => void;
  onSelect: (selection: AccessSelection) => void;
  onSignIn: () => void;
};

function defaultNamespace(current: string): string {
  return current.trim() || readRuntimeConfig().defaults.namespace || "mcp-servers";
}

function emptyGrantDraft(namespace: string): GrantDraft {
  return {
    name: "",
    namespace: defaultNamespace(namespace),
    server: "",
    humanID: "",
    agentID: "",
    teamID: "",
    maxTrust: "low",
    allowedSideEffects: ["read"],
    expiresAt: "",
  };
}

function emptySessionDraft(namespace: string): SessionDraft {
  return {
    name: "",
    namespace: defaultNamespace(namespace),
    server: "",
    humanID: "",
    agentID: "",
    teamID: "",
    consentedTrust: "low",
    expiresAt: "",
  };
}

// revoked=false alone does not mean a session can still be used: an expiry in
// the past ends it, and an expiry we cannot parse is reported as unknown.
export function sessionState(session: SessionSummary, now = Date.now()) {
  if (session.revoked) {
    return { tone: "attention" as const, label: "Revoked" };
  }
  switch (expiryState(session.expiresAt, now)) {
    case "expired":
      return { tone: "attention" as const, label: "Expired" };
    case "unparseable":
      return { tone: "unknown" as const, label: "Unknown expiry" };
    default:
      return { tone: "ready" as const, label: "Active" };
  }
}

function grantStatus(grant: GrantSummary, now = Date.now()) {
  if (grant.disabled) return { tone: "attention" as const, label: "Disabled" };
  switch (expiryState(grant.expiresAt, now)) {
    case "expired":
      return { tone: "attention" as const, label: "Expired" };
    case "unparseable":
      return { tone: "unknown" as const, label: "Unknown expiry" };
    default:
      return { tone: "ready" as const, label: "Active" };
  }
}

function expiryText(session: SessionSummary): string {
  switch (expiryState(session.expiresAt)) {
    case "none":
      return "No expiry";
    case "unparseable":
      return `Unreadable (${session.expiresAt})`;
    default:
      return formatTimestamp(session.expiresAt);
  }
}

export function AccessControlPanel({
  namespace,
  onNamespaceChange,
  onSelect,
  onSignIn,
}: AccessControlPanelProps) {
  const [filter, setFilter] = useState("");
  const [busyKey, setBusyKey] = useState("");
  const [actionError, setActionError] = useState("");
  const [actionNotice, setActionNotice] = useState("");
  const [confirm, setConfirm] = useState<ConfirmRequest | null>(null);
  const [grantDraft, setGrantDraft] = useState<GrantDraft | null>(null);
  const [sessionDraft, setSessionDraft] = useState<SessionDraft | null>(null);
  const reload = useAdminReload();

  const grantsQuery = useGrants(true, namespace);
  const sessionsQuery = useSessions(true, namespace);
  // The same authorized catalog the Servers screen reads, so the create forms
  // can offer real server and namespace choices instead of free text.
  const catalog = useCatalog(true, namespace);

  const grants = grantsQuery.data ?? [];
  const sessions = sessionsQuery.data ?? [];

  const term = filter.trim().toLowerCase();
  const matches = (parts: Array<string | undefined>) =>
    !term || parts.filter(Boolean).join(" ").toLowerCase().includes(term);

  const visibleGrants = grants.filter((grant) =>
    matches([grant.name, grant.namespace, grant.serverRef?.name, subjectLabel(grant.subject)])
  );
  const visibleSessions = sessions.filter((session) =>
    matches([session.name, session.namespace, session.serverRef?.name, subjectLabel(session.subject)])
  );

  const activeGrants = grants.filter((grant) => grantStatus(grant).label === "Active").length;
  const activeSessions = sessions.filter((session) => sessionState(session).label === "Active").length;
  const namespaceOptions = useMemo(() => {
    const names = new Set(catalog.namespaces.map((entry) => entry.namespace));
    names.add(defaultNamespace(namespace));
    return [...names].filter(Boolean).sort();
  }, [catalog.namespaces, namespace]);

  async function runAction(key: string, label: string, action: () => Promise<void>) {
    setBusyKey(key);
    setActionError("");
    setActionNotice("");
    try {
      await action();
      setActionNotice(label);
      reload();
    } catch (error) {
      setActionError(error instanceof Error ? error.message : "The change could not be applied.");
    } finally {
      setBusyKey("");
      setConfirm(null);
    }
  }

  function askToggleGrant(grant: GrantSummary) {
    const disable = !grant.disabled;
    setConfirm({
      title: `${disable ? "Disable" : "Enable"} grant "${grant.name}"?`,
      body: disable
        ? `The gateway will stop matching this grant, so ${subjectLabel(grant.subject)} loses access to ${grant.serverRef?.name || "its server"} unless another grant allows it.`
        : `The gateway will match this grant again, restoring ${subjectLabel(grant.subject)}'s access up to ${grant.maxTrust || "its"} trust.`,
      confirmLabel: disable ? "Disable grant" : "Enable grant",
      destructive: disable,
      onConfirm: () =>
        runAction(
          `grant:${accessKey(grant)}`,
          `Grant "${grant.name}" ${disable ? "disabled" : "enabled"}.`,
          () => setGrantDisabled(grant.namespace, grant.name, disable)
        ),
    });
  }

  function askToggleSession(session: SessionSummary) {
    const revoke = !session.revoked;
    setConfirm({
      title: `${revoke ? "Revoke" : "Restore"} session "${session.name}"?`,
      body: revoke
        ? `In-flight and future tool calls made with this session are denied immediately.`
        : `The session becomes usable again until its expiry (${expiryText(session)}).`,
      confirmLabel: revoke ? "Revoke session" : "Restore session",
      destructive: revoke,
      onConfirm: () =>
        runAction(
          `session:${accessKey(session)}`,
          `Session "${session.name}" ${revoke ? "revoked" : "restored"}.`,
          () => setSessionRevoked(session.namespace, session.name, revoke)
        ),
    });
  }

  function askRevokeGrantSessions(grant: GrantSummary) {
    setConfirm({
      title: `Revoke sessions created from grant "${grant.name}"?`,
      body: "Every active session linked to this grant will be revoked. The grant itself remains enabled.",
      confirmLabel: "Revoke sessions",
      destructive: true,
      onConfirm: () => runAction(
        `grant-sessions:${accessKey(grant)}`,
        `Sessions from grant "${grant.name}" revoked.`,
        async () => { await revokeGrantSessions(grant.namespace, grant.name); }
      ),
    });
  }

  const grantColumns = useMemo(
    () =>
      buildColumns<GrantSummary>([
        {
          id: "name",
          header: "Grant",
          rowHeader: true,
          sortValue: (grant) => grant.name,
          cell: (grant) => (
            <>
              <button
                type="button"
                className="link-button"
                data-testid="grant-drilldown"
                onClick={() => onSelect({ kind: "grant", item: grant })}
              >
                {grant.name}
              </button>
              <span className="cell-detail">{grant.namespace}</span>
            </>
          ),
        },
        {
          id: "server",
          header: "Server",
          sortValue: (grant) => grant.serverRef?.name || "",
          cell: (grant) => grant.serverRef?.name || "—",
        },
        { id: "subject", header: "Subject", cell: (grant) => subjectLabel(grant.subject) },
        {
          id: "trust",
          header: "Trust ceiling",
          sortValue: (grant) => grant.maxTrust || "",
          cell: (grant) => grant.maxTrust || "—",
        },
        {
          id: "effects",
          header: "Side effects",
          cell: (grant) => (grant.allowedSideEffects || []).join(", ") || "—",
        },
        {
          id: "expires",
          header: "Expires",
          sortValue: (grant) => grant.expiresAt || "",
          cell: (grant) => (grant.expiresAt ? formatTimestamp(grant.expiresAt) : "No expiry"),
        },
        {
          id: "status",
          header: "Status",
          sortValue: (grant) => grantStatus(grant).label,
          cell: (grant) => (
            <div className="cell-actions">
              <StatusBadge tone={grantStatus(grant).tone}>
                {grantStatus(grant).label}
              </StatusBadge>
              <Button
                variant="ghost"
                size="sm"
                data-testid="grant-toggle"
                busy={busyKey === `grant:${accessKey(grant)}`}
                onClick={() => askToggleGrant(grant)}
              >
                {grant.disabled ? "Enable" : "Disable"}
              </Button>
              <Button
                variant="ghost"
                size="sm"
                data-testid="grant-revoke-sessions"
                busy={busyKey === `grant-sessions:${accessKey(grant)}`}
                onClick={() => askRevokeGrantSessions(grant)}
              >
                Revoke sessions
              </Button>
            </div>
          ),
        },
      ]),
    // askToggleGrant and onSelect close over current state.
    [busyKey, onSelect]
  );

  const sessionColumns = useMemo(
    () =>
      buildColumns<SessionSummary>([
        {
          id: "name",
          header: "Session",
          rowHeader: true,
          sortValue: (session) => session.name,
          cell: (session) => (
            <>
              <button
                type="button"
                className="link-button"
                data-testid="session-drilldown"
                onClick={() => onSelect({ kind: "session", item: session })}
              >
                {session.name}
              </button>
              <span className="cell-detail">{session.namespace}</span>
            </>
          ),
        },
        {
          id: "server",
          header: "Server",
          sortValue: (session) => session.serverRef?.name || "",
          cell: (session) => session.serverRef?.name || "—",
        },
        { id: "subject", header: "Subject", cell: (session) => subjectLabel(session.subject) },
        {
          id: "trust",
          header: "Consented trust",
          sortValue: (session) => session.consentedTrust || "",
          cell: (session) => session.consentedTrust || "—",
        },
        {
          id: "expires",
          header: "Expires",
          sortValue: (session) => Date.parse(session.expiresAt || "") || 0,
          cell: (session) => (
            <span title={formatAbsolute(session.expiresAt)}>{expiryText(session)}</span>
          ),
        },
        {
          id: "status",
          header: "Status",
          cell: (session) => {
            const state = sessionState(session);
            return (
              <div className="cell-actions">
                <StatusBadge tone={state.tone}>{state.label}</StatusBadge>
                <Button
                  variant="ghost"
                  size="sm"
                  data-testid="session-toggle"
                  busy={busyKey === `session:${accessKey(session)}`}
                  onClick={() => askToggleSession(session)}
                >
                  {session.revoked ? "Restore" : "Revoke"}
                </Button>
              </div>
            );
          },
        },
      ]),
    [busyKey, onSelect]
  );

  const chips: FilterChip[] = [];
  if (filter.trim()) {
    chips.push({ id: "filter", label: "Search", value: filter.trim(), onRemove: () => setFilter("") });
  }
  if (namespace.trim()) {
    chips.push({
      id: "namespace",
      label: "Namespace",
      value: namespace.trim(),
      onRemove: () => onNamespaceChange(""),
    });
  }

  return (
    <>
      <PageHeader
        title="Access control"
        description="Grants and agent sessions enforced by the MCP gateway. Select one to trace the decisions it produced."
        actions={
          <>
            <Button variant="secondary" icon="refresh" onClick={reload} data-testid="access-refresh">
              Refresh
            </Button>
            <Button
              variant="primary"
              icon="plus"
              data-testid="grant-create-toggle"
              onClick={() => {
                setSessionDraft(null);
                setGrantDraft((current) => (current ? null : emptyGrantDraft(namespace)));
              }}
            >
              New grant
            </Button>
            <Button
              variant="secondary"
              icon="plus"
              data-testid="session-create-toggle"
              onClick={() => {
                setGrantDraft(null);
                setSessionDraft((current) => (current ? null : emptySessionDraft(namespace)));
              }}
            >
              New session
            </Button>
          </>
        }
      />

      <MetricGrid
        label="Access control summary"
        testId="access-stats"
        metrics={[
          { label: "Active grants", value: activeGrants, icon: "shield" },
          {
            label: "Disabled grants",
            value: grants.length - activeGrants,
            icon: "close",
            tone: grants.length - activeGrants > 0 ? "warning" : "default",
          },
          { label: "Active sessions", value: activeSessions, icon: "clock" },
          {
            label: "Revoked or expired",
            value: sessions.length - activeSessions,
            icon: "alert",
            tone: sessions.length - activeSessions > 0 ? "warning" : "default",
          },
        ]}
      />

      <FilterBar label="Filter access records">
        <TextField
          label="Search"
          type="search"
          fieldClassName="grow"
          leadingIcon
          placeholder="Name, server, or subject"
          value={filter}
          data-testid="access-filter"
          onChange={(event) => setFilter(event.target.value)}
        />
        <SelectField
          label="Namespace"
          value={namespace}
          data-testid="access-namespace"
          hint="Scopes the reads below."
          options={[
            { value: "", label: "All namespaces" },
            ...namespaceOptions.map((ns) => ({ value: ns, label: ns })),
          ]}
          onChange={(event) => onNamespaceChange(event.target.value)}
        />
      </FilterBar>

      <FilterSummary
        chips={chips}
        count={`${visibleGrants.length} grants · ${visibleSessions.length} sessions`}
        onClear={() => {
          setFilter("");
          onNamespaceChange("");
        }}
        testId="access-summary"
      />

      {actionNotice ? (
        <p className="notice notice-success" role="status" data-testid="access-action-notice">
          <Icon name="check" />
          <span className="notice-body">{actionNotice}</span>
        </p>
      ) : null}
      {actionError ? (
        <p className="notice notice-danger" role="alert" data-testid="access-action-error">
          <Icon name="alert" />
          <span className="notice-body">{actionError}</span>
        </p>
      ) : null}

      {grantDraft ? (
        <GrantForm
          draft={grantDraft}
          servers={catalog.servers}
          namespaces={namespaceOptions}
          busy={busyKey === "create-grant"}
          submitError={actionError}
          onChange={setGrantDraft}
          onCancel={() => setGrantDraft(null)}
          onSubmit={() =>
            void runAction("create-grant", `Grant "${grantDraft.name}" created.`, async () => {
              await createGrant({
                name: grantDraft.name.trim(),
                namespace: grantDraft.namespace.trim(),
                serverRef: { name: grantDraft.server.trim() },
                subject: {
                  humanID: grantDraft.humanID.trim() || undefined,
                  agentID: grantDraft.agentID.trim() || undefined,
                  teamID: grantDraft.teamID.trim() || undefined,
                },
                maxTrust: grantDraft.maxTrust,
                allowedSideEffects: grantDraft.allowedSideEffects,
                expiresAt: grantDraft.expiresAt ? new Date(grantDraft.expiresAt).toISOString() : undefined,
              });
              setGrantDraft(null);
            })
          }
        />
      ) : null}

      {sessionDraft ? (
        <SessionForm
          draft={sessionDraft}
          servers={catalog.servers}
          namespaces={namespaceOptions}
          busy={busyKey === "create-session"}
          submitError={actionError}
          onChange={setSessionDraft}
          onCancel={() => setSessionDraft(null)}
          onSubmit={() =>
            void runAction("create-session", `Session "${sessionDraft.name}" created.`, async () => {
              await createSession({
                name: sessionDraft.name.trim(),
                namespace: sessionDraft.namespace.trim(),
                serverRef: { name: sessionDraft.server.trim() },
                subject: {
                  humanID: sessionDraft.humanID.trim() || undefined,
                  agentID: sessionDraft.agentID.trim() || undefined,
                  teamID: sessionDraft.teamID.trim() || undefined,
                },
                consentedTrust: sessionDraft.consentedTrust,
                expiresAt: sessionDraft.expiresAt
                  ? new Date(sessionDraft.expiresAt).toISOString()
                  : undefined,
              });
              setSessionDraft(null);
            })
          }
        />
      ) : null}

      <section className="section">
        <div className="section-head">
          <h2 className="section-title" id="grants-title">
            Access grants
          </h2>
          <p className="section-note">What a subject is allowed to do on a server.</p>
        </div>
        <AsyncSection
          query={grantsQuery}
          loadingLabel="Loading access grants…"
          errorTitle="Access grants could not be loaded."
          onRetry={reload}
          onSignIn={onSignIn}
          testId="grants"
        >
          <DataTable
            columns={grantColumns}
            rows={visibleGrants}
            rowKey={accessKey}
            caption="Access grants with server, subject, trust ceiling, allowed side effects, and status."
            regionLabel="Access grants"
            testId="grants-table"
            emptyMessage={
              grants.length === 0 ? "No access grants found." : "No grants match this filter."
            }
          />
        </AsyncSection>
      </section>

      <section className="section">
        <div className="section-head">
          <h2 className="section-title" id="sessions-title">
            Agent sessions
          </h2>
          <p className="section-note">What an agent consented to for a bounded period.</p>
        </div>
        <AsyncSection
          query={sessionsQuery}
          loadingLabel="Loading agent sessions…"
          errorTitle="Agent sessions could not be loaded."
          onRetry={reload}
          onSignIn={onSignIn}
          testId="sessions"
        >
          <DataTable
            columns={sessionColumns}
            rows={visibleSessions}
            rowKey={accessKey}
            caption="Agent sessions with server, subject, consented trust, expiry, and status."
            regionLabel="Agent sessions"
            testId="sessions-table"
            emptyMessage={
              sessions.length === 0 ? "No agent sessions found." : "No sessions match this filter."
            }
          />
        </AsyncSection>
      </section>

      {confirm ? (
        <ConfirmDialog
          {...confirm}
          busy={busyKey !== ""}
          onCancel={() => setConfirm(null)}
          testId="access-confirm"
          confirmTestId="access-confirm-yes"
          cancelTestId="access-confirm-cancel"
        />
      ) : null}
    </>
  );
}
