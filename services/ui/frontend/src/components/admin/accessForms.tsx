import { useState, type FormEvent } from "react";

import { Button } from "../../ui/Button";
import { StatusBadge } from "../../ui/Badge";
import { SelectField, TextField } from "../../ui/Field";
import { EmptyState, ErrorState, LoadingState } from "../../ui/States";
import type { ServerSummary } from "../../api/types";
import { useTeamAgents, useTeamMembers, useTeams } from "../../hooks/useAdminData";

export type GrantDraft = {
  name: string;
  namespace: string;
  server: string;
  humanID: string;
  agentID: string;
  teamID: string;
  maxTrust: string;
  allowedSideEffects: string[];
  expiresAt: string;
};

export type SessionDraft = {
  name: string;
  namespace: string;
  server: string;
  humanID: string;
  agentID: string;
  teamID: string;
  consentedTrust: string;
  expiresAt: string;
};

// The three values api/v1alpha1 accepts for a trust level and a side effect.
const TRUST_OPTIONS = [
  { value: "low", label: "low" },
  { value: "medium", label: "medium" },
  { value: "high", label: "high" },
];
export const SIDE_EFFECTS = ["read", "write", "destructive"];

type SubjectMode = "team" | "human" | "agent" | "human_agent";
type SubjectValues = { humanID: string; agentID: string; teamID: string };

function subjectModeOf(subject: SubjectValues): SubjectMode {
  if (subject.humanID && subject.agentID) return "human_agent";
  if (subject.agentID) return "agent";
  if (subject.humanID) return "human";
  if (subject.teamID) return "team";
  return "human";
}

