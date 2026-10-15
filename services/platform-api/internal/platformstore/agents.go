package platformstore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/lib/pq"
	"golang.org/x/text/cases"
	"mcp-runtime/pkg/sessionstate"
)

var (
	ErrAgentNotFound    = errors.New("agent not found")
	ErrAgentNameTaken   = errors.New("agent name already exists in team")
	ErrInvalidAgentName = errors.New("agent name must contain 1 to 64 characters")
)

const agentIDAlphabet = "0123456789abcdefghjkmnpqrstvwxyz"

// NewAgentID returns a platform-generated ID with a lowercase ULID suffix.
func NewAgentID(now time.Time, entropy []byte) (string, error) {
	if len(entropy) != 10 {
		return "", fmt.Errorf("ULID entropy must be 10 bytes")
	}
	value := make([]byte, 16)
	ms := uint64(now.UnixMilli())
	for i := 5; i >= 0; i-- {
		value[i] = byte(ms)
		ms >>= 8
	}
	copy(value[6:], entropy)
	n := new(big.Int).SetBytes(value)
	base := big.NewInt(32)
	rem := new(big.Int)
	out := make([]byte, 26)
	for i := len(out) - 1; i >= 0; i-- {
		n.QuoRem(n, base, rem)
		out[i] = agentIDAlphabet[rem.Int64()]
	}
	return "agt_" + string(out), nil
}

func GenerateAgentID() (string, error) {
	var entropy [10]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", err
	}
	return NewAgentID(time.Now().UTC(), entropy[:])
}

// NormalizeAgentName applies the directory's stable whitespace and case rule.
func NormalizeAgentName(name string) (string, string, error) {
	name = strings.Join(strings.Fields(name), " ")
	if len([]rune(name)) < 1 || len([]rune(name)) > 64 {
		return "", "", ErrInvalidAgentName
	}
	return name, cases.Fold().String(name), nil
}

// CreateAgent creates an active, team-owned agent with an immutable platform ID.
func (s *Store) CreateAgent(ctx context.Context, teamSlug, name, createdBy string) (Agent, error) {
	name, normalized, err := NormalizeAgentName(name)
	if err != nil {
		return Agent{}, err
	}
	team, ok, err := s.GetTeamBySlug(ctx, teamSlug)
	if err != nil {
		return Agent{}, err
	}
	if !ok {
		return Agent{}, sql.ErrNoRows
	}
	id, err := GenerateAgentID()
	if err != nil {
		return Agent{}, err
	}
	var result Agent
	err = s.db.QueryRowContext(ctx, `
	INSERT INTO agents (id, team_id, name, name_normalized, status, created_by)
VALUES ($1, $2, $3, $4, 'active', NULLIF($5, '')::uuid)
RETURNING id, team_id::text, name, status, COALESCE(created_by::text, ''), created_at, updated_at`,
		id, team.ID, name, normalized, strings.TrimSpace(createdBy)).Scan(
		&result.ID, &result.TeamID, &result.Name, &result.Status,
		&result.CreatedBy, &result.CreatedAt, &result.UpdatedAt)
	if err != nil {
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code == "23505" {
			return Agent{}, ErrAgentNameTaken
		}
		return Agent{}, err
	}
	result.TeamSlug = team.Slug
	return result, nil
}

// ListAgents returns a stable, bounded page of team agents.
func (s *Store) ListAgents(ctx context.Context, filter AgentListFilter) (AgentPage, error) {
	filter.Limit = clampAgentPageSize(filter.Limit)
	team, ok, err := s.GetTeamBySlug(ctx, filter.TeamSlug)
	if err != nil {
		return AgentPage{}, err
	}
	if !ok {
		return AgentPage{}, sql.ErrNoRows
	}
	args := []any{team.ID}
	where := "team_id = $1"
	if filter.Status != "" {
		args = append(args, filter.Status)
		where += fmt.Sprintf(" AND status = $%d", len(args))
	}
	if q := cases.Fold().String(strings.TrimSpace(filter.Query)); q != "" {
		args = append(args, q)
		where += fmt.Sprintf(" AND position($%d in name_normalized) > 0", len(args))
	}
	if filter.Cursor != "" {
		createdAt, id, err := decodeAgentCursor(filter.Cursor)
		if err != nil {
			return AgentPage{}, err
		}
		args = append(args, createdAt, id)
		where += fmt.Sprintf(" AND (created_at, id) < ($%d, $%d)", len(args)-1, len(args))
	}
	args = append(args, filter.Limit+1)
	query := `SELECT id, team_id::text, name, status, COALESCE(created_by::text, ''), created_at, updated_at, deactivated_at, COALESCE(deactivated_by::text, '')
FROM agents WHERE ` + where + fmt.Sprintf(" ORDER BY created_at DESC, id DESC LIMIT $%d", len(args))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return AgentPage{}, err
	}
	defer rows.Close()
	items := make([]Agent, 0, filter.Limit)
	for rows.Next() {
		var item Agent
		if err := rows.Scan(&item.ID, &item.TeamID, &item.Name, &item.Status, &item.CreatedBy,
			&item.CreatedAt, &item.UpdatedAt, &item.DeactivatedAt, &item.DeactivatedBy); err != nil {
			return AgentPage{}, err
		}
		item.TeamSlug = team.Slug
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return AgentPage{}, err
	}
	page := AgentPage{Agents: items}
	if len(items) > filter.Limit {
		last := items[filter.Limit-1]
		page.Agents = items[:filter.Limit]
		page.NextCursor = encodeAgentCursor(last.CreatedAt, last.ID)
	}
	return page, nil
}

