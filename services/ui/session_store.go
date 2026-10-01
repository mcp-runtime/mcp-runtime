package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"
)

const (
	sessionStoreMemory   = "memory"
	sessionStorePostgres = "postgres"
	minSessionKeyLen     = 32
)

// sessionBackend persists UI sessions. The default in-memory backend keeps
// the single-replica behavior (sessions are lost on restart); a shared backend
// lets several UI replicas validate the same session cookie.
type sessionBackend interface {
	// put stores a session until its ExpiresAt. It may also drop expired rows.
	put(ctx context.Context, sess uiSession, now time.Time) error
	// get returns a live session. A missing or expired session is (_, false, nil).
	get(ctx context.Context, id string, now time.Time) (uiSession, bool, error)
	delete(ctx context.Context, id string) error
}

// uiSessionStore issues, looks up and revokes UI sessions on top of a backend.
type uiSessionStore struct {
	backend sessionBackend
	now     func() time.Time
}

func newUISessionStore(now func() time.Time) *uiSessionStore {
	return newUISessionStoreWithBackend(newMemorySessionBackend(), now)
}

func newUISessionStoreWithBackend(backend sessionBackend, now func() time.Time) *uiSessionStore {
	return &uiSessionStore{backend: backend, now: now}
}

func (s *uiSessionStore) createSession(ctx context.Context, session uiSession) (uiSession, error) {
	id, err := randomURLToken(24)
	if err != nil {
		return uiSession{}, err
	}
	session.ID = id
	csrfToken, err := randomURLToken(24)
	if err != nil {
		return uiSession{}, err
	}
	session.CSRFToken = csrfToken
	now := s.now()
	maxExpiry := now.Add(sessionDuration)
	if session.ExpiresAt.IsZero() || session.ExpiresAt.After(maxExpiry) {
		session.ExpiresAt = maxExpiry
	}
	if !session.ExpiresAt.After(now) {
		return uiSession{}, errors.New("session expiry is in the past")
	}
	if err := s.backend.put(ctx, session, now); err != nil {
		return uiSession{}, err
	}
	return session, nil
}

func (s *uiSessionStore) sessionFromRequest(r *http.Request) (uiSession, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return uiSession{}, false
	}
	sessionID := strings.TrimSpace(cookie.Value)
	if sessionID == "" {
		return uiSession{}, false
	}
	return s.getContext(r.Context(), sessionID)
}

func (s *uiSessionStore) get(id string) (uiSession, bool) {
	return s.getContext(context.Background(), id)
}

// getContext fails closed: a backend error is treated as "no session".
func (s *uiSessionStore) getContext(ctx context.Context, id string) (uiSession, bool) {
	sess, ok, err := s.backend.get(ctx, id, s.now())
	if err != nil {
		log.Printf("ui session lookup failed: %v", err)
		return uiSession{}, false
	}
	return sess, ok
}

func (s *uiSessionStore) delete(id string) {
	if err := s.backend.delete(context.Background(), id); err != nil {
		log.Printf("ui session delete failed: %v", err)
	}
}

// memorySessionBackend is the default; sessions are cleared on UI restart and
// are not visible to other replicas.
type memorySessionBackend struct {
	mu       sync.Mutex
	sessions map[string]uiSession
}

func newMemorySessionBackend() *memorySessionBackend {
	return &memorySessionBackend{sessions: map[string]uiSession{}}
}

func (m *memorySessionBackend) put(_ context.Context, sess uiSession, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.purgeExpiredLocked(now)
	m.sessions[sess.ID] = sess
	return nil
}

func (m *memorySessionBackend) get(_ context.Context, id string, now time.Time) (uiSession, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.purgeExpiredLocked(now)
	sess, ok := m.sessions[id]
	return sess, ok, nil
}

func (m *memorySessionBackend) delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, id)
	return nil
}

func (m *memorySessionBackend) purgeExpiredLocked(now time.Time) {
	for id, sess := range m.sessions {
		if !sess.ExpiresAt.After(now) {
			delete(m.sessions, id)
		}
	}
}

// postgresSessionBackend stores sessions in a shared Postgres table. The
// session payload (principal plus upstream credentials) is AES-256-GCM
// encrypted with a key held only by the UI replicas, with the session ID as
// additional authenticated data, so database readers cannot recover upstream
// API keys or move a payload between session IDs.
type postgresSessionBackend struct {
	db   *sql.DB
	aead cipher.AEAD
}

const createSessionTableSQL = `CREATE TABLE IF NOT EXISTS ui_sessions (
	id TEXT PRIMARY KEY,
	expires_at TIMESTAMPTZ NOT NULL,
	payload BYTEA NOT NULL
)`

const createSessionIndexSQL = `CREATE INDEX IF NOT EXISTS ui_sessions_expires_at_idx ON ui_sessions (expires_at)`

