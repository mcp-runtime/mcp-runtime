package platformstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"mcp-runtime/pkg/sessionstate"
)

// CompleteSessionEnrollment updates metadata under the same lock as revocation
// and deletion. Authorization fields always come from the current stored row.
func (s *Store) CompleteSessionEnrollment(ctx context.Context, namespace, name, uid string) error {
	return s.mutateSessionEnrollment(ctx, namespace, name, uid, "", false)
}

func (s *Store) DeletePendingSession(ctx context.Context, namespace, name, uid, humanID string) error {
	return s.mutateSessionEnrollment(ctx, namespace, name, uid, humanID, true)
}

func (s *Store) mutateSessionEnrollment(ctx context.Context, namespace, name, uid, humanID string, cleanup bool) error {
	if uid == "" || (cleanup && humanID == "") {
		return sessionstate.ErrInvalidSession
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockSessionState(ctx, tx); err != nil {
		return err
	}
	var data []byte
	err = tx.QueryRowContext(ctx, `SELECT data FROM sessions WHERE namespace=$1 AND name=$2`, namespace, name).Scan(&data)
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
	if item.UID != uid {
		return sessionstate.ErrConflict
	}
	if cleanup {
		if string(item.Binding.HumanID) != humanID || item.Annotations[sessionstate.AdapterEnrollmentPendingAnnotation] != "true" {
			return sessionstate.ErrConflict
		}
		if err = deleteSessionTx(ctx, tx, item); err != nil {
			return err
		}
	} else {
		expires, err := time.Parse(time.RFC3339, item.Binding.ExpiresAt)
		if err != nil || !expires.After(time.Now()) || item.Binding.Revoked {
			return sessionstate.ErrConflict
		}
		if item.Binding.AgentID != "" {
			if err = checkSessionAgent(ctx, tx, item); err != nil {
				return err
			}
		}
		if item.Annotations[sessionstate.AdapterEnrollmentPendingAnnotation] == "true" {
			delete(item.Annotations, sessionstate.AdapterEnrollmentPendingAnnotation)
			if err = writeSession(ctx, tx, item); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