function SubjectFields({
  testPrefix,
  subject,
  onChange,
}: {
  testPrefix: "grant" | "session";
  subject: SubjectValues;
  onChange: (subject: SubjectValues) => void;
}) {
  const [mode, setMode] = useState<SubjectMode>(() => subjectModeOf(subject));
  const teamsQuery = useTeams(true);
  const teams = teamsQuery.data ?? [];
  const [teamSlug, setTeamSlug] = useState(() => teams.find((team) => team.id === subject.teamID)?.slug ?? "");
  const [customTeam, setCustomTeam] = useState(false);
  const [customHuman, setCustomHuman] = useState(false);
  const membersQuery = useTeamMembers(true, teamSlug);
  const members = membersQuery.data ?? [];
  const agentsQuery = useTeamAgents(true, teamSlug);
  const agents = agentsQuery.data?.agents ?? [];
  const activeAgents = agents.filter((agent) => agent.status === "active");
  const needsHuman = mode === "human" || mode === "human_agent";
  const needsAgent = mode === "agent" || mode === "human_agent";

  function selectMode(nextMode: SubjectMode) {
    setMode(nextMode);
    setTeamSlug("");
    setCustomTeam(false);
    setCustomHuman(false);
    onChange({ humanID: "", agentID: "", teamID: "" });
  }

  function selectTeam(slug: string) {
    const team = teams.find((candidate) => candidate.slug === slug);
    setTeamSlug(slug);
    setCustomHuman(false);
    // Changing the team always drops IDs selected under the previous team.
    onChange({ humanID: "", agentID: "", teamID: team?.id ?? "" });
  }

  return (
    <>
      <SelectField
        label="Subject type"
        value={mode}
        data-testid={`${testPrefix}-subject-mode`}
        options={[
          { value: "team", label: "Team only" },
          { value: "human", label: "Human" },
          { value: "agent", label: "Agent" },
          { value: "human_agent", label: "Human and agent" },
        ]}
        hint="Agent choices come from the selected team's active agent directory."
        onChange={(event) => selectMode(event.target.value as SubjectMode)}
      />

      {mode === "team" || needsHuman || needsAgent ? (
        <>
          {customTeam ? (
            <>
              <TextField
                label="Team ID"
                value={subject.teamID}
                data-testid={`${testPrefix}-team-custom`}
                onChange={(event) => onChange({ ...subject, teamID: event.target.value })}
              />
              <StatusBadge tone="warning" dot={false} testId={`${testPrefix}-team-not-in-directory`}>
                Not in directory
              </StatusBadge>
              <button type="button" className="link-button" onClick={() => { setCustomTeam(false); onChange({ ...subject, teamID: "", agentID: "" }); }}>
                Choose a listed team
              </button>
            </>
          ) : teamsQuery.error ? (
            <div className="field">
              <span className="field-label">Team</span>
              <ErrorState
                title="Teams could not be loaded."
                detail="You can retry or enter a custom team ID."
                onRetry={() => void teamsQuery.refetch()}
                testId={`${testPrefix}-teams-error`}
              />
              <button type="button" className="link-button" onClick={() => setCustomTeam(true)}>
                Enter a custom team ID
              </button>
            </div>
          ) : teamsQuery.isPending ? (
            <div className="field" data-testid={`${testPrefix}-teams-loading-wrap`}>
              <span className="field-label">Team</span>
              <LoadingState label="Loading teams…" variant="inline" testId={`${testPrefix}-teams-loading`} />
            </div>
          ) : teams.length === 0 ? (
            <div className="field">
              <span className="field-label">Team</span>
              <EmptyState title="No teams are available." detail="Enter a custom team ID if you already have one." testId={`${testPrefix}-teams-empty`} />
              <button type="button" className="link-button" onClick={() => setCustomTeam(true)}>
                Enter a custom team ID
              </button>
            </div>
          ) : (
            <>
              <SelectField
                label={mode === "team" ? "Team" : "Subject team"}
                value={teamSlug}
                data-testid={`${testPrefix}-team-select`}
                options={[
                  { value: "", label: "Select a team" },
                  ...teams.map((team) => ({ value: team.slug, label: `${team.name || team.slug} (${team.slug})` })),
                ]}
                onChange={(event) => selectTeam(event.target.value)}
              />
              <button type="button" className="link-button" onClick={() => { setCustomTeam(true); setTeamSlug(""); onChange({ humanID: "", agentID: "", teamID: "" }); }}>
                Enter a custom team ID
              </button>
            </>
          )}
        </>
      ) : null}

      {needsHuman ? (
        customHuman ? (
          <>
            <TextField
              label="Human ID"
              value={subject.humanID}
              data-testid={`${testPrefix}-human-custom`}
              onChange={(event) => onChange({ ...subject, humanID: event.target.value })}
            />
            <StatusBadge tone="warning" dot={false} testId={`${testPrefix}-human-not-in-directory`}>
              Not in directory
            </StatusBadge>
            <button type="button" className="link-button" onClick={() => { setCustomHuman(false); onChange({ ...subject, humanID: "" }); }}>
              Choose a listed member
            </button>
          </>
        ) : !teamSlug ? (
          <div className="field">
            <span className="field-label">Member</span>
            <EmptyState title="Select a team to load its members." testId={`${testPrefix}-members-unselected`} />
            <button type="button" className="link-button" onClick={() => setCustomHuman(true)}>
              Enter a custom human ID
            </button>
          </div>
        ) : membersQuery.error ? (
          <div className="field">
            <span className="field-label">Member</span>
            <ErrorState
              title="Team members could not be loaded."
              detail="You can retry or enter a custom human ID."
              onRetry={() => void membersQuery.refetch()}
              testId={`${testPrefix}-members-error`}
            />
            <button type="button" className="link-button" onClick={() => setCustomHuman(true)}>
              Enter a custom human ID
            </button>
          </div>
        ) : membersQuery.isPending ? (
          <div className="field">
            <span className="field-label">Member</span>
            <LoadingState label="Loading team members…" variant="inline" testId={`${testPrefix}-members-loading`} />
          </div>
        ) : members.length === 0 ? (
          <div className="field">
            <span className="field-label">Member</span>
            <EmptyState title="This team has no members." detail="Enter a custom human ID if needed." testId={`${testPrefix}-members-empty`} />
            <button type="button" className="link-button" onClick={() => setCustomHuman(true)}>
              Enter a custom human ID
            </button>
          </div>
        ) : (
          <>
            <SelectField
              label="Member"
              value={subject.humanID}
              data-testid={`${testPrefix}-human-select`}
              options={[
                { value: "", label: "Select a member" },
                ...members.map((member) => ({
                  value: member.user_id,
                  label: `${member.email || member.user_id} (${member.user_id})`,
                })),
              ]}
              hint="The selected member’s stable user ID is submitted."
              onChange={(event) => onChange({ ...subject, humanID: event.target.value })}
            />
            <button type="button" className="link-button" onClick={() => setCustomHuman(true)}>
              Enter a custom human ID
            </button>
          </>
        )
      ) : null}

      {needsAgent ? (
        !teamSlug ? (
          <div className="field">
            <span className="field-label">Agent</span>
            <EmptyState title="Select a listed team to load active agents." testId={`${testPrefix}-agents-unselected`} />
          </div>
        ) : agentsQuery.error ? (
          <div className="field">
            <span className="field-label">Agent</span>
            <ErrorState
              title="Team agents could not be loaded."
              detail="Retry the directory request before selecting an agent."
              onRetry={() => void agentsQuery.refetch()}
              testId={`${testPrefix}-agents-error`}
            />
          </div>
        ) : agentsQuery.isPending ? (
          <div className="field">
            <span className="field-label">Agent</span>
            <LoadingState label="Loading active agents…" variant="inline" testId={`${testPrefix}-agents-loading`} />
          </div>
        ) : activeAgents.length === 0 ? (
          <div className="field">
            <span className="field-label">Agent</span>
            <EmptyState title="This team has no active agents." detail="Create or reactivate an agent in the directory first." testId={`${testPrefix}-agents-empty`} />
          </div>
        ) : (
          <>
            <SelectField
              label="Agent"
              value={subject.agentID}
              data-testid={`${testPrefix}-agent-select`}
              options={[
                { value: "", label: "Select an active agent" },
                ...agents.map((agent) => ({
                  value: agent.id,
                  label: agent.status === "active"
                    ? `${agent.name} (${agent.id})`
                    : `${agent.name} (${agent.id}) — inactive, unavailable`,
                  disabled: agent.status !== "active",
                })),
              ]}
              hint="Only active agents in the selected team are available."
              onChange={(event) => onChange({ ...subject, agentID: event.target.value })}
            />
          </>
        )
      ) : null}

    </>
  );
}

