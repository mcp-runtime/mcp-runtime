package platformstore

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"strings"

	"github.com/google/uuid"
	"mcp-runtime/pkg/platform"
	"mcp-runtime/pkg/sessionstate"
)

var (
	ErrSelfDeactivation  = errors.New("cannot deactivate the signed-in administrator")
	ErrLastAdministrator = errors.New("cannot deactivate the last active administrator; create another administrator first")
	ErrLastTeamOwner     = errors.New("cannot deactivate a team's last active owner; transfer team ownership first")
	ErrAccountInactive   = errors.New("platform account is deactivated")
	ErrActorInactive     = errors.New("administrator account is no longer active")
)

// DeactivateUser irreversibly disables an account, preserving its identity and
// audit tombstone. Session revocations use the normal versioned change log.
// Lock order is session state, users, memberships, then individual session rows.
func (s *Store) DeactivateUser(ctx context.Context, userID, actorID string, event AuditEvent) (platform.UserDeactivationResult, error) {
	userID = strings.TrimSpace(userID)
	result := platform.UserDeactivationResult{UserID: userID, Status: "deactivated"}
	parsed, err := uuid.Parse(userID)
	if err != nil {
		return result, UserInputError("valid user ID required")
	}
	userID = parsed.String()
	result.UserID = userID
	actorID = strings.TrimSpace(actorID)
	if parsedActor, err := uuid.Parse(actorID); err == nil {
		actorID = parsedActor.String()
	}
	if actorID != "" && actorID == userID {
		return result, ErrSelfDeactivation
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if err = lockSessionState(ctx, tx); err != nil {
		return result, err
	}
	// These short-lived table locks serialize ownership/role changes with the
	// authority checks below, including writers outside this lifecycle path.
	if _, err = tx.ExecContext(ctx, `LOCK TABLE users IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, `LOCK TABLE team_memberships IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return result, err
	}
	if actorID != "" {
		var active bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND role='admin' AND deleted_at IS NULL)`, actorID).Scan(&active); err != nil {
			return result, err
		}
		if !active {
			return result, ErrActorInactive
		}
	}
	var role string
	var deleted sql.NullTime
	if err = tx.QueryRowContext(ctx, `SELECT role,deleted_at FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&role, &deleted); err != nil {
		return result, err
	}
	result.AlreadyDeactivated = deleted.Valid
	if !deleted.Valid {
		if role == RoleAdmin {
			var others int64
			if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role='admin' AND deleted_at IS NULL AND id<>$1`, userID).Scan(&others); err != nil {
				return result, err
			}
			if others == 0 {
				return result, ErrLastAdministrator
			}
		}
		var stranded bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS (
 SELECT 1 FROM team_memberships mine JOIN teams t ON t.id=mine.team_id
 WHERE mine.user_id=$1 AND mine.role='owner' AND mine.deleted_at IS NULL AND t.deleted_at IS NULL
 AND NOT EXISTS (SELECT 1 FROM team_memberships other JOIN users u ON u.id=other.user_id
 WHERE other.team_id=mine.team_id AND other.user_id<>$1 AND other.role='owner'
 AND other.deleted_at IS NULL AND u.deleted_at IS NULL))`, userID).Scan(&stranded); err != nil {
			return result, err
		}
		if stranded {
			return result, ErrLastTeamOwner
		}
		if _, err = tx.ExecContext(ctx, `UPDATE users SET deleted_at=now() WHERE id=$1 AND deleted_at IS NULL`, userID); err != nil {
			return result, err
		}
	}
	updates := []struct {
		query string
		count *int64
	}{
		{`UPDATE api_keys SET revoked=true,revoked_at=COALESCE(revoked_at,now()) WHERE user_id=$1 AND NOT revoked`, &result.RevokedAPIKeys},
		{`UPDATE registry_credentials SET revoked=true,revoked_at=COALESCE(revoked_at,now()) WHERE user_id=$1 AND NOT revoked`, &result.RevokedRegistryCredentials},
		{`UPDATE refresh_tokens SET revoked=true WHERE user_id=$1 AND NOT revoked`, &result.RevokedRefreshTokens},
		{`UPDATE team_memberships SET deleted_at=now() WHERE user_id=$1 AND deleted_at IS NULL`, &result.RemovedMemberships},
	}
	for _, update := range updates {
		row, updateErr := tx.ExecContext(ctx, update.query, userID)
		if updateErr != nil {
			return result, updateErr
		}
		*update.count, err = row.RowsAffected()
		if err != nil {
			return result, err
		}
	}
	result.RevokedSessions, err = revokeSessionsTx(ctx, tx, sessionstate.Filter{HumanID: userID}, "user_deactivated")
	if err != nil {
		return result, err
	}
	// Audit insertion shares the transaction: success cannot be reported without
	// a durable record. Error details and user-supplied bodies are not persisted.
	actorIP := event.ActorIP
	if net.ParseIP(actorIP) == nil {
		actorIP = ""
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_logs(user_id,action,resource,status,actor_ip,source,auth_identity)
 VALUES(NULLIF($1,'')::uuid,'user_deactivate',$2,'success',NULLIF($3,''),$4,$5)`, actorID, userID, actorIP, event.Source, event.AuthIdentity); err != nil {
		return result, err
	}
	return result, tx.Commit()
}

// checkSessionHuman runs while holding the same state lock as deactivation.
// External human identifiers remain supported; known deactivated accounts may
// never create, rewrite or un-revoke a usable session through a stale request.
func checkSessionHuman(ctx context.Context, tx *sql.Tx, item sessionstate.Session) error {
	if item.Binding.HumanID == "" {
		return nil
	}
	var inactive bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id::text=$1 AND deleted_at IS NOT NULL)`, string(item.Binding.HumanID)).Scan(&inactive); err != nil {
		return err
	}
	if inactive {
		return errors.Join(ErrAccountInactive, sessionstate.ErrInactiveHuman)
	}
	return nil
}
