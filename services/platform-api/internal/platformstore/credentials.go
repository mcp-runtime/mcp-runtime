package platformstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	cb "mcp-runtime/pkg/credentialbroker"
	"mcp-runtime/pkg/platformauth"
	"mcp-runtime/pkg/sessionstate"
)

const credentialSchemaSQL = `
CREATE TABLE IF NOT EXISTS credential_metadata (
 namespace TEXT NOT NULL, kind TEXT NOT NULL CHECK(kind IN ('sources','bindings')),
 name TEXT NOT NULL, revision BIGINT NOT NULL DEFAULT 1, data JSONB NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(namespace,kind,name)
);
CREATE INDEX IF NOT EXISTS credential_metadata_sources ON credential_metadata(namespace,(data->>'source')) WHERE kind='bindings';
`

type credentialQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// CheckCredentialNamespace reads current ownership, never JWT team claims. A
// namespace admission grant alone does not authorize managing its credentials.
func credentialNamespace(ctx context.Context, q credentialQuery, p platformauth.Principal, ns string, manage bool) error {
	if p.IsService && p.Role == platformauth.RoleAdmin {
		return nil
	}
	if p.UserID() == "" {
		return cb.ErrDenied
	}
	var allowed bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users u WHERE u.id::text=$1 AND u.deleted_at IS NULL AND
 (u.role='admin' OR EXISTS(SELECT 1 FROM namespaces n WHERE n.namespace=$2 AND n.deleted_at IS NULL AND
 (n.user_id=u.id OR EXISTS(SELECT 1 FROM team_memberships m JOIN teams t ON t.id=m.team_id
 WHERE m.user_id=u.id AND m.team_id=n.team_id AND m.deleted_at IS NULL AND t.deleted_at IS NULL
 AND (NOT $3 OR m.role='owner'))))))`, p.UserID(), ns, manage).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return cb.ErrDenied
	}
	return nil
}

func credentialAdmin(ctx context.Context, q credentialQuery, p platformauth.Principal) error {
	if p.IsService && p.Role == platformauth.RoleAdmin {
		return nil
	}
	var allowed bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id::text=$1 AND role='admin' AND deleted_at IS NULL)`, p.UserID()).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return cb.ErrDenied
	}
	return nil
}

func credentialSourceAuthority(ctx context.Context, q credentialQuery, p platformauth.Principal, source cb.Source) error {
	if p.IsService && p.Role == platformauth.RoleAdmin {
		return nil
	}
	var allowed bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users u WHERE u.id::text=$1 AND u.deleted_at IS NULL AND
 (u.role='admin' OR u.id::text=$2 OR EXISTS(SELECT 1 FROM team_memberships m JOIN teams t ON t.id=m.team_id
 WHERE m.user_id=u.id AND m.team_id::text=$3 AND m.deleted_at IS NULL AND t.deleted_at IS NULL)))`, p.UserID(), source.UserID, source.TeamID).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return cb.ErrDenied
	}
	return nil
}

func credentialActor(p platformauth.Principal) string {
	if p.IsService {
		return ""
	}
	return p.UserID()
}

func credentialRead(ctx context.Context, q credentialQuery, ns, kind, name string, out any) error {
	var data []byte
	err := q.QueryRowContext(ctx, `SELECT data FROM credential_metadata WHERE namespace=$1 AND kind=$2 AND name=$3`, ns, kind, name).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return cb.ErrNotFound
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func (s *Store) ListCredentials(ctx context.Context, p platformauth.Principal, ns, kind string) ([]json.RawMessage, error) {
	if kind != "sources" && kind != "bindings" {
		return nil, cb.ErrDenied
	}
	if err := credentialNamespace(ctx, s.db, p, ns, false); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM credential_metadata WHERE namespace=$1 AND kind=$2 ORDER BY name`, ns, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var raw json.RawMessage
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if kind == "sources" {
			var source cb.Source
			if err = json.Unmarshal(raw, &source); err != nil {
				return nil, err
			}
			if err = credentialSourceAuthority(ctx, s.db, p, source); errors.Is(err, cb.ErrDenied) {
				continue
			} else if err != nil {
				return nil, err
			}
		}
		out = append(out, raw)
	}
	return out, rows.Err()
}