// Kubernetes object names: RFC 1123 subdomain, which is what the API server
// rejects if we get it wrong. Validating here keeps the error next to the field.
const NAME_PATTERN = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;

export function validateName(value: string, what: string): string {
  const name = value.trim();
  if (!name) {
    return `Enter a ${what} name.`;
  }
  if (name.length > 63) {
    return "Use 63 characters or fewer.";
  }
  if (!NAME_PATTERN.test(name)) {
    return "Use lowercase letters, digits, and hyphens, starting and ending with a letter or digit.";
  }
  return "";
}

export function validateSubject(draft: { humanID: string; agentID: string; teamID: string }): string {
  if (!draft.humanID.trim() && !draft.agentID.trim() && !draft.teamID.trim()) {
    return "Choose a human, agent, or team subject.";
  }
  return "";
}

export function subjectIsCrossTeam(servers: ServerSummary[], draft: { namespace: string; server: string; teamID: string }): boolean {
  const server = servers.find((candidate) => candidate.namespace === draft.namespace && candidate.name === draft.server);
  return Boolean(server?.team_id && draft.teamID && server.team_id !== draft.teamID);
}

export function defaultCrossTeamExpiry(): string {
  const date = new Date(Date.now() + 24 * 60 * 60 * 1000);
  return new Date(date.getTime() - date.getTimezoneOffset() * 60_000).toISOString().slice(0, 16);
}

export function withCrossTeamExpiry<T extends { namespace: string; server: string; teamID: string; expiresAt: string }>(
  previous: T,
  next: T,
  servers: ServerSummary[]
): T {
  if (!subjectIsCrossTeam(servers, previous) && subjectIsCrossTeam(servers, next) && !next.expiresAt) {
    return { ...next, expiresAt: defaultCrossTeamExpiry() };
  }
  return next;
}

