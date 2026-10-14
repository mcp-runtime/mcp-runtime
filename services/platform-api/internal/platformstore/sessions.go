package platformstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"mcp-runtime/pkg/policy"
	"mcp-runtime/pkg/sessionstate"
)

const sessionSchemaSQL = `
CREATE TABLE IF NOT EXISTS session_sync_state (
 singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
 epoch text NOT NULL, sequence bigint NOT NULL DEFAULT 0,
 floor bigint NOT NULL DEFAULT 0
);
INSERT INTO session_sync_state(singleton, epoch) VALUES(true, uuid_generate_v4()::text)
 ON CONFLICT(singleton) DO NOTHING;
CREATE TABLE IF NOT EXISTS sessions (
 id text PRIMARY KEY, namespace text NOT NULL, name text NOT NULL,
 server_namespace text NOT NULL, server_name text NOT NULL, server_uid text NOT NULL,
 agent_id text NOT NULL, subject_team_id text NOT NULL, authority_team_id text NOT NULL,
 grant_namespace text NOT NULL, grant_name text NOT NULL,
 revoked boolean NOT NULL, expires_at timestamptz, data jsonb NOT NULL,
 UNIQUE(namespace,name)
);
CREATE INDEX IF NOT EXISTS idx_sessions_server ON sessions(server_namespace, server_uid);
CREATE INDEX IF NOT EXISTS idx_sessions_agent ON sessions(agent_id);
CREATE INDEX IF NOT EXISTS idx_sessions_grant ON sessions(grant_namespace, grant_name);
CREATE TABLE IF NOT EXISTS session_changes (
 sequence bigint PRIMARY KEY, server_namespace text NOT NULL, server_uid text NOT NULL,
 data jsonb NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_session_changes_server ON session_changes(server_namespace,server_uid,sequence);
CREATE TABLE IF NOT EXISTS session_retired_servers (
 server_uid text PRIMARY KEY, retired_at timestamptz NOT NULL DEFAULT now()
);
`

const sessionChangeRetention = 100000

func (s *Store) initializeSessions(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, sessionSchemaSQL); err != nil {
		return err
	}
	// A process restart is also the restore fence. Rotating the shared epoch
	// makes every gateway relist, including if a restored sequence is unchanged.
	_, err := s.db.ExecContext(ctx, `UPDATE session_sync_state SET epoch=$1 WHERE singleton`, uuid.NewString())
	return err
}

func validateSession(item sessionstate.Session) error {
	if item.Namespace == "" || item.Binding.Name == "" || item.ServerNamespace == "" || item.ServerName == "" || item.ServerUID == "" || item.Binding.AgentID == "" || item.Binding.TeamID == "" {
		return fmt.Errorf("session namespace, name, server UID, agent and subject team are required")
	}
	if strings.ContainsAny(item.Namespace+string(item.Binding.Name), "/\\") {
		return fmt.Errorf("invalid session identity")
	}
	if item.Binding.Namespace != "" && string(item.Binding.Namespace) != item.Namespace {
		return fmt.Errorf("session namespace mismatch")
	}
	if item.Binding.ExpiresAt != "" {
		if _, err := time.Parse(time.RFC3339, item.Binding.ExpiresAt); err != nil {
			return fmt.Errorf("invalid session expiry")
		}
	}
	return nil
}

// lockSessionState serializes change sequence allocation through commit. SQL
// sequences alone are unsafe: transaction N+1 could commit before N.
func lockSessionState(ctx context.Context, tx *sql.Tx) error {
	var seq int64
	return tx.QueryRowContext(ctx, `SELECT sequence FROM session_sync_state WHERE singleton FOR UPDATE`).Scan(&seq)
}