func newPostgresSessionBackend(ctx context.Context, db *sql.DB, secret string) (*postgresSessionBackend, error) {
	if len(secret) < minSessionKeyLen {
		return nil, fmt.Errorf("UI_SESSION_ENCRYPTION_KEY must be at least %d characters", minSessionKeyLen)
	}
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if err := ensureSessionSchema(ctx, db); err != nil {
		return nil, err
	}
	return &postgresSessionBackend{db: db, aead: aead}, nil
}

// Two UI replicas can run CREATE TABLE IF NOT EXISTS together. Postgres can
// still raise a unique violation on the type catalog for the loser. Retrying
// lets that replica observe the table the winner created.
func ensureSessionSchema(ctx context.Context, db *sql.DB) error {
	statements := []struct {
		query string
		what  string
	}{
		{createSessionTableSQL, "ui_sessions table"},
		{createSessionIndexSQL, "ui_sessions index"},
	}
	for _, statement := range statements {
		var err error
		for attempt := 0; attempt < 5; attempt++ {
			if _, err = db.ExecContext(ctx, statement.query); err == nil {
				break
			}
			if !concurrentSchemaRace(err) {
				return fmt.Errorf("ensure %s: %w", statement.what, err)
			}
			timer := time.NewTimer(time.Duration(attempt+1) * 50 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return fmt.Errorf("ensure %s: %w", statement.what, ctx.Err())
			case <-timer.C:
			}
		}
		if err != nil {
			return fmt.Errorf("ensure %s: %w", statement.what, err)
		}
	}
	return nil
}

func concurrentSchemaRace(err error) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && string(pqErr.Code) == "23505"
}

func (p *postgresSessionBackend) seal(sess uiSession) ([]byte, error) {
	plain, err := json.Marshal(sess)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, p.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return p.aead.Seal(nonce, nonce, plain, []byte(sess.ID)), nil
}

func (p *postgresSessionBackend) open(id string, blob []byte) (uiSession, error) {
	n := p.aead.NonceSize()
	if len(blob) < n {
		return uiSession{}, errors.New("session payload too short")
	}
	plain, err := p.aead.Open(nil, blob[:n], blob[n:], []byte(id))
	if err != nil {
		return uiSession{}, errors.New("session payload cannot be decrypted")
	}
	var sess uiSession
	if err := json.Unmarshal(plain, &sess); err != nil {
		return uiSession{}, err
	}
	if sess.ID != id {
		return uiSession{}, errors.New("session payload id mismatch")
	}
	return sess, nil
}

func (p *postgresSessionBackend) put(ctx context.Context, sess uiSession, now time.Time) error {
	blob, err := p.seal(sess)
	if err != nil {
		return err
	}
	// Best-effort cleanup; failure must not block login.
	if _, err := p.db.ExecContext(ctx, `DELETE FROM ui_sessions WHERE expires_at <= $1`, now); err != nil {
		log.Printf("ui session purge failed: %v", err)
	}
	_, err = p.db.ExecContext(ctx,
		`INSERT INTO ui_sessions (id, expires_at, payload) VALUES ($1, $2, $3)`,
		sess.ID, sess.ExpiresAt, blob)
	return err
}

func (p *postgresSessionBackend) get(ctx context.Context, id string, now time.Time) (uiSession, bool, error) {
	var blob []byte
	err := p.db.QueryRowContext(ctx,
		`SELECT payload FROM ui_sessions WHERE id = $1 AND expires_at > $2`, id, now).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return uiSession{}, false, nil
	}
	if err != nil {
		return uiSession{}, false, err
	}
	sess, err := p.open(id, blob)
	if err != nil {
		return uiSession{}, false, err
	}
	return sess, true, nil
}

func (p *postgresSessionBackend) delete(ctx context.Context, id string) error {
	_, err := p.db.ExecContext(ctx, `DELETE FROM ui_sessions WHERE id = $1`, id)
	return err
}

// buildSessionBackend selects a backend from UI_SESSION_STORE
// (memory|postgres; default memory).
func buildSessionBackend(ctx context.Context, kind, dsn, key string) (sessionBackend, error) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", sessionStoreMemory:
		return newMemorySessionBackend(), nil
	case sessionStorePostgres:
		if strings.TrimSpace(dsn) == "" {
			return nil, errors.New("UI_SESSION_DATABASE_URL is required when UI_SESSION_STORE=postgres")
		}
		db, err := sql.Open("postgres", dsn)
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(10)
		db.SetConnMaxLifetime(30 * time.Minute)
		pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		backend, err := newPostgresSessionBackend(pctx, db, key)
		if err != nil {
			_ = db.Close()
			return nil, err
		}
		return backend, nil
	default:
		return nil, fmt.Errorf("unsupported UI_SESSION_STORE %q (want memory or postgres)", kind)
	}
}

// configureSessionStore installs the configured backend into the package store.
func configureSessionStore(ctx context.Context) error {
	backend, err := buildSessionBackend(ctx,
		os.Getenv("UI_SESSION_STORE"),
		os.Getenv("UI_SESSION_DATABASE_URL"),
		os.Getenv("UI_SESSION_ENCRYPTION_KEY"))
	if err != nil {
		return err
	}
	sessions.backend = backend
	return nil
}
