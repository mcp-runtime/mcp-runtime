package platformstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

func hashAPIKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func keyPrefix(raw string) string {
	if len(raw) <= 8 {
		return raw
	}
	return raw[:8]
}

func generateAPIKeyValue() (string, error) {
	token, err := randomURLToken(32)
	if err != nil {
		return "", err
	}
	return "mcpu_" + token, nil
}

func randomURLToken(rawBytes int) (string, error) {
	b := make([]byte, rawBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ListUserAPIKeys returns non-secret API key metadata for a user.
func (s *Store) ListUserAPIKeys(ctx context.Context, userID string) ([]APIKeySummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, prefix, created_at, revoked, revoked_at FROM api_keys WHERE user_id = $1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIKeySummary
	for rows.Next() {
		var rec APIKeySummary
		if err := rows.Scan(&rec.ID, &rec.Name, &rec.Prefix, &rec.CreatedAt, &rec.Revoked, &rec.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// CreateUserAPIKey creates a user API key and returns its one-time raw value.
func (s *Store) CreateUserAPIKey(ctx context.Context, userID, name string) (APIKeySummary, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return APIKeySummary{}, "", errors.New("name required")
	}
	rawKey, err := generateAPIKeyValue()
	if err != nil {
		return APIKeySummary{}, "", err
	}
	rec := APIKeySummary{ID: "uk_" + uuid.NewString(), Name: name, Prefix: keyPrefix(rawKey), CreatedAt: time.Now().UTC()}
	insert, err := s.db.ExecContext(ctx, `WITH account AS (SELECT id FROM users WHERE id=$3 AND deleted_at IS NULL FOR SHARE)
 INSERT INTO api_keys (id,key_hash,user_id,name,prefix,created_at,revoked) SELECT $1,$2,id,$4,$5,$6,false FROM account`,
		rec.ID, hashAPIKey(rawKey), userID, rec.Name, rec.Prefix, rec.CreatedAt)
	if err != nil {
		return APIKeySummary{}, "", err
	}
	if count, err := insert.RowsAffected(); err != nil {
		return APIKeySummary{}, "", err
	} else if count == 0 {
		return APIKeySummary{}, "", ErrAccountInactive
	}
	return rec, rawKey, nil
}

// RevokeUserAPIKey marks a user API key as revoked.
func (s *Store) RevokeUserAPIKey(ctx context.Context, userID, id string) (APIKeySummary, error) {
	var rec APIKeySummary
	err := s.db.QueryRowContext(ctx, `
UPDATE api_keys
SET revoked = true, revoked_at = COALESCE(revoked_at, now())
WHERE user_id = $1 AND id = $2
RETURNING id, name, prefix, created_at, revoked, revoked_at`, userID, id).
		Scan(&rec.ID, &rec.Name, &rec.Prefix, &rec.CreatedAt, &rec.Revoked, &rec.RevokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return APIKeySummary{}, sql.ErrNoRows
	}
	if err != nil {
		return APIKeySummary{}, err
	}
	return rec, nil
}

// ListRegistryCredentials returns non-secret registry credential metadata for a user.
func (s *Store) ListRegistryCredentials(ctx context.Context, userID string) ([]APIKeySummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, prefix, created_at, revoked, revoked_at FROM registry_credentials WHERE user_id = $1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIKeySummary
	for rows.Next() {
		var rec APIKeySummary
		if err := rows.Scan(&rec.ID, &rec.Name, &rec.Prefix, &rec.CreatedAt, &rec.Revoked, &rec.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// CreateRegistryCredential creates a registry credential and returns its one-time raw value.
func (s *Store) CreateRegistryCredential(ctx context.Context, userID, name string) (APIKeySummary, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return APIKeySummary{}, "", errors.New("name required")
	}
	rawKey, err := randomURLToken(32)
	if err != nil {
		return APIKeySummary{}, "", err
	}
	rawKey = "mcpr_" + rawKey
	rec := APIKeySummary{ID: "rk_" + uuid.NewString(), Name: name, Prefix: keyPrefix(rawKey), CreatedAt: time.Now().UTC()}
	insert, err := s.db.ExecContext(ctx, `WITH account AS (SELECT id FROM users WHERE id=$3 AND deleted_at IS NULL FOR SHARE)
 INSERT INTO registry_credentials (id,key_hash,user_id,name,prefix,created_at,revoked) SELECT $1,$2,id,$4,$5,$6,false FROM account`,
		rec.ID, hashAPIKey(rawKey), userID, rec.Name, rec.Prefix, rec.CreatedAt)
	if err != nil {
		return APIKeySummary{}, "", err
	}
	if count, err := insert.RowsAffected(); err != nil {
		return APIKeySummary{}, "", err
	} else if count == 0 {
		return APIKeySummary{}, "", ErrAccountInactive
	}
	return rec, rawKey, nil
}

// RevokeRegistryCredential marks a registry credential as revoked.
func (s *Store) RevokeRegistryCredential(ctx context.Context, userID, id string) (APIKeySummary, error) {
	var rec APIKeySummary
	err := s.db.QueryRowContext(ctx, `
UPDATE registry_credentials
SET revoked = true, revoked_at = COALESCE(revoked_at, now())
WHERE user_id = $1 AND id = $2
RETURNING id, name, prefix, created_at, revoked, revoked_at`, userID, id).
		Scan(&rec.ID, &rec.Name, &rec.Prefix, &rec.CreatedAt, &rec.Revoked, &rec.RevokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return APIKeySummary{}, sql.ErrNoRows
	}
	if err != nil {
		return APIKeySummary{}, err
	}
	return rec, nil
}

// AuthenticateRegistryCredential validates Docker registry basic-auth credentials.
// It supports both registry-specific API keys (mcpr_ prefix) and regular
// platform passwords.
func (s *Store) AuthenticateRegistryCredential(ctx context.Context, username, password string) (Principal, bool, error) {
	username = strings.TrimSpace(username)
	password = strings.TrimSpace(password)
	if username == "" || password == "" {
		return Principal{}, false, nil
	}

	// 1. Try registry-specific API keys (mcpr_ prefix)
	if strings.HasPrefix(password, "mcpr_") {
		targetHash := hashAPIKey(password)
		var keyID, userID string
		err := s.db.QueryRowContext(ctx, `
SELECT rc.id, rc.user_id
FROM registry_credentials rc
JOIN users u ON u.id = rc.user_id AND u.deleted_at IS NULL
WHERE rc.key_hash = $1 AND rc.revoked = false`, targetHash).
			Scan(&keyID, &userID)
		if err == nil {
			p, err := s.PrincipalForUserID(ctx, userID)
			if err != nil {
				return Principal{}, false, err
			}
			if !registryCredentialUsernameMatches(p, username) {
				return Principal{}, false, nil
			}
			_, _ = s.db.ExecContext(ctx, `UPDATE registry_credentials SET last_used_at = now() WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '5 minutes')`, keyID)
			p.AuthType = "registry_basic"
			p.APIKeyID = keyID
			return p, true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Principal{}, false, err
		}
	}

	// 2. Fallback to regular platform password authentication
	u, ok, err := s.AuthenticatePassword(ctx, username, password)
	if err != nil || !ok {
		return Principal{}, false, err
	}

	p, err := s.PrincipalForUserID(ctx, u.ID)
	if err != nil {
		return Principal{}, false, err
	}
	p.AuthType = "registry_password"
	return p, true, nil
}

func registryCredentialUsernameMatches(p Principal, username string) bool {
	username = strings.TrimSpace(username)
	if username == "" {
		return false
	}
	if p.HasNamespace(username) {
		return true
	}
	if strings.TrimSpace(p.Subject) == username {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(p.Email), username) {
		return true
	}
	return false
}
