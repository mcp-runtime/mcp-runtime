import { useEffect, useMemo, useState, type FormEvent } from "react";

import { AsyncSection } from "./AsyncSection";
import { StatusBadge } from "../../ui/Badge";
import { Button } from "../../ui/Button";
import { ConfirmDialog, type ConfirmRequest } from "../../ui/ConfirmDialog";
import { CopyButton } from "../../ui/CopyButton";
import { DataTable, buildColumns } from "../../ui/DataTable";
import { SelectField, TextField } from "../../ui/Field";
import { Icon } from "../../ui/Icon";
import { PageHeader } from "../../ui/PageHeader";
import { EmptyState, ErrorState, LoadingState } from "../../ui/States";
import { useAdminReload, useTeamMembers, useTeams } from "../../hooks/useAdminData";
import { createTeam, createTeamUser, removeTeamMember, setTeamMemberRole } from "../../api/admin";
import type { TeamMembership, TeamRecord } from "../../api/types";

type TeamsPanelProps = { onSignIn: () => void };

const SLUG_PATTERN = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;

export function TeamsPanel({ onSignIn }: TeamsPanelProps) {
  const reload = useAdminReload();
  const teamsQuery = useTeams(true);
  const teams = useMemo(() => teamsQuery.data ?? [], [teamsQuery.data]);

  const [selectedSlug, setSelectedSlug] = useState("");
  const [busyKey, setBusyKey] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [confirm, setConfirm] = useState<ConfirmRequest | null>(null);
  const [teamForm, setTeamForm] = useState<{ slug: string; name: string } | null>(null);
  const [userForm, setUserForm] = useState<{ email: string; password: string; role: string } | null>(null);
  const [formErrors, setFormErrors] = useState<Record<string, string>>({});

  // Keep a valid selection without ever pointing at a team that has gone away.
  useEffect(() => {
    if (teams.length === 0) {
      if (selectedSlug) setSelectedSlug("");
      return;
    }
    if (!selectedSlug || !teams.some((team) => team.slug === selectedSlug)) {
      setSelectedSlug(teams[0].slug);
    }
  }, [teams, selectedSlug]);

  const membersQuery = useTeamMembers(true, selectedSlug);
  const selectedTeam = teams.find((team) => team.slug === selectedSlug);
  const members = membersQuery.data ?? [];

  async function runAction(key: string, successMessage: string, action: () => Promise<void>) {
    setBusyKey(key);
    setError("");
    setNotice("");
    try {
      await action();
      setNotice(successMessage);
      reload();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "The change could not be applied.");
    } finally {
      setBusyKey("");
      setConfirm(null);
    }
  }

  function submitTeam(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!teamForm || busyKey) {
      return;
    }
    const next: Record<string, string> = {};
    if (!SLUG_PATTERN.test(teamForm.slug.trim())) {
      next.slug = "Use lowercase letters, digits, and hyphens.";
    }
    if (!teamForm.name.trim()) {
      next.name = "Enter a display name.";
    }
    setFormErrors(next);
    if (Object.keys(next).length > 0) {
      return;
    }
    const slug = teamForm.slug.trim();
    void runAction("create-team", `Team "${teamForm.name.trim()}" created.`, async () => {
      await createTeam(slug, teamForm.name.trim());
      setTeamForm(null);
      setSelectedSlug(slug);
    });
  }

  function submitUser(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!userForm || busyKey || !selectedSlug) {
      return;
    }
    const next: Record<string, string> = {};
    if (!userForm.email.trim()) {
      next.email = "Enter an email address.";
    }
    if (userForm.password.length < 8) {
      next.password = "Use at least 8 characters.";
    }
    setFormErrors(next);
    if (Object.keys(next).length > 0) {
      return;
    }
    void runAction("create-user", `Account created for ${userForm.email.trim()}.`, async () => {
      await createTeamUser(selectedSlug, userForm.email.trim(), userForm.password, userForm.role);
      setUserForm(null);
    });
  }

  const teamColumns = useMemo(
    () =>
      buildColumns<TeamRecord>([
        {
          id: "name",
          header: "Team",
          rowHeader: true,
          sortValue: (team) => team.name || team.slug,
          cell: (team) => (
            <>
              <button
                type="button"
                className="link-button"
                aria-pressed={team.slug === selectedSlug}
                data-testid="team-select"
                onClick={() => setSelectedSlug(team.slug)}
              >
                {team.name || team.slug}
              </button>
              <span className="cell-detail">{team.slug}</span>
            </>
          ),
        },
        {
          id: "namespace",
          header: "Namespace",
          sortValue: (team) => team.namespace || "",
          cell: (team) => team.namespace || "—",
        },
        {
          id: "id",
          header: "ID",
          cell: (team) => (
            <span className="copy-row">
              <span className="copy-value" title={team.id}>
                {team.id}
              </span>
              <CopyButton value={team.id} label={`Copy the identifier for ${team.name || team.slug}`} />
            </span>
          ),
        },
        {
          id: "actions",
          header: "Actions",
          cell: (team) => (
            <Button
              variant={team.slug === selectedSlug ? "primary" : "ghost"}
              size="sm"
              onClick={() => setSelectedSlug(team.slug)}
            >
              {team.slug === selectedSlug ? "Selected" : "Manage members"}
            </Button>
          ),
        },
      ]),
    [selectedSlug]
  );

  const memberColumns = useMemo(
    () =>
      buildColumns<TeamMembership>([
        {
          id: "email",
          header: "Member",
          rowHeader: true,
          sortValue: (member) => member.email || member.user_id,
          cell: (member) => member.email || member.user_id,
        },
        {
          id: "role",
          header: "Role",
          sortValue: (member) => member.role,
          cell: (member) => (
            <StatusBadge tone={member.role === "owner" ? "info" : "neutral"}>{member.role}</StatusBadge>
          ),
        },
        {
          id: "id",
          header: "User ID",
          cell: (member) => (
            <span className="copy-row">
              <span className="copy-value" title={member.user_id}>
                {member.user_id}
              </span>
              <CopyButton value={member.user_id} label={`Copy the user ID for ${member.email || member.user_id}`} />
            </span>
          ),
        },
        {
          id: "actions",
          header: "Actions",
          cell: (member) => {
            const who = member.email || member.user_id;
            const nextRole = member.role === "owner" ? "member" : "owner";
            return (
              <div className="cell-actions">
                <Button
                  variant="ghost"
                  size="sm"
                  disabled={busyKey !== ""}
                  data-testid="member-role"
                  onClick={() =>
                    setConfirm({
                      title: `Change ${who} to ${nextRole}?`,
                      body:
                        nextRole === "owner"
                          ? `${who} will be able to manage this team's membership.`
                          : `${who} will lose the ability to manage this team's membership.`,
                      confirmLabel: `Set ${nextRole}`,
                      onConfirm: () =>
                        runAction(`role:${member.user_id}`, `${who} is now ${nextRole}.`, () =>
                          setTeamMemberRole(selectedSlug, member.user_id, nextRole)
                        ),
                    })
                  }
                >
                  {member.role === "owner" ? "Set member" : "Make owner"}
                </Button>
                <Button
                  variant="danger-outline"
                  size="sm"
                  disabled={busyKey !== ""}
                  data-testid="member-remove"
                  onClick={() =>
                    setConfirm({
                      title: `Remove ${who} from ${selectedTeam?.name || selectedSlug}?`,
                      body: `${who} loses access to the team namespace ${selectedTeam?.namespace || ""}. The account itself is not deleted.`,
                      confirmLabel: "Remove member",
                      destructive: true,
                      onConfirm: () =>
                        runAction(`remove:${member.user_id}`, `${who} removed from the team.`, () =>
                          removeTeamMember(selectedSlug, member.user_id)
                        ),
                    })
                  }
                >
                  Remove
                </Button>
              </div>
            );
          },
        },
      ]),
    [busyKey, selectedSlug, selectedTeam]
  );

  return (
    <>
      <PageHeader
        title="Teams"
        breadcrumb={[{ label: "Administration" }, { label: "Teams" }]}
        description="Tenant teams, the namespace each one owns, and their membership."
        actions={
          <>
            <Button variant="secondary" icon="refresh" onClick={reload} data-testid="teams-refresh">
              Refresh
            </Button>
            <Button
              variant="primary"
              icon="plus"
              data-testid="team-create-toggle"
              onClick={() => {
                setFormErrors({});
                setTeamForm((current) => (current ? null : { slug: "", name: "" }));
              }}
            >
              New team
            </Button>
          </>
        }
      />

      {notice ? (
        <p className="notice notice-success" role="status" data-testid="teams-action-notice">
          <Icon name="check" />
          <span className="notice-body">{notice}</span>
        </p>
      ) : null}
      {error ? (
        <p className="notice notice-danger" role="alert" data-testid="teams-action-error">
          <Icon name="alert" />
          <span className="notice-body">{error}</span>
        </p>
      ) : null}

      {teamForm ? (
        <form className="form-panel" onSubmit={submitTeam} data-testid="team-create-form" noValidate>
          <h3 className="form-panel-title">Create a team</h3>
          <div className="form-grid">
            <TextField
              label="Slug"
              value={teamForm.slug}
              required
              error={formErrors.slug}
              announceError
              hint="Used for the team namespace and API paths."
              data-testid="team-slug"
              onChange={(event) => setTeamForm({ ...teamForm, slug: event.target.value })}
            />
            <TextField
              label="Display name"
              value={teamForm.name}
              required
              error={formErrors.name}
              announceError
              data-testid="team-name"
              onChange={(event) => setTeamForm({ ...teamForm, name: event.target.value })}
            />
          </div>
          <div className="form-footer">
            <Button variant="ghost" onClick={() => setTeamForm(null)} disabled={busyKey !== ""}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" busy={busyKey === "create-team"} data-testid="team-create-submit">
              {busyKey === "create-team" ? "Creating…" : "Create team"}
            </Button>
          </div>
        </form>
      ) : null}

      <section className="section">
        <div className="section-head">
          <h2 className="section-title" id="teams-title">
            Team directory
          </h2>
          <p className="section-note">Select a team to manage its members.</p>
        </div>
        <AsyncSection
          query={teamsQuery}
          loadingLabel="Loading teams…"
          errorTitle="Teams could not be loaded."
          onRetry={reload}
          onSignIn={onSignIn}
          testId="teams"
        >
          <DataTable
            columns={teamColumns}
            rows={teams}
            rowKey={(team) => team.id || team.slug}
            caption="Tenant teams with slug, namespace, and identifier."
            regionLabel="Team directory"
            testId="teams-table"
            selectedKey={selectedTeam ? selectedTeam.id || selectedTeam.slug : undefined}
            emptyMessage="No teams found."
          />
        </AsyncSection>
      </section>

      <section className="section">
        <div className="section-head">
          <h2 className="section-title" id="team-members-title">
            {selectedTeam ? `Members of ${selectedTeam.name || selectedTeam.slug}` : "Members"}
          </h2>
          {selectedTeam ? (
            <p className="section-note">Namespace {selectedTeam.namespace || "—"} · <a className="link-button" href={`#/agents?team=${encodeURIComponent(selectedTeam.slug)}`}>View agents</a></p>
          ) : null}
        </div>

        {selectedTeam ? (
          <div className="inline-actions" style={{ marginBottom: "var(--space-3)" }}>
            <SelectField
              label="Team"
              value={selectedSlug}
              data-testid="team-select-input"
              options={teams.map((team) => ({ value: team.slug, label: team.name || team.slug }))}
              onChange={(event) => setSelectedSlug(event.target.value)}
            />
            <Button
              variant="secondary"
              icon="plus"
              data-testid="team-user-toggle"
              onClick={() => {
                setFormErrors({});
                setUserForm((current) => (current ? null : { email: "", password: "", role: "member" }));
              }}
            >
              Create account in this team
            </Button>
          </div>
        ) : null}

        {userForm && selectedTeam ? (
          <form className="form-panel" onSubmit={submitUser} data-testid="team-user-form" noValidate>
            <h3 className="form-panel-title">Create an account in {selectedTeam.name || selectedTeam.slug}</h3>
            <p className="section-note" style={{ marginBottom: "var(--space-4)" }}>
              This creates a new platform account with the password you set. It does not invite an existing
              user; the API has no invite flow.
            </p>
            <div className="form-grid">
              <TextField
                label="Email"
                type="email"
                value={userForm.email}
                required
                error={formErrors.email}
                announceError
                autoComplete="off"
                data-testid="team-user-email"
                onChange={(event) => setUserForm({ ...userForm, email: event.target.value })}
              />
              <TextField
                label="Temporary password"
                type="password"
                value={userForm.password}
                required
                minLength={8}
                error={formErrors.password}
                announceError
                autoComplete="new-password"
                hint="Share it out of band; the member should change it after signing in."
                data-testid="team-user-password"
                onChange={(event) => setUserForm({ ...userForm, password: event.target.value })}
              />
              <SelectField
                label="Role"
                value={userForm.role}
                options={[
                  { value: "member", label: "Member" },
                  { value: "owner", label: "Owner" },
                ]}
                data-testid="team-user-role"
                onChange={(event) => setUserForm({ ...userForm, role: event.target.value })}
              />
            </div>
            <div className="form-footer">
              <Button variant="ghost" onClick={() => setUserForm(null)} disabled={busyKey !== ""}>
                Cancel
              </Button>
              <Button type="submit" variant="primary" busy={busyKey === "create-user"} data-testid="team-user-submit">
                {busyKey === "create-user" ? "Creating…" : "Create account"}
              </Button>
            </div>
          </form>
        ) : null}

        {!selectedTeam ? (
          <EmptyState icon="users" title="Select a team to see its members." testId="team-members-none" />
        ) : membersQuery.isPending ? (
          <LoadingState label={`Loading members of ${selectedTeam.name || selectedTeam.slug}…`} testId="team-members-loading" />
        ) : membersQuery.error ? (
          <ErrorState
            title="Team members could not be loaded."
            detail={membersQuery.error instanceof Error ? membersQuery.error.message : ""}
            onRetry={() => void membersQuery.refetch()}
            testId="team-members-error"
          />
        ) : (
          <DataTable
            columns={memberColumns}
            rows={members}
            rowKey={(member) => member.user_id}
            caption="Members of the selected team, with role and user identifier."
            regionLabel="Team members"
            testId="team-members-table"
            emptyMessage="No members in this team."
          />
        )}
      </section>

      {confirm ? (
        <ConfirmDialog
          {...confirm}
          busy={busyKey !== ""}
          onCancel={() => setConfirm(null)}
          testId="teams-confirm"
          confirmTestId="teams-confirm-yes"
          cancelTestId="teams-confirm-cancel"
        />
      ) : null}
    </>
  );
}
