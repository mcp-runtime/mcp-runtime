package platformstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	sentinelaccess "mcp-runtime/pkg/access"
	"mcp-runtime/pkg/platforminventory"
)

// ListNamespaces returns platform-managed namespace records for admin views.
func (s *Store) ListNamespaces(ctx context.Context) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT
  n.id,
  COALESCE(n.user_id::text, ''),
  COALESCE(u.email, ''),
  COALESCE(n.team_id::text, ''),
  COALESCE(t.slug, ''),
  COALESCE(t.display_name, ''),
  n.namespace,
  COALESCE(n.scope, 'user'),
  n.created_at
FROM namespaces n
LEFT JOIN users u ON u.id = n.user_id
LEFT JOIN teams t ON t.id = n.team_id
WHERE n.deleted_at IS NULL
ORDER BY n.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		item, err := scanNamespaceRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// GetNamespace returns one platform-managed namespace record by Kubernetes namespace.
func (s *Store) GetNamespace(ctx context.Context, namespace string) (map[string]any, bool, error) {
	namespace = strings.TrimSpace(namespace)
	var id, userID, email, teamID, teamSlug, teamName, scope string
	var createdAt time.Time
	err := s.db.QueryRowContext(ctx, `
SELECT
  n.id,
  COALESCE(n.user_id::text, ''),
  COALESCE(u.email, ''),
  COALESCE(n.team_id::text, ''),
  COALESCE(t.slug, ''),
  COALESCE(t.display_name, ''),
  COALESCE(n.scope, 'user'),
  n.created_at
FROM namespaces n
LEFT JOIN users u ON u.id = n.user_id
LEFT JOIN teams t ON t.id = n.team_id
WHERE n.deleted_at IS NULL AND n.namespace = $1
LIMIT 1`, namespace).Scan(&id, &userID, &email, &teamID, &teamSlug, &teamName, &scope, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return map[string]any{
		"id":         id,
		"user_id":    userID,
		"email":      email,
		"team_id":    teamID,
		"team_slug":  teamSlug,
		"team_name":  teamName,
		"scope":      scope,
		"namespace":  namespace,
		"created_at": createdAt,
		"is_shared":  namespace == SharedCatalogNamespace,
		"is_managed": strings.HasPrefix(namespace, TeamNamespacePrefix),
	}, true, nil
}

// CreateTeam creates a team, its namespace record, and an owner membership.
func (s *Store) CreateTeam(ctx context.Context, slug, name, createdByUserID string) (Team, error) {
	slug = NormalizeTeamSlug(slug)
	if err := ValidateTeamSlug(slug); err != nil {
		return Team{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = slug
	}
	namespace := TeamNamespacePrefix + slug
	if err := ValidateTeamNamespace(namespace); err != nil {
		return Team{}, err
	}
	teamID := uuid.NewString()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Team{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err = tx.ExecContext(ctx, `
INSERT INTO teams (id, slug, display_name, created_by)
VALUES ($1, $2, $3, NULLIF($4, '')::uuid)`, teamID, slug, name, strings.TrimSpace(createdByUserID)); err != nil {
		return Team{}, err
	}
	if _, err = tx.ExecContext(ctx, `
INSERT INTO namespaces (id, user_id, team_id, namespace, display_name, scope)
VALUES ($1, NULL, $2, $3, $4, $5)`, uuid.NewString(), teamID, namespace, name, NamespaceScopeTeam); err != nil {
		return Team{}, err
	}
	if strings.TrimSpace(createdByUserID) != "" {
		if _, err = tx.ExecContext(ctx, `
INSERT INTO team_memberships (id, team_id, user_id, role)
VALUES ($1, $2, $3, $4)
ON CONFLICT (team_id, user_id) WHERE deleted_at IS NULL
DO UPDATE SET role = EXCLUDED.role, deleted_at = NULL`, uuid.NewString(), teamID, createdByUserID, TeamRoleOwner); err != nil {
			return Team{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return Team{}, err
	}

	return Team{
		ID:        teamID,
		Slug:      slug,
		Name:      name,
		Namespace: namespace,
		CreatedAt: time.Now().UTC(),
	}, nil
}

// DeleteTeamBySlug soft-deletes a team and its derived namespace/membership records.
func (s *Store) DeleteTeamBySlug(ctx context.Context, slug string) error {
	slug = NormalizeTeamSlug(slug)
	if err := ValidateTeamSlug(slug); err != nil {
		return err
	}
	team, ok, err := s.GetTeamBySlug(ctx, slug)
	if err != nil {
		return err
	}
	if !ok {
		return sql.ErrNoRows
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err = tx.ExecContext(ctx, `UPDATE team_memberships SET deleted_at = now() WHERE team_id = $1 AND deleted_at IS NULL`, team.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE namespaces SET deleted_at = now() WHERE team_id = $1 AND deleted_at IS NULL`, team.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE teams SET deleted_at = now() WHERE id = $1 AND deleted_at IS NULL`, team.ID); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

// ListTeams returns all non-deleted platform teams.
func (s *Store) ListTeams(ctx context.Context) ([]Team, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT t.id, t.slug, t.display_name, COALESCE(n.namespace, ''), t.created_at
FROM teams t
LEFT JOIN namespaces n ON n.team_id = t.id AND n.deleted_at IS NULL AND COALESCE(n.scope, 'team') = 'team'
WHERE t.deleted_at IS NULL
ORDER BY t.slug ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Team, 0)
	for rows.Next() {
		var team Team
		if err := rows.Scan(&team.ID, &team.Slug, &team.Name, &team.Namespace, &team.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, team)
	}
	return out, rows.Err()
}

// ListUserTeams returns active team memberships for a user.
func (s *Store) ListUserTeams(ctx context.Context, userID string) ([]TeamMembership, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT t.id, t.slug, t.display_name, COALESCE(n.namespace, ''), tm.user_id::text, tm.role, tm.created_at
FROM team_memberships tm
JOIN teams t ON t.id = tm.team_id AND t.deleted_at IS NULL
LEFT JOIN namespaces n ON n.team_id = t.id AND n.deleted_at IS NULL AND COALESCE(n.scope, 'team') = 'team'
WHERE tm.user_id = $1 AND tm.deleted_at IS NULL
ORDER BY t.slug ASC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]TeamMembership, 0)
	for rows.Next() {
		var membership TeamMembership
		if err := rows.Scan(&membership.TeamID, &membership.TeamSlug, &membership.TeamName, &membership.TeamNamespace, &membership.UserID, &membership.Role, &membership.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, membership)
	}
	return out, rows.Err()
}

// ListTeamMemberships returns active memberships for a team slug.
func (s *Store) ListTeamMemberships(ctx context.Context, teamSlug string) ([]TeamMembership, error) {
	teamSlug = NormalizeTeamSlug(teamSlug)
	rows, err := s.db.QueryContext(ctx, `
SELECT t.id, t.slug, t.display_name, COALESCE(n.namespace, ''), tm.user_id::text, u.email, tm.role, tm.created_at
FROM team_memberships tm
JOIN teams t ON t.id = tm.team_id AND t.deleted_at IS NULL
JOIN users u ON u.id = tm.user_id AND u.deleted_at IS NULL
LEFT JOIN namespaces n ON n.team_id = t.id AND n.deleted_at IS NULL AND COALESCE(n.scope, 'team') = 'team'
WHERE t.slug = $1 AND tm.deleted_at IS NULL
ORDER BY u.email ASC`, teamSlug)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]TeamMembership, 0)
	for rows.Next() {
		var membership TeamMembership
		if err := rows.Scan(&membership.TeamID, &membership.TeamSlug, &membership.TeamName, &membership.TeamNamespace, &membership.UserID, &membership.Email, &membership.Role, &membership.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, membership)
	}
	return out, rows.Err()
}

// GetTeamBySlug returns one non-deleted team by normalized slug.
func (s *Store) GetTeamBySlug(ctx context.Context, slug string) (Team, bool, error) {
	slug = NormalizeTeamSlug(slug)
	var team Team
	err := s.db.QueryRowContext(ctx, `
SELECT t.id, t.slug, t.display_name, COALESCE(n.namespace, ''), t.created_at
FROM teams t
LEFT JOIN namespaces n ON n.team_id = t.id AND n.deleted_at IS NULL AND COALESCE(n.scope, 'team') = 'team'
WHERE t.slug = $1 AND t.deleted_at IS NULL`, slug).
		Scan(&team.ID, &team.Slug, &team.Name, &team.Namespace, &team.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Team{}, false, nil
	}
	if err != nil {
		return Team{}, false, err
	}
	return team, true, nil
}

// ResolveTeamIDs returns a map of team UUID → slug for the given IDs.
// Unknown or deleted team IDs are silently omitted.
func (s *Store) ResolveTeamIDs(ctx context.Context, ids []string) (map[string]string, error) {
	if len(ids) == 0 {
		return map[string]string{}, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, slug FROM teams WHERE id = ANY(ARRAY["+strings.Join(placeholders, ",")+"]::uuid[]) AND deleted_at IS NULL",
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]string, len(ids))
	for rows.Next() {
		var id, slug string
		if err := rows.Scan(&id, &slug); err != nil {
			return nil, err
		}
		out[id] = slug
	}
	return out, rows.Err()
}

// ResolveUserIDs returns a map of user UUID → email for the given IDs.
// Unknown or deleted user IDs are silently omitted.
func (s *Store) ResolveUserIDs(ctx context.Context, ids []string) (map[string]string, error) {
	if len(ids) == 0 {
		return map[string]string{}, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, email FROM users WHERE id = ANY(ARRAY["+strings.Join(placeholders, ",")+"]::uuid[]) AND deleted_at IS NULL",
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]string, len(ids))
	for rows.Next() {
		var id, email string
		if err := rows.Scan(&id, &email); err != nil {
			return nil, err
		}
		out[id] = email
	}
	return out, rows.Err()
}

