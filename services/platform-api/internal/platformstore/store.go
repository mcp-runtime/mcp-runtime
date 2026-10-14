package platformstore

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

// Open connects to Postgres, applies schema migrations, and returns a Store.
func Open(ctx context.Context, dsn string, jwtSecret []byte) (*Store, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(intEnvOrDefault("PLATFORM_DB_MAX_CONNS", defaultDBMaxConns))
	db.SetMaxIdleConns(intEnvOrDefault("PLATFORM_DB_MAX_IDLE_CONNS", defaultDBMaxIdle))
	db.SetConnMaxLifetime(30 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	s := &Store{db: db, jwtSecret: jwtSecret}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.initializeSessions(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// NewForTest returns an in-memory Store shell for tests that stub persistence.
func NewForTest(jwtSecret []byte) *Store {
	return &Store{jwtSecret: jwtSecret}
}

func (s *Store) migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, platformSchemaSQL)
	return err
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Ping checks Postgres connectivity for readiness probes.
func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("platform store not configured")
	}
	return s.db.PingContext(ctx)
}

func intEnvOrDefault(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}
