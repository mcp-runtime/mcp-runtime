package platformstore

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"
	"mcp-runtime/pkg/platformauth"
)

// ErrEmailAlreadyRegistered is safe to report without exposing SQL or user data.
var ErrEmailAlreadyRegistered = errors.New("a user with this email already exists; add them as a member instead")

// UserInputError identifies safe account validation messages.
type UserInputError string

func (e UserInputError) Error() string { return string(e) }

func preparePasswordUser(email, password, role string) (User, []byte, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	role = strings.TrimSpace(role)
	if role == "" {
		role = RoleUser
	}
	if role != RoleUser && role != RoleAdmin {
		return User{}, nil, UserInputError("role must be user or admin")
	}
	if !validEmail(email) {
		return User{}, nil, UserInputError("valid email required")
	}
	if len(password) < 8 {
		return User{}, nil, UserInputError("password must be at least 8 characters")
	}
	if len(password) > 72 {
		return User{}, nil, UserInputError("password must be at most 72 bytes")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return User{}, nil, err
	}
	return User{ID: uuid.NewString(), Email: email, Role: role}, hash, nil
}

func insertPasswordUser(ctx context.Context, tx *sql.Tx, user User, hash []byte) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO users (id,email,role) VALUES ($1,$2,$3)`, user.ID, user.Email, user.Role); err != nil {
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code == "23505" && pqErr.Constraint == "users_email_key" {
			return ErrEmailAlreadyRegistered
		}
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO auth_identities (user_id,provider,subject,password_hash) VALUES ($1,$2,$3,$4)`, user.ID, passwordProvider, user.Email, string(hash))
	return err
}