type ServerChoiceProps = {
  servers: ServerSummary[];
  value: string;
  namespace: string;
  error?: string;
  onChange: (value: string) => void;
};

// Offers the servers the signed-in admin can already read, and still accepts a
// typed name for a server outside the current catalog read.
function ServerChoice({ servers, value, namespace, error, onChange }: ServerChoiceProps) {
  const inScope = servers.filter((server) => !namespace || server.namespace === namespace);
  const known = inScope.some((server) => server.name === value);

  if (inScope.length === 0) {
    return (
      <TextField
        label="Server"
        value={value}
        required
        error={error}
        announceError
        hint="No servers were returned for this namespace; enter the MCPServer name."
        onChange={(event) => onChange(event.target.value)}
      />
    );
  }

  return (
    <SelectField
      label="Server"
      value={known ? value : ""}
      required
      error={error}
      announceError
      hint="MCPServer objects visible in this namespace."
      options={[
        { value: "", label: "Select a server" },
        ...inScope.map((server) => ({ value: server.name, label: server.name })),
      ]}
      onChange={(event) => onChange(event.target.value)}
    />
  );
}

type GrantFormProps = {
  draft: GrantDraft;
  servers: ServerSummary[];
  namespaces: string[];
  busy: boolean;
  submitError: string;
  onChange: (draft: GrantDraft) => void;
  onCancel: () => void;
  onSubmit: () => void;
};

