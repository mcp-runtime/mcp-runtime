import { StatusBadge } from "../../ui/Badge";
import { CopyButton } from "../../ui/CopyButton";
import { EmptyState } from "../../ui/States";
import type { TeamMembership } from "../../api/types";

type TeamMembershipPanelProps = {
  teams: TeamMembership[];
};

function roleTone(role: string): "info" | "neutral" {
  return role.toLowerCase() === "owner" ? "info" : "neutral";
}

export function TeamMembershipPanel({ teams }: TeamMembershipPanelProps) {
  return (
    <section className="section">
      <div className="section-head">
        <h2 className="section-title" id="team-membership-title">
          Team membership
        </h2>
        <p className="section-note">Each team owns one namespace.</p>
      </div>

      {teams.length === 0 ? (
        <EmptyState
          icon="users"
          title="You are not a member of any team."
          detail="Team namespaces appear here once an administrator adds you."
          testId="teams-empty"
        />
      ) : (
        <ul className="server-grid" data-testid="team-list">
          {teams.map((team) => {
            const namespace = team.namespace || team.team_namespace || "";
            const slug = team.slug || team.team_slug || "";
            return (
              <li key={team.id || team.team_id || slug || namespace} data-testid="team-item">
                <article className="server-card">
                  <div className="server-card-head">
                    <div>
                      <h3 className="server-card-name">{team.name || team.team_name || slug || "Team"}</h3>
                      <p className="server-card-namespace">{namespace || "No namespace"}</p>
                    </div>
                    {team.role ? (
                      <StatusBadge tone={roleTone(team.role)} label={`Your role: ${team.role}`}>
                        {team.role}
                      </StatusBadge>
                    ) : null}
                  </div>
                  {slug ? (
                    <span className="copy-row">
                      <span className="copy-value" title={slug}>
                        {slug}
                      </span>
                      <CopyButton value={slug} label={`Copy the slug for ${team.name || slug}`} />
                      <a className="link-button" href={`#/agents?team=${encodeURIComponent(slug)}`}>View agents</a>
                    </span>
                  ) : null}
                </article>
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}