// PutCredential persists only validated typed metadata. Revision zero creates;
// updates require the exact existing revision, preventing stale policy writes.
func (s *Store) PutCredential(ctx context.Context, p platformauth.Principal, source *cb.Source, binding *cb.Binding) (json.RawMessage, error) {
	var ns, name, kind string
	var revision int64
	var value any
	if source != nil && binding == nil {
		if err := source.Validate(); err != nil {
			return nil, err
		}
		ns, name, kind, revision, value = source.Namespace, source.Name, "sources", source.Revision, source
	}
	if binding != nil && source == nil {
		if err := binding.Validate(); err != nil {
			return nil, err
		}
		ns, name, kind, revision, value = binding.Namespace, binding.Name, "bindings", binding.Revision, binding
	}
	if value == nil {
		return nil, cb.ErrDenied
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = lockSessionState(ctx, tx); err != nil {
		return nil, err
	}
	if source != nil {
		err = credentialAdmin(ctx, tx, p)
	} else {
		err = credentialNamespace(ctx, tx, p, ns, true)
	}
	if err != nil {
		return nil, err
	}
	if source != nil {
		var exists bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM namespaces WHERE namespace=$1 AND deleted_at IS NULL)
 AND (EXISTS(SELECT 1 FROM users WHERE id::text=$2 AND deleted_at IS NULL) OR EXISTS(SELECT 1 FROM teams WHERE id::text=$3 AND deleted_at IS NULL))`, source.Namespace, source.UserID, source.TeamID).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return nil, cb.ErrDenied
		}
	}
	if binding != nil {
		var approved cb.Source
		if err = credentialRead(ctx, tx, ns, "sources", binding.Source, &approved); err != nil {
			return nil, err
		}
		if !approved.Allows(binding.HumanID, binding.TeamID) {
			return nil, cb.ErrDenied
		}
		if err = credentialSourceAuthority(ctx, tx, p, approved); err != nil {
			return nil, err
		}
		if err = credentialSubjects(ctx, tx, binding.HumanID, binding.AgentID, binding.TeamID); err != nil {
			return nil, err
		}
	}
	var current int64
	err = tx.QueryRowContext(ctx, `SELECT revision FROM credential_metadata WHERE namespace=$1 AND kind=$2 AND name=$3 FOR UPDATE`, ns, kind, name).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if (errors.Is(err, sql.ErrNoRows) && revision != 0) || (err == nil && current != revision) {
		return nil, cb.ErrConflict
	}
	revision++
	if source != nil {
		source.Revision = revision
	} else {
		binding.Revision = revision
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO credential_metadata(namespace,kind,name,revision,data) VALUES($1,$2,$3,$4,$5)
 ON CONFLICT(namespace,kind,name) DO UPDATE SET revision=EXCLUDED.revision,data=EXCLUDED.data,updated_at=now()`, ns, kind, name, revision, []byte(raw)); err != nil {
		return nil, err
	}
	if err = credentialAudit(ctx, tx, credentialActor(p), "credential_"+kind+"_write", ns, name, "success", "", ""); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return raw, nil
}

func (s *Store) DeleteCredential(ctx context.Context, p platformauth.Principal, ns, kind, name string, revision int64) error {
	if kind != "sources" && kind != "bindings" {
		return cb.ErrDenied
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockSessionState(ctx, tx); err != nil {
		return err
	}
	if kind == "sources" {
		err = credentialAdmin(ctx, tx, p)
	} else {
		err = credentialNamespace(ctx, tx, p, ns, true)
	}
	if err != nil {
		return err
	}
	if kind == "sources" {
		var used bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM credential_metadata WHERE namespace=$1 AND kind='bindings' AND data->>'source'=$2)`, ns, name).Scan(&used); err != nil {
			return err
		}
		if used {
			return cb.ErrConflict
		}
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM credential_metadata WHERE namespace=$1 AND kind=$2 AND name=$3 AND revision=$4`, ns, kind, name, revision)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return cb.ErrConflict
	}
	if err = credentialAudit(ctx, tx, credentialActor(p), "credential_"+kind+"_delete", ns, name, "success", "", ""); err != nil {
		return err
	}
	return tx.Commit()
}

func credentialSubjects(ctx context.Context, q credentialQuery, human, agent, team string) error {
	var activeHuman string
	err := q.QueryRowContext(ctx, `SELECT u.id::text FROM users u JOIN team_memberships m ON m.user_id=u.id
 JOIN teams t ON t.id=m.team_id JOIN agents a ON a.team_id=t.id
 WHERE u.id::text=$1 AND u.deleted_at IS NULL AND m.deleted_at IS NULL AND t.deleted_at IS NULL
 AND t.id::text=$3 AND a.id=$2 AND a.status='active' FOR SHARE OF u,m,t,a`, human, agent, team).Scan(&activeHuman)
	if errors.Is(err, sql.ErrNoRows) {
		return cb.ErrDenied
	}
	if err != nil {
		return err
	}
	return nil
}