func (s *Store) GetAgent(ctx context.Context, id string) (Agent, bool, error) {
	var item Agent
	err := s.db.QueryRowContext(ctx, `SELECT a.id, a.team_id::text, t.slug, a.name, a.status,
COALESCE(a.created_by::text, ''), a.created_at, a.updated_at, a.deactivated_at, COALESCE(a.deactivated_by::text, '')
FROM agents a JOIN teams t ON t.id = a.team_id WHERE a.id = $1`, strings.TrimSpace(id)).Scan(
		&item.ID, &item.TeamID, &item.TeamSlug, &item.Name, &item.Status, &item.CreatedBy,
		&item.CreatedAt, &item.UpdatedAt, &item.DeactivatedAt, &item.DeactivatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, false, nil
	}
	return item, err == nil, err
}

func (s *Store) RenameAgent(ctx context.Context, id, name string) (Agent, error) {
	name, normalized, err := NormalizeAgentName(name)
	if err != nil {
		return Agent{}, err
	}
	var item Agent
	err = s.db.QueryRowContext(ctx, `UPDATE agents SET name = $2, name_normalized = $3, updated_at = now()
WHERE id = $1 RETURNING id, team_id::text, name, status, COALESCE(created_by::text, ''), created_at, updated_at, deactivated_at, COALESCE(deactivated_by::text, '')`,
		strings.TrimSpace(id), name, normalized).Scan(&item.ID, &item.TeamID, &item.Name, &item.Status,
		&item.CreatedBy, &item.CreatedAt, &item.UpdatedAt, &item.DeactivatedAt, &item.DeactivatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, ErrAgentNotFound
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23505" {
		return Agent{}, ErrAgentNameTaken
	}
	return item, err
}

func (s *Store) SetAgentStatus(ctx context.Context, id, status, actorID string) (Agent, error) {
	if status != "active" && status != "inactive" {
		return Agent{}, fmt.Errorf("invalid agent status")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Agent{}, err
	}
	defer tx.Rollback()
	if err = lockSessionState(ctx, tx); err != nil {
		return Agent{}, err
	}
	var item Agent
	err = tx.QueryRowContext(ctx, `UPDATE agents SET status=$2, updated_at=now(),
 deactivated_at=CASE WHEN $2='inactive' THEN COALESCE(deactivated_at,now()) ELSE NULL END,
 deactivated_by=CASE WHEN $2='inactive' THEN NULLIF($3,'')::uuid ELSE NULL END
 WHERE id=$1 RETURNING id,team_id::text,name,status,COALESCE(created_by::text,''),created_at,updated_at,deactivated_at,COALESCE(deactivated_by::text,'')`,
		strings.TrimSpace(id), status, strings.TrimSpace(actorID)).Scan(&item.ID, &item.TeamID, &item.Name, &item.Status, &item.CreatedBy, &item.CreatedAt, &item.UpdatedAt, &item.DeactivatedAt, &item.DeactivatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, ErrAgentNotFound
	}
	if err != nil {
		return Agent{}, err
	}
	if status == "inactive" {
		if _, err = revokeSessionsTx(ctx, tx, sessionstate.Filter{AgentID: item.ID}, "agent_deactivated"); err != nil {
			return Agent{}, err
		}
	}
	err = tx.QueryRowContext(ctx, `SELECT slug FROM teams WHERE id=$1`, item.TeamID).Scan(&item.TeamSlug)
	if err != nil {
		return Agent{}, err
	}
	return item, tx.Commit()
}

func clampAgentPageSize(limit int) int {
	if limit < 1 {
		return 50
	}
	if limit > 200 {
		return 200
	}
	return limit
}

func encodeAgentCursor(createdAt time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(createdAt.UTC().Format(time.RFC3339Nano) + "|" + id))
}

func decodeAgentCursor(cursor string) (time.Time, string, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("invalid cursor")
	}
	parts := strings.SplitN(string(decoded), "|", 2)
	if len(parts) != 2 || parts[1] == "" {
		return time.Time{}, "", fmt.Errorf("invalid cursor")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", fmt.Errorf("invalid cursor")
	}
	return createdAt, parts[1], nil
}