export function GrantForm({
  draft,
  servers,
  namespaces,
  busy,
  submitError,
  onChange,
  onCancel,
  onSubmit,
}: GrantFormProps) {
  const [errors, setErrors] = useState<Record<string, string>>({});
  const crossTeam = subjectIsCrossTeam(servers, draft);
  const updateDraft = (next: GrantDraft) => onChange(withCrossTeamExpiry(draft, next, servers));

  function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busy) {
      return;
    }
    const next: Record<string, string> = {};
    const nameError = validateName(draft.name, "grant");
    if (nameError) next.name = nameError;
    if (!draft.server.trim()) next.server = "Choose the server this grant applies to.";
    const subjectError = validateSubject(draft);
    if (subjectError) next.subject = subjectError;
    if (draft.allowedSideEffects.length === 0) next.effects = "Allow at least one side effect.";
    if (crossTeam && !draft.expiresAt) next.expiresAt = "Cross-team access must expire.";
    if (draft.expiresAt && (!Number.isFinite(Date.parse(draft.expiresAt)) || Date.parse(draft.expiresAt) <= Date.now())) {
      next.expiresAt = "Choose a future expiry time.";
    }
    setErrors(next);
    if (Object.keys(next).length > 0) {
      return;
    }
    onSubmit();
  }

  return (
    <form className="form-panel" onSubmit={handleSubmit} data-testid="grant-create-form" noValidate>
      <h3 className="form-panel-title">Create an access grant</h3>

      <fieldset className="form-fieldset">
        <legend>Identity</legend>
        <div className="form-grid">
          <TextField
            label="Grant name"
            value={draft.name}
            required
            error={errors.name}
            announceError
            data-testid="grant-name"
            onChange={(event) => updateDraft({ ...draft, name: event.target.value })}
          />
          <SelectField
            label="Namespace"
            value={draft.namespace}
            hint="The grant is created in this namespace."
            options={
              namespaces.includes(draft.namespace)
                ? namespaces.map((ns) => ({ value: ns, label: ns }))
                : [{ value: draft.namespace, label: draft.namespace }, ...namespaces.map((ns) => ({ value: ns, label: ns }))]
            }
            data-testid="grant-namespace"
            onChange={(event) => updateDraft({ ...draft, namespace: event.target.value })}
          />
          <ServerChoice
            servers={servers}
            value={draft.server}
            namespace={draft.namespace}
            error={errors.server}
            onChange={(value) => updateDraft({ ...draft, server: value })}
          />
        </div>
      </fieldset>

      {crossTeam ? (
        <p className="notice notice-warning" role="status" data-testid="grant-cross-team-banner">
          <span className="notice-body">Cross-team access: this subject belongs to a different team than the server. An expiry is required; the default is 24 hours.</span>
        </p>
      ) : null}

      <fieldset className="form-fieldset">
        <legend>Subject</legend>
        <div className="form-grid">
          <SubjectFields
            testPrefix="grant"
            subject={draft}
            onChange={(subject) => updateDraft({ ...draft, ...subject })}
          />
        </div>
        {errors.subject ? (
          <p className="field-error" role="alert" data-testid="grant-subject-error">
            {errors.subject}
          </p>
        ) : null}
      </fieldset>

      <fieldset className="form-fieldset">
        <legend>Policy ceiling</legend>
        <div className="form-grid">
          <SelectField
            label="Maximum trust"
            value={draft.maxTrust}
            options={TRUST_OPTIONS}
            hint="A call is denied when the tool needs more trust than this."
            data-testid="grant-trust"
            onChange={(event) => updateDraft({ ...draft, maxTrust: event.target.value })}
          />
          <TextField
            label={crossTeam ? "Expires at (required for cross-team access)" : "Expires at (optional)"}
            type="datetime-local"
            value={draft.expiresAt}
            required={crossTeam}
            error={errors.expiresAt}
            announceError
            hint="After this time, the grant cannot authorize calls or session refreshes."
            data-testid="grant-expires-at"
            onChange={(event) => updateDraft({ ...draft, expiresAt: event.target.value })}
          />
          <div className="field">
            <span className="field-label" id="grant-effects-label">
              Allowed side effects
            </span>
            <div className="inline-actions" role="group" aria-labelledby="grant-effects-label">
              {SIDE_EFFECTS.map((effect) => {
                const checked = draft.allowedSideEffects.includes(effect);
                return (
                  <label className="chip" key={effect} style={{ paddingLeft: 8, cursor: "pointer" }}>
                    <input
                      type="checkbox"
                      checked={checked}
                      data-testid={`grant-effect-${effect}`}
                      onChange={() =>
                        updateDraft({
                          ...draft,
                          allowedSideEffects: checked
                            ? draft.allowedSideEffects.filter((value) => value !== effect)
                            : [...draft.allowedSideEffects, effect],
                        })
                      }
                    />
                    {effect}
                  </label>
                );
              })}
            </div>
            {errors.effects ? (
              <span className="field-error" role="alert">
                {errors.effects}
              </span>
            ) : (
              <span className="field-hint">Submitted as written here; nothing is added silently.</span>
            )}
          </div>
        </div>
      </fieldset>

      {submitError ? (
        <p className="notice notice-danger" role="alert" data-testid="grant-create-error">
          <span className="notice-body">{submitError}</span>
        </p>
      ) : null}

      <div className="form-footer">
        <span className="form-footer-note">
          Policy version and tool rules are left at their server-side defaults.
        </span>
        <Button variant="ghost" onClick={onCancel} disabled={busy}>
          Cancel
        </Button>
        <Button type="submit" variant="primary" busy={busy} data-testid="grant-create-submit">
          {busy ? "Creating…" : "Create grant"}
        </Button>
      </div>
    </form>
  );
}

type SessionFormProps = {
  draft: SessionDraft;
  servers: ServerSummary[];
  namespaces: string[];
  busy: boolean;
  submitError: string;
  onChange: (draft: SessionDraft) => void;
  onCancel: () => void;
  onSubmit: () => void;
};