// CreatePasswordUser creates a password-login platform account with the requested role.
func (s *Store) CreatePasswordUser(ctx context.Context, email, password, role string) (User, error) {
	user, hash, err := preparePasswordUser(email, password, role)
	if err != nil {
		return User{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	if err := insertPasswordUser(ctx, tx, user, hash); err != nil {
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return user, nil
}

// CreateTeamUser atomically creates a password account and its team membership.
func (s *Store) CreateTeamUser(ctx context.Context, teamSlug, email, password, role string) (User, TeamMembership, error) {
	role = normalizeTeamMembershipRole(role)
	if role == "" {
		return User{}, TeamMembership{}, UserInputError("membership role must be owner or member")
	}
	user, hash, err := preparePasswordUser(email, password, RoleUser)
	if err != nil {
		return User{}, TeamMembership{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, TeamMembership{}, err
	}
	defer tx.Rollback()
	var team Team
	err = tx.QueryRowContext(ctx, `SELECT id, slug, name, namespace FROM teams WHERE slug = $1 AND deleted_at IS NULL FOR SHARE`, NormalizeTeamSlug(teamSlug)).Scan(&team.ID, &team.Slug, &team.Name, &team.Namespace)
	if err != nil {
		return User{}, TeamMembership{}, err
	}
	if err := insertPasswordUser(ctx, tx, user, hash); err != nil {
		return User{}, TeamMembership{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO team_memberships (id, team_id, user_id, role) VALUES ($1, $2, $3, $4)`, uuid.NewString(), team.ID, user.ID, role)
	if err != nil {
		return User{}, TeamMembership{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, TeamMembership{}, err
	}
	return user, TeamMembership{TeamID: team.ID, TeamSlug: team.Slug, TeamName: team.Name, TeamNamespace: team.Namespace, UserID: user.ID, Role: role, CreatedAt: time.Now().UTC()}, nil
}

// EnsurePasswordUser creates or updates a password-login account for a fixed role.
func (s *Store) EnsurePasswordUser(ctx context.Context, email, password string, role string) (User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	role = strings.TrimSpace(role)
	if role == "" {
		role = RoleUser
	}
	if role != RoleUser && role != RoleAdmin {
		return User{}, errors.New("role must be user or admin")
	}
	if !validEmail(email) {
		return User{}, errors.New("valid email required")
	}
	if len(password) < 8 {
		return User{}, errors.New("password must be at least 8 characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return User{}, err
	}

	var u User
	err = s.db.QueryRowContext(ctx, `
SELECT u.id, u.email, u.role, ''
FROM users u
WHERE u.email = $1 AND u.deleted_at IS NULL`, email).
		Scan(&u.ID, &u.Email, &u.Role, &u.Namespace)
	if errors.Is(err, sql.ErrNoRows) {
		return s.CreatePasswordUser(ctx, email, password, role)
	}
	if err != nil {
		return User{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err = tx.ExecContext(ctx, `UPDATE users SET role = $1 WHERE id = $2`, role, u.ID); err != nil {
		return User{}, err
	}
	if _, err = tx.ExecContext(ctx, `
INSERT INTO auth_identities (user_id, provider, subject, password_hash)
VALUES ($1, $2, $3, $4)
ON CONFLICT (provider, subject)
DO UPDATE SET user_id = EXCLUDED.user_id, password_hash = EXCLUDED.password_hash`, u.ID, passwordProvider, email, string(hash)); err != nil {
		return User{}, err
	}
	if err = tx.Commit(); err != nil {
		return User{}, err
	}
	u.Role = role
	return u, nil
}

// EnsureTeamPasswordUser ensures a regular password-login account exists for
// team membership flows. Existing platform roles are preserved so an admin is
// not accidentally demoted when they are added to a team.
func (s *Store) EnsureTeamPasswordUser(ctx context.Context, email, password string) (User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	password = strings.TrimSpace(password)
	if !validEmail(email) {
		return User{}, errors.New("valid email required")
	}
	if len(password) < 12 {
		return User{}, errors.New("password must be at least 12 characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return User{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var u User
	err = tx.QueryRowContext(ctx, `
SELECT u.id, u.email, u.role, ''
FROM users u
WHERE u.email = $1 AND u.deleted_at IS NULL`, email).
		Scan(&u.ID, &u.Email, &u.Role, &u.Namespace)
	if errors.Is(err, sql.ErrNoRows) {
		u = User{ID: uuid.NewString(), Email: email, Role: RoleUser}
		if _, err = tx.ExecContext(ctx, `INSERT INTO users (id,email,role) VALUES ($1,$2,$3)`, u.ID, u.Email, u.Role); err != nil {
			return User{}, err
		}
	} else if err != nil {
		return User{}, err
	}

	if _, err = tx.ExecContext(ctx, `
INSERT INTO auth_identities (user_id, provider, subject, password_hash)
VALUES ($1, $2, $3, $4)
ON CONFLICT (provider, subject)
DO UPDATE SET user_id = EXCLUDED.user_id, password_hash = EXCLUDED.password_hash`, u.ID, passwordProvider, email, string(hash)); err != nil {
		return User{}, err
	}
	if err = tx.Commit(); err != nil {
		return User{}, err
	}
	return u, nil
}

// AuthenticatePassword validates password-login credentials and returns the matching user.
func (s *Store) AuthenticatePassword(ctx context.Context, email, password string) (User, bool, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	var u User
	var hash string
	err := s.db.QueryRowContext(ctx, `
SELECT u.id, u.email, u.role, '', ai.password_hash
FROM auth_identities ai
JOIN users u ON u.id = ai.user_id AND u.deleted_at IS NULL
WHERE ai.provider = $1 AND ai.subject = $2`, passwordProvider, email).
		Scan(&u.ID, &u.Email, &u.Role, &u.Namespace, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return User{}, false, nil
	}
	return u, true, nil
}

// EnsureOIDCUser binds an OIDC subject to a platform account, creating it when needed.
func (s *Store) EnsureOIDCUser(ctx context.Context, provider, subject, email, role string) (User, error) {
	provider = strings.TrimSpace(provider)
	subject = strings.TrimSpace(subject)
	email = strings.ToLower(strings.TrimSpace(email))
	role = strings.TrimSpace(role)
	if role == "" {
		role = RoleUser
	}
	if role != RoleUser && role != RoleAdmin {
		return User{}, errors.New("role must be user or admin")
	}
	if provider == "" {
		return User{}, errors.New("oidc provider required")
	}
	if subject == "" {
		return User{}, errors.New("oidc subject required")
	}

	var u User
	err := s.db.QueryRowContext(ctx, `
SELECT u.id, u.email, u.role, ''
FROM auth_identities ai
JOIN users u ON u.id = ai.user_id AND u.deleted_at IS NULL
WHERE ai.provider = $1 AND ai.subject = $2`, provider, subject).
		Scan(&u.ID, &u.Email, &u.Role, &u.Namespace)
	if err == nil {
		return s.ensureOIDCUserRole(ctx, u, role)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return User{}, err
	}
	if !validEmail(email) {
		return User{}, errors.New("valid email required for oidc user")
	}

	userExists := true
	err = s.db.QueryRowContext(ctx, `
SELECT u.id, u.email, u.role, ''
FROM users u
WHERE u.email = $1 AND u.deleted_at IS NULL`, email).
		Scan(&u.ID, &u.Email, &u.Role, &u.Namespace)
	if errors.Is(err, sql.ErrNoRows) {
		u = User{ID: uuid.NewString(), Email: email, Role: role}
		userExists = false
	} else if err != nil {
		return User{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if !userExists {
		if _, err = tx.ExecContext(ctx, `INSERT INTO users (id,email,role) VALUES ($1,$2,$3)`, u.ID, u.Email, u.Role); err != nil {
			return User{}, err
		}
	} else if role == RoleAdmin && u.Role != RoleAdmin {
		if _, err = tx.ExecContext(ctx, `UPDATE users SET role = $1 WHERE id = $2`, RoleAdmin, u.ID); err != nil {
			return User{}, err
		}
		u.Role = RoleAdmin
	}
	if _, err = tx.ExecContext(ctx, `
INSERT INTO auth_identities (user_id, provider, subject)
VALUES ($1, $2, $3)
ON CONFLICT (provider, subject)
DO UPDATE SET user_id = EXCLUDED.user_id`, u.ID, provider, subject); err != nil {
		return User{}, err
	}
	if err = tx.Commit(); err != nil {
		return User{}, err
	}
	return u, nil
}

func (s *Store) ensureOIDCUserRole(ctx context.Context, u User, role string) (User, error) {
	if role != RoleAdmin {
		return u, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if role == RoleAdmin && u.Role != RoleAdmin {
		if _, err = tx.ExecContext(ctx, `UPDATE users SET role = $1 WHERE id = $2`, RoleAdmin, u.ID); err != nil {
			return User{}, err
		}
		u.Role = RoleAdmin
	}
	if err = tx.Commit(); err != nil {
		return User{}, err
	}
	return u, nil
}

// GetUser returns a non-deleted user by id.
func (s *Store) GetUser(ctx context.Context, userID string) (User, bool, error) {
	var u User
	err := s.db.QueryRowContext(ctx, `
SELECT u.id, u.email, u.role, ''
FROM users u
WHERE u.id = $1 AND u.deleted_at IS NULL`, userID).
		Scan(&u.ID, &u.Email, &u.Role, &u.Namespace)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, err
	}
	return u, true, nil
}

// DeleteUser deletes a platform user by id.
func (s *Store) DeleteUser(ctx context.Context, userID string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, strings.TrimSpace(userID))
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

// CreateAccessToken signs a short-lived platform JWT for a user.
func (s *Store) CreateAccessToken(u User, ttl time.Duration) (string, error) {
	p := Principal{
		Role:      u.Role,
		Subject:   u.ID,
		Email:     u.Email,
		Namespace: u.Namespace,
	}
	if s.db != nil {
		resolved, err := s.PrincipalForUserID(context.Background(), u.ID)
		if err != nil {
			return "", err
		}
		p = resolved
	}
	p.AuthType = "platform_jwt"
	return platformauth.Sign(s.jwtSecret, p, ttl, platformauth.RequiredAudiences())
}

// AuthenticateJWT validates a platform JWT and resolves it to the current principal.
func (s *Store) AuthenticateJWT(token string) (Principal, bool) {
	if s == nil || len(s.jwtSecret) == 0 {
		return Principal{}, false
	}
	claims, err := platformauth.Verify(s.jwtSecret, token, platformauth.AudiencePlatform)
	if err != nil {
		return Principal{}, false
	}
	return platformauth.ToPrincipal(claims), true
}

// AuthenticateUserAPIKey validates a user API key and resolves its principal.
func (s *Store) AuthenticateUserAPIKey(ctx context.Context, rawKey string) (Principal, bool, error) {
	targetHash := hashAPIKey(rawKey)
	var keyID, userID string
	err := s.db.QueryRowContext(ctx, `
SELECT ak.id, ak.user_id
FROM api_keys ak
JOIN users u ON u.id = ak.user_id AND u.deleted_at IS NULL
WHERE ak.key_hash = $1 AND ak.revoked = false`, targetHash).
		Scan(&keyID, &userID)
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, false, nil
	}
	if err != nil {
		return Principal{}, false, err
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE api_keys SET last_used_at = now() WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '5 minutes')`, keyID)
	p, err := s.PrincipalForUserID(ctx, userID)
	if err != nil {
		return Principal{}, false, err
	}
	p.AuthType = "user_api_key"
	p.APIKeyID = keyID
	return p, true, nil
}

// PrincipalForUserID returns the platform principal for a non-deleted user.
func (s *Store) PrincipalForUserID(ctx context.Context, userID string) (Principal, error) {
	var p Principal
	err := s.db.QueryRowContext(ctx, `
SELECT u.id, u.email, u.role
FROM users u
WHERE u.id = $1 AND u.deleted_at IS NULL`, userID).
		Scan(&p.Subject, &p.Email, &p.Role)
	if err != nil {
		return Principal{}, err
	}
	teams, err := s.listTeamMembershipsForUser(ctx, userID)
	if err != nil {
		return Principal{}, err
	}
	p.Teams = teams
	for _, team := range teams {
		if ns := strings.TrimSpace(team.Namespace); ns != "" {
			p.Namespace = ns
			break
		}
	}
	p.AllowedNamespaces = dedupeNamespaces(append(collectAllowedNamespaces(teams), SharedCatalogNamespace))
	return p, nil
}

func collectAllowedNamespaces(teams []PrincipalTeam) []string {
	namespaces := make([]string, 0, len(teams))
	for _, team := range teams {
		if ns := strings.TrimSpace(team.Namespace); ns != "" {
			namespaces = append(namespaces, ns)
		}
	}
	return namespaces
}

func dedupeNamespaces(namespaces []string) []string {
	if len(namespaces) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(namespaces))
	out := make([]string, 0, len(namespaces))
	for _, namespace := range namespaces {
		namespace = strings.TrimSpace(namespace)
		if namespace == "" {
			continue
		}
		if _, ok := seen[namespace]; ok {
			continue
		}
		seen[namespace] = struct{}{}
		out = append(out, namespace)
	}
	return out
}

func (s *Store) listTeamMembershipsForUser(ctx context.Context, userID string) ([]PrincipalTeam, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT t.id, t.slug, t.display_name, COALESCE(n.namespace, ''), tm.role
FROM team_memberships tm
JOIN teams t ON t.id = tm.team_id AND t.deleted_at IS NULL
LEFT JOIN namespaces n ON n.team_id = t.id AND n.deleted_at IS NULL AND COALESCE(n.scope, 'team') = 'team'
WHERE tm.user_id = $1 AND tm.deleted_at IS NULL
ORDER BY t.slug ASC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	teams := make([]PrincipalTeam, 0)
	for rows.Next() {
		var team PrincipalTeam
		if err := rows.Scan(&team.ID, &team.Slug, &team.Name, &team.Namespace, &team.Role); err != nil {
			return nil, err
		}
		teams = append(teams, team)
	}
	return teams, rows.Err()
}
func validEmail(email string) bool {
	if len(email) > 254 || !strings.Contains(email, "@") {
		return false
	}
	host := email[strings.LastIndex(email, "@")+1:]
	return host != "" && net.ParseIP(host) == nil
}