// UpsertTeamMembership creates or updates a user's role in a team.
func (s *Store) UpsertTeamMembership(ctx context.Context, teamSlug, userID, role string) (TeamMembership, error) {
	teamSlug = NormalizeTeamSlug(teamSlug)
	userID = strings.TrimSpace(userID)
	role = normalizeTeamMembershipRole(role)
	if userID == "" {
		return TeamMembership{}, errors.New("userID is required")
	}
	if role == "" {
		return TeamMembership{}, errors.New("membership role is required")
	}
	team, ok, err := s.GetTeamBySlug(ctx, teamSlug)
	if err != nil {
		return TeamMembership{}, err
	}
	if !ok {
		return TeamMembership{}, sql.ErrNoRows
	}
	if _, exists, err := s.GetUser(ctx, userID); err != nil {
		return TeamMembership{}, err
	} else if !exists {
		return TeamMembership{}, sql.ErrNoRows
	}
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO team_memberships (id, team_id, user_id, role)
VALUES ($1, $2, $3, $4)
ON CONFLICT (team_id, user_id) WHERE deleted_at IS NULL
DO UPDATE SET role = EXCLUDED.role, deleted_at = NULL`, uuid.NewString(), team.ID, userID, role); err != nil {
		return TeamMembership{}, err
	}
	return TeamMembership{
		TeamID:        team.ID,
		TeamSlug:      team.Slug,
		TeamName:      team.Name,
		TeamNamespace: team.Namespace,
		UserID:        userID,
		Role:          role,
		CreatedAt:     time.Now().UTC(),
	}, nil
}

// DeleteTeamMembership soft-deletes a user's active team membership.
func (s *Store) DeleteTeamMembership(ctx context.Context, teamSlug, userID string) error {
	team, ok, err := s.GetTeamBySlug(ctx, teamSlug)
	if err != nil {
		return err
	}
	if !ok {
		return sql.ErrNoRows
	}
	result, err := s.db.ExecContext(ctx, `UPDATE team_memberships SET deleted_at = now() WHERE team_id = $1 AND user_id = $2 AND deleted_at IS NULL`, team.ID, strings.TrimSpace(userID))
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// NormalizeTeamSlug trims and lowercases a team slug.
func NormalizeTeamSlug(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

func scanNamespaceRow(rows *sql.Rows) (map[string]any, error) {
	var id, userID, email, teamID, teamSlug, teamName, namespace, scope string
	var createdAt time.Time
	if err := rows.Scan(&id, &userID, &email, &teamID, &teamSlug, &teamName, &namespace, &scope, &createdAt); err != nil {
		return nil, err
	}
	return map[string]any{
		"id":         id,
		"user_id":    userID,
		"email":      email,
		"team_id":    teamID,
		"team_slug":  teamSlug,
		"team_name":  teamName,
		"scope":      scope,
		"namespace":  namespace,
		"created_at": createdAt,
		"is_shared":  namespace == SharedCatalogNamespace,
		"is_managed": strings.HasPrefix(namespace, TeamNamespacePrefix),
	}, nil
}

func normalizeTeamMembershipRole(raw string) string {
	role := strings.ToLower(strings.TrimSpace(raw))
	switch role {
	case TeamRoleOwner, TeamRoleMember:
		return role
	default:
		return ""
	}
}

// ValidateTeamSlug validates the normalized slug used for team URLs and names.
func ValidateTeamSlug(slug string) error {
	if slug == "" {
		return errors.New("team slug is required")
	}
	if err := sentinelaccess.ValidateResourceName("team", slug); err != nil {
		return err
	}
	return nil
}

// ValidateTeamNamespace validates a managed team namespace name.
func ValidateTeamNamespace(namespace string) error {
	if namespace == "" {
		return errors.New("namespace required")
	}
	if strings.TrimSpace(namespace) == SharedCatalogNamespace {
		return errors.New("shared catalog namespace is reserved")
	}
	reserved := []string{
		"default", "kube-system", "kube-public", "kube-node-lease",
		platforminventory.OperatorNamespace,
		platforminventory.PlatformNamespace,
		platforminventory.ObservabilityNamespace,
		platforminventory.LogCollectorNamespace,
		"registry", "traefik",
	}
	for _, disallowed := range reserved {
		if namespace == disallowed {
			return fmt.Errorf("namespace %q is reserved", namespace)
		}
	}
	if err := sentinelaccess.ValidateResourceName("namespace", namespace); err != nil {
		return err
	}
	return nil
}
