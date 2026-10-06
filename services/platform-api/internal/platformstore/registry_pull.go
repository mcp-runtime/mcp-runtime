package platformstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"mcp-runtime/pkg/registryauth"
)

type RegistryPullCredential struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	Password  string    `json:"password,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (s *Store) CreateRegistryPullCredential(ctx context.Context, scope registryauth.PullScope) (RegistryPullCredential, error) {
	raw, err := randomURLToken(32)
	if err != nil {
		return RegistryPullCredential{}, err
	}
	record := RegistryPullCredential{ID: "rp_" + uuid.NewString(), Username: "mcp-pull-" + scope.Namespace, Password: "mcpp_" + raw, ExpiresAt: time.Now().Add(90 * 24 * time.Hour).UTC()}
	encoded, err := json.Marshal(scope)
	if err != nil {
		return RegistryPullCredential{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO registry_pull_credentials (id,key_hash,namespace,scope,expires_at) VALUES ($1,$2,$3,$4,$5)`, record.ID, hashAPIKey(record.Password), scope.Namespace, string(encoded), record.ExpiresAt)
	if err != nil {
		return RegistryPullCredential{}, err
	}
	return record, nil
}

func (s *Store) RevokeRegistryPullCredential(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE registry_pull_credentials SET revoked=true WHERE id=$1`, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) AuthenticateRegistryPullCredential(ctx context.Context, username, password string) (registryauth.PullScope, bool, error) {
	var scope registryauth.PullScope
	if s == nil || s.db == nil {
		return scope, false, errors.New("registry credential store unavailable")
	}
	if !strings.HasPrefix(password, "mcpp_") {
		return scope, false, nil
	}
	var namespace string
	var encoded []byte
	err := s.db.QueryRowContext(ctx, `SELECT namespace,scope FROM registry_pull_credentials WHERE key_hash=$1 AND revoked=false AND expires_at>now()`, hashAPIKey(password)).Scan(&namespace, &encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return scope, false, nil
	}
	if err != nil {
		return scope, false, err
	}
	if username != "mcp-pull-"+namespace {
		return scope, false, nil
	}
	if err := json.Unmarshal(encoded, &scope); err != nil {
		return scope, false, err
	}
	if scope.Namespace != namespace {
		return scope, false, errors.New("registry credential scope mismatch")
	}
	return scope, true, nil
}