func appendSessionChange(ctx context.Context, tx *sql.Tx, item sessionstate.Session, deleted bool) error {
	var seq int64
	if err := tx.QueryRowContext(ctx, `UPDATE session_sync_state SET sequence=sequence+1 WHERE singleton RETURNING sequence`).Scan(&seq); err != nil {
		return err
	}
	change := sessionstate.Change{Kind: "session", Sequence: seq, Operation: "upsert", ID: item.ID, Session: &item}
	if deleted || item.Binding.Revoked {
		change.Operation = "delete"
		change.Session = nil
	}
	data, err := json.Marshal(change)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO session_changes(sequence,server_namespace,server_uid,data) VALUES($1,$2,$3,$4)`, seq, item.ServerNamespace, item.ServerUID, string(data)); err != nil {
		return err
	}
	// Retain a bounded log. floor is the last removed sequence, not the first
	// retained one, so a cursor exactly at the floor remains valid.
	floor := seq - sessionChangeRetention
	if floor > 0 {
		if _, err = tx.ExecContext(ctx, `DELETE FROM session_changes WHERE sequence <= $1`, floor); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE session_sync_state SET floor=GREATEST(floor,$1) WHERE singleton`, floor)
	}
	return err
}

func writeSession(ctx context.Context, tx *sql.Tx, item sessionstate.Session) error {
	data, err := json.Marshal(item)
	if err != nil {
		return err
	}
	var expiry any
	if item.Binding.ExpiresAt != "" {
		expiry, _ = time.Parse(time.RFC3339, item.Binding.ExpiresAt)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO sessions(id,namespace,name,server_namespace,server_name,server_uid,agent_id,subject_team_id,authority_team_id,grant_namespace,grant_name,revoked,expires_at,data)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
 ON CONFLICT(id) DO UPDATE SET agent_id=EXCLUDED.agent_id,subject_team_id=EXCLUDED.subject_team_id,authority_team_id=EXCLUDED.authority_team_id,grant_namespace=EXCLUDED.grant_namespace,grant_name=EXCLUDED.grant_name,revoked=EXCLUDED.revoked,expires_at=EXCLUDED.expires_at,data=EXCLUDED.data`,
		item.ID, item.Namespace, string(item.Binding.Name), item.ServerNamespace, item.ServerName, item.ServerUID, string(item.Binding.AgentID), string(item.Binding.TeamID), item.AuthorityTeamID, item.GrantNamespace, item.GrantName, item.Binding.Revoked, expiry, string(data))
	if err != nil {
		return err
	}
	return appendSessionChange(ctx, tx, item, false)
}

// PutSession validates active directory membership inside the same transaction
// as persistence. The state lock is acquired before the agent lock everywhere.
func (s *Store) PutSession(ctx context.Context, item sessionstate.Session) (sessionstate.Session, error) {
	if err := validateSession(item); err != nil {
		return sessionstate.Session{}, err
	}
	item.ID = item.Namespace + "/" + string(item.Binding.Name)
	item.Binding.Namespace = policy.Namespace(item.Namespace)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return sessionstate.Session{}, err
	}
	defer tx.Rollback()
	if err = lockSessionState(ctx, tx); err != nil {
		return sessionstate.Session{}, err
	}
	var retired bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM session_retired_servers WHERE server_uid=$1)`, item.ServerUID).Scan(&retired); err != nil {
		return sessionstate.Session{}, err
	}
	if retired {
		return sessionstate.Session{}, sessionstate.ErrConflict
	}
	var status, team string
	err = tx.QueryRowContext(ctx, `SELECT status,team_id::text FROM agents WHERE id=$1 FOR UPDATE`, string(item.Binding.AgentID)).Scan(&status, &team)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (status != "active" || team != string(item.Binding.TeamID))) {
		return sessionstate.Session{}, sessionstate.ErrInactiveAgent
	}
	if err != nil {
		return sessionstate.Session{}, err
	}
	var previous []byte
	err = tx.QueryRowContext(ctx, `SELECT data FROM sessions WHERE id=$1`, item.ID).Scan(&previous)
	if err == nil {
		var old sessionstate.Session
		if err = json.Unmarshal(previous, &old); err != nil {
			return sessionstate.Session{}, err
		}
		if old.ServerUID != item.ServerUID || old.ServerNamespace != item.ServerNamespace || old.ServerName != item.ServerName {
			return sessionstate.Session{}, sessionstate.ErrConflict
		}
		item.UID = old.UID
		item.CreatedAt = old.CreatedAt
	} else if !errors.Is(err, sql.ErrNoRows) {
		return sessionstate.Session{}, err
	}
	if item.UID == "" {
		item.UID = uuid.NewString()
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now().UTC()
	}
	if item.Binding.Revoked && item.RevokedAt == nil {
		now := time.Now().UTC()
		item.RevokedAt = &now
	}
	if !item.Binding.Revoked {
		item.RevokedAt = nil
		item.RevocationCause = ""
	}
	if err = writeSession(ctx, tx, item); err != nil {
		return sessionstate.Session{}, err
	}
	return item, tx.Commit()
}