export function SessionForm({
  draft,
  servers,
  namespaces,
  busy,
  submitError,
  onChange,
  onCancel,
  onSubmit,
}: SessionFormProps) {
  const [errors, setErrors] = useState<Record<string, string>>({});
  const crossTeam = subjectIsCrossTeam(servers, draft);
  const updateDraft = (next: SessionDraft) => onChange(withCrossTeamExpiry(draft, next, servers));

  function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busy) {
      return;
    }
    const next: Record<string, string> = {};
    const nameError = validateName(draft.name, "session");
    if (nameError) next.name = nameError;
    if (!draft.server.trim()) next.server = "Choose the server this session applies to.";
    const subjectError = validateSubject(draft);
    if (subjectError) next.subject = subjectError;
    if (crossTeam && !draft.expiresAt) next.expiresAt = "Cross-team access must expire.";
    if (draft.expiresAt && Number.isNaN(Date.parse(draft.expiresAt))) {
      next.expiresAt = "Enter a valid date and time.";
    }
    setErrors(next);
    if (Object.keys(next).length > 0) {
      return;
    }
    onSubmit();
  }

  return (
    <form className="form-panel" onSubmit={handleSubmit} data-testid="session-create-form" noValidate>
      <h3 className="form-panel-title">Create an agent session</h3>

      <fieldset className="form-fieldset">
        <legend>Identity</legend>
        <div className="form-grid">
          <TextField
            label="Session name"
            value={draft.name}
            required
            error={errors.name}
            announceError
            data-testid="session-name"
            onChange={(event) => updateDraft({ ...draft, name: event.target.value })}
          />
          <SelectField
            label="Namespace"
            value={draft.namespace}
            hint="The session is created in this namespace."
            options={
              namespaces.includes(draft.namespace)
                ? namespaces.map((ns) => ({ value: ns, label: ns }))
                : [{ value: draft.namespace, label: draft.namespace }, ...namespaces.map((ns) => ({ value: ns, label: ns }))]
            }
            data-testid="session-namespace"
            onChange={(event) => updateDraft({ ...draft, namespace: event.target.value })}
          />
          <ServerChoice
            servers={servers}
            value={draft.server}
            namespace={draft.namespace}
            error={errors.server}
            onChange={(value) => updateDraft({ ...draft, server: value })}
          />
        </div>
      </fieldset>

      <fieldset className="form-fieldset">
        <legend>Subject</legend>
        <div className="form-grid">
          <SubjectFields
            testPrefix="session"
            subject={draft}
            onChange={(subject) => updateDraft({ ...draft, ...subject })}
          />
        </div>
        {errors.subject ? (
          <p className="field-error" role="alert" data-testid="session-subject-error">
            {errors.subject}
          </p>
        ) : null}
      </fieldset>

      {crossTeam ? (
        <p className="notice notice-warning" role="status" data-testid="session-cross-team-banner">
          <span className="notice-body">Cross-team access: this subject belongs to a different team than the server. An expiry is required; the default is 24 hours.</span>
        </p>
      ) : null}

      <fieldset className="form-fieldset">
        <legend>Consent</legend>
        <div className="form-grid">
          <SelectField
            label="Consented trust"
            value={draft.consentedTrust}
            options={TRUST_OPTIONS}
            hint="The ceiling this session consented to, capped again by the grant."
            data-testid="session-trust"
            onChange={(event) => updateDraft({ ...draft, consentedTrust: event.target.value })}
          />
          <TextField
            label={crossTeam ? "Expires at (required for cross-team access)" : "Expires at"}
            type="datetime-local"
            value={draft.expiresAt}
            required={crossTeam}
            error={errors.expiresAt}
            announceError
            hint="Leave empty for no expiry."
            data-testid="session-expires"
            onChange={(event) => updateDraft({ ...draft, expiresAt: event.target.value })}
          />
        </div>
      </fieldset>

      {submitError ? (
        <p className="notice notice-danger" role="alert" data-testid="session-create-error">
          <span className="notice-body">{submitError}</span>
        </p>
      ) : null}

      <div className="form-footer">
        <span className="form-footer-note">Policy version is left at its server-side default.</span>
        <Button variant="ghost" onClick={onCancel} disabled={busy}>
          Cancel
        </Button>
        <Button type="submit" variant="primary" busy={busy} data-testid="session-create-submit">
          {busy ? "Creating…" : "Create session"}
        </Button>
      </div>
    </form>
  );
}