func credentialAudit(ctx context.Context, tx *sql.Tx, human, action, ns, name, status, agent, team string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO audit_logs(user_id,action,resource,namespace,status,message,source,agent_id,team_id)
 VALUES(NULLIF($1,'')::uuid,$2,$3,$4,$5,'credential metadata only','credential-broker',NULLIF($6,''),NULLIF($7,'')::uuid)`, human, action, name, ns, status, agent, team)
	return err
}

// UseCredentials serializes resolution with account/agent/session revocation
// and metadata writes. The bounded provider read and canonical grant check run
// inside this transaction; a value is returned only after durable usage audit.
func (s *Store) UseCredentials(ctx context.Context, server sessionstate.Server, request cb.ResolutionRequest, resolve func(sessionstate.Session, []cb.Binding, []cb.Source) (cb.Delivery, error)) (out cb.Delivery, err error) {
	if len(request.Bindings) == 0 || len(request.Bindings) > 16 {
		return out, cb.ErrDenied
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if err = lockSessionState(ctx, tx); err != nil {
		return out, err
	}
	audited := false
	human, agent, team := "", "", ""
	defer func() {
		if !audited && err != nil {
			if auditErr := credentialAudit(ctx, tx, human, "credential_access", server.Namespace, "resolution", "denied", agent, team); auditErr != nil {
				out, err = cb.Delivery{}, cb.ErrUnavailable
				return
			}
			if commitErr := tx.Commit(); commitErr != nil {
				out, err = cb.Delivery{}, cb.ErrUnavailable
			}
		}
	}()
	var raw []byte
	if err = tx.QueryRowContext(ctx, `SELECT data FROM sessions WHERE id=$1`, request.Session).Scan(&raw); err != nil {
		return out, cb.ErrDenied
	}
	var session sessionstate.Session
	if err = json.Unmarshal(raw, &session); err != nil {
		return out, err
	}
	b := session.Binding
	human, agent, team = string(b.HumanID), string(b.AgentID), string(b.TeamID)
	expiry, e := time.Parse(time.RFC3339, b.ExpiresAt)
	if e != nil || !expiry.After(time.Now()) || b.Revoked || session.RevokedAt != nil || session.ServerNamespace != server.Namespace || session.ServerName != server.Name || session.ServerUID != server.UID || session.Annotations[sessionstate.AdapterEnrollmentPendingAnnotation] != "" || session.GrantName == "" || session.GrantNamespace == "" {
		return out, cb.ErrDenied
	}
	if err = credentialSubjects(ctx, tx, string(b.HumanID), string(b.AgentID), string(b.TeamID)); err != nil {
		return out, err
	}
	bindings := make([]cb.Binding, 0, len(request.Bindings))
	sources := make([]cb.Source, 0, len(request.Bindings))
	seen := map[string]bool{}
	for _, name := range request.Bindings {
		if seen[name] || !cb.NameValid(name) {
			return out, cb.ErrDenied
		}
		seen[name] = true
		var binding cb.Binding
		if err = credentialRead(ctx, tx, server.Namespace, "bindings", name, &binding); err != nil {
			return out, err
		}
		if !slices.Contains(binding.Tools, request.Tool) {
			continue
		}
		if binding.Server != server.Name || binding.ServerUID != server.UID || !binding.Matches(string(b.HumanID), string(b.AgentID), string(b.TeamID), request.Session, request.Tool) {
			return out, cb.ErrDenied
		}
		var source cb.Source
		if err = credentialRead(ctx, tx, server.Namespace, "sources", binding.Source, &source); err != nil {
			return out, err
		}
		if !source.Allows(string(b.HumanID), string(b.TeamID)) {
			return out, cb.ErrDenied
		}
		bindings = append(bindings, binding)
		sources = append(sources, source)
	}
	out, err = resolve(session, bindings, sources)
	status := "success"
	if err != nil {
		status = "denied"
	}
	for _, binding := range bindings {
		if auditErr := credentialAudit(ctx, tx, string(b.HumanID), "credential_access", server.Namespace, binding.Name, status, string(b.AgentID), string(b.TeamID)); auditErr != nil {
			return cb.Delivery{}, fmt.Errorf("credential audit unavailable")
		}
	}
	if len(bindings) == 0 {
		if auditErr := credentialAudit(ctx, tx, human, "credential_access", server.Namespace, "resolution", status, agent, team); auditErr != nil {
			return cb.Delivery{}, cb.ErrUnavailable
		}
	}
	audited = true
	if commitErr := tx.Commit(); commitErr != nil {
		return cb.Delivery{}, commitErr
	}
	if err != nil {
		return cb.Delivery{}, err
	}
	return out, nil
}