func (s *Store) GetSession(ctx context.Context, namespace, name string) (sessionstate.Session, bool, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT data FROM sessions WHERE namespace=$1 AND name=$2`, namespace, name).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return sessionstate.Session{}, false, nil
	}
	if err != nil {
		return sessionstate.Session{}, false, err
	}
	var item sessionstate.Session
	err = json.Unmarshal(data, &item)
	return item, err == nil, err
}

func sessionFilter(filter sessionstate.Filter) (string, []any) {
	where := "true"
	args := []any{}
	add := func(column, value string) {
		if value != "" {
			args = append(args, value)
			where += fmt.Sprintf(" AND %s=$%d", column, len(args))
		}
	}
	add("namespace", filter.Namespace)
	add("server_name", filter.ServerName)
	add("server_uid", filter.ServerUID)
	add("agent_id", filter.AgentID)
	add("grant_namespace", filter.GrantNamespace)
	add("grant_name", filter.GrantName)
	if filter.After != "" {
		args = append(args, filter.After)
		where += fmt.Sprintf(" AND id > $%d", len(args))
	}
	return where, args
}

func (s *Store) ListSessions(ctx context.Context, filter sessionstate.Filter) (sessionstate.Page, error) {
	where, args := sessionFilter(filter)
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM sessions WHERE `+where+fmt.Sprintf(` ORDER BY id LIMIT $%d`, len(args)), args...)
	if err != nil {
		return sessionstate.Page{}, err
	}
	defer rows.Close()
	items := make([]sessionstate.Session, 0)
	for rows.Next() {
		var data []byte
		var item sessionstate.Session
		if err = rows.Scan(&data); err != nil {
			return sessionstate.Page{}, err
		}
		if err = json.Unmarshal(data, &item); err != nil {
			return sessionstate.Page{}, err
		}
		items = append(items, item)
	}
	page := sessionstate.Page{Sessions: items}
	if len(items) > limit {
		page.Sessions = items[:limit]
		page.NextCursor = items[limit-1].ID
	}
	return page, rows.Err()
}

func (s *Store) SetSessionRevoked(ctx context.Context, namespace, name string, revoked bool, cause string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockSessionState(ctx, tx); err != nil {
		return err
	}
	var data []byte
	if err = tx.QueryRowContext(ctx, `SELECT data FROM sessions WHERE namespace=$1 AND name=$2`, namespace, name).Scan(&data); errors.Is(err, sql.ErrNoRows) {
		return sessionstate.ErrNotFound
	} else if err != nil {
		return err
	}
	var item sessionstate.Session
	if err = json.Unmarshal(data, &item); err != nil {
		return err
	}
	if !revoked {
		var status, team string
		err = tx.QueryRowContext(ctx, `SELECT status,team_id::text FROM agents WHERE id=$1 FOR UPDATE`, string(item.Binding.AgentID)).Scan(&status, &team)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && (status != "active" || team != string(item.Binding.TeamID))) {
			return sessionstate.ErrInactiveAgent
		}
		if err != nil {
			return err
		}
	}
	item.Binding.Revoked = revoked
	item.RevocationCause = cause
	if revoked {
		now := time.Now().UTC()
		item.RevokedAt = &now
	} else {
		item.RevokedAt = nil
		item.RevocationCause = ""
	}
	if err = writeSession(ctx, tx, item); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteSession(ctx context.Context, namespace, name string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockSessionState(ctx, tx); err != nil {
		return err
	}
	var data []byte
	err = tx.QueryRowContext(ctx, `DELETE FROM sessions WHERE namespace=$1 AND name=$2 RETURNING data`, namespace, name).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return sessionstate.ErrNotFound
	}
	if err != nil {
		return err
	}
	var item sessionstate.Session
	if err = json.Unmarshal(data, &item); err != nil {
		return err
	}
	if err = appendSessionChange(ctx, tx, item, true); err != nil {
		return err
	}
	return tx.Commit()
}

func revokeSessionsTx(ctx context.Context, tx *sql.Tx, filter sessionstate.Filter, cause string) (int64, error) {
	where, args := sessionFilter(filter)
	rows, err := tx.QueryContext(ctx, `SELECT data FROM sessions WHERE NOT revoked AND `+where, args...)
	if err != nil {
		return 0, err
	}
	items := []sessionstate.Session{}
	for rows.Next() {
		var data []byte
		var item sessionstate.Session
		if err = rows.Scan(&data); err != nil {
			rows.Close()
			return 0, err
		}
		if err = json.Unmarshal(data, &item); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	for _, item := range items {
		item.Binding.Revoked = true
		item.RevokedAt = &now
		item.RevocationCause = cause
		if err = writeSession(ctx, tx, item); err != nil {
			return 0, err
		}
	}
	return int64(len(items)), nil
}

func (s *Store) RevokeSessions(ctx context.Context, filter sessionstate.Filter, cause string) (int64, error) {
	if filter.ServerUID == "" && filter.AgentID == "" && filter.GrantName == "" {
		return 0, fmt.Errorf("bulk revocation requires server UID, agent or grant")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err = lockSessionState(ctx, tx); err != nil {
		return 0, err
	}
	if cause == "server_retired" {
		if filter.ServerUID == "" {
			return 0, fmt.Errorf("retirement requires server UID")
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO session_retired_servers(server_uid) VALUES($1) ON CONFLICT DO NOTHING`, filter.ServerUID); err != nil {
			return 0, err
		}
	}
	count, err := revokeSessionsTx(ctx, tx, filter, cause)
	if err != nil {
		return 0, err
	}
	return count, tx.Commit()
}

func (s *Store) SessionSnapshot(ctx context.Context, server sessionstate.Server) (sessionstate.Snapshot, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return sessionstate.Snapshot{}, err
	}
	defer tx.Rollback()
	out := sessionstate.Snapshot{Sessions: []sessionstate.Session{}}
	if err = tx.QueryRowContext(ctx, `SELECT epoch,sequence FROM session_sync_state WHERE singleton`).Scan(&out.Epoch, &out.Sequence); err != nil {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT data FROM sessions WHERE server_namespace=$1 AND server_uid=$2 AND NOT revoked AND (expires_at IS NULL OR expires_at>now()) ORDER BY id`, server.Namespace, server.UID)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var data []byte
		var item sessionstate.Session
		if err = rows.Scan(&data); err != nil {
			rows.Close()
			return out, err
		}
		if err = json.Unmarshal(data, &item); err != nil {
			rows.Close()
			return out, err
		}
		out.Sessions = append(out.Sessions, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}

func (s *Store) SessionChanges(ctx context.Context, server sessionstate.Server, epoch string, after int64) (sessionstate.Changes, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return sessionstate.Changes{}, err
	}
	defer tx.Rollback()
	out := sessionstate.Changes{Changes: []sessionstate.Change{}}
	var floor int64
	if err = tx.QueryRowContext(ctx, `SELECT epoch,sequence,floor FROM session_sync_state WHERE singleton`).Scan(&out.Epoch, &out.Sequence, &floor); err != nil {
		return out, err
	}
	if epoch != out.Epoch || after < floor || after > out.Sequence || after < 0 {
		return out, sessionstate.ErrReset
	}
	rows, err := tx.QueryContext(ctx, `SELECT data FROM session_changes WHERE server_namespace=$1 AND server_uid=$2 AND sequence>$3 ORDER BY sequence LIMIT $4`, server.Namespace, server.UID, after, sessionstate.ChangeLimit+1)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var data []byte
		var change sessionstate.Change
		if err = rows.Scan(&data); err != nil {
			rows.Close()
			return out, err
		}
		if err = json.Unmarshal(data, &change); err != nil {
			rows.Close()
			return out, err
		}
		out.Changes = append(out.Changes, change)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(out.Changes) > sessionstate.ChangeLimit {
		out.More = true
		out.Changes = out.Changes[:sessionstate.ChangeLimit]
		out.Sequence = out.Changes[len(out.Changes)-1].Sequence
	}
	return out, tx.Commit()
}
