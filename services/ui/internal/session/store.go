package session

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
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

// Backend persists UI sessions. The default in-memory backend keeps
// the single-replica behavior (sessions are lost on restart); a shared backend
// lets several UI replicas validate the same session cookie.
type Backend interface {
	// Put stores a session until its ExpiresAt. It may also drop expired rows.
	Put(ctx context.Context, sess Session, now time.Time) error
	// Get returns a live session. A missing or expired session is (_, false, nil).
	Get(ctx context.Context, id string, now time.Time) (Session, bool, error)
	Delete(ctx context.Context, id string) error
}

// Store issues, looks up and revokes UI sessions on top of a backend.
type Store struct {
	backend Backend
	now     func() time.Time
}

func NewStore(now func() time.Time) *Store {
	return NewStoreWithBackend(newMemorySessionBackend(), now)
}

func NewStoreWithBackend(backend Backend, now func() time.Time) *Store {
	return &Store{backend: backend, now: now}
}

func (s *Store) Create(ctx context.Context, session Session) (Session, error) {
	id, err := randomURLToken(24)
	if err != nil {
		return Session{}, err
	}
	session.ID = id
	csrfToken, err := randomURLToken(24)
	if err != nil {
		return Session{}, err
	}
	session.CSRFToken = csrfToken
	now := s.now()
	maxExpiry := now.Add(Duration)
	if session.ExpiresAt.IsZero() || session.ExpiresAt.After(maxExpiry) {
		session.ExpiresAt = maxExpiry
	}
	if !session.ExpiresAt.After(now) {
		return Session{}, errors.New("session expiry is in the past")
	}
	if err := s.backend.Put(ctx, session, now); err != nil {
		return Session{}, err
	}
	return session, nil
}

func (s *Store) FromRequest(r *http.Request) (Session, bool) {
	cookie, err := r.Cookie(CookieName)
	if err != nil {
		return Session{}, false
	}
	sessionID := strings.TrimSpace(cookie.Value)
	if sessionID == "" {
		return Session{}, false
	}
	return s.GetContext(r.Context(), sessionID)
}

func (s *Store) Get(id string) (Session, bool) {
	return s.GetContext(context.Background(), id)
}

// GetContext fails closed: a backend error is treated as "no session".
func (s *Store) GetContext(ctx context.Context, id string) (Session, bool) {
	sess, ok, err := s.backend.Get(ctx, id, s.now())
	if err != nil {
		log.Printf("ui session lookup failed: %v", err)
		return Session{}, false
	}
	return sess, ok
}

func (s *Store) Delete(id string) {
	if err := s.backend.Delete(context.Background(), id); err != nil {
		log.Printf("ui session delete failed: %v", err)
	}
}

// memorySessionBackend is the default; sessions are cleared on UI restart and
// are not visible to other replicas.
type memorySessionBackend struct {
	mu       sync.Mutex
	sessions map[string]Session
}

func newMemorySessionBackend() *memorySessionBackend {
	return &memorySessionBackend{sessions: map[string]Session{}}
}

func (m *memorySessionBackend) Put(_ context.Context, sess Session, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.purgeExpiredLocked(now)
	m.sessions[sess.ID] = sess
	return nil
}

func (m *memorySessionBackend) Get(_ context.Context, id string, now time.Time) (Session, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.purgeExpiredLocked(now)
	sess, ok := m.sessions[id]
	return sess, ok, nil
}

func (m *memorySessionBackend) Delete(_ context.Context, id string) error {
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
		announced := false
		schemaRetries := 0
		for {
			if _, err = db.ExecContext(ctx, statement.query); err == nil {
				break
			}
			switch {
			case transientSessionConnect(err):
				if !announced {
					log.Printf("session store waiting for postgres: %v", err)
					announced = true
				}
			case concurrentSchemaRace(err) && schemaRetries < 4:
				schemaRetries++
			default:
				return fmt.Errorf("ensure %s: %w", statement.what, err)
			}
			delay := 50 * time.Millisecond
			if transientSessionConnect(err) {
				delay = 500 * time.Millisecond
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return fmt.Errorf("ensure %s: %w", statement.what, err)
			case <-timer.C:
			}
		}
	}
	return nil
}

// A headless Postgres Service has no DNS name until its pod is ready.
// Exiting on that lookup makes both UI replicas crash and trips the setup
// smoke check. Wait out the caller's deadline instead.
func transientSessionConnect(err error) bool {
	if err == nil {
		return false
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	msg := err.Error()
	for _, needle := range []string{
		"no such host",
		"connection refused",
		"connection reset",
		"server misbehaving",
		"i/o timeout",
		"network is unreachable",
		"temporary failure in name resolution",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

func concurrentSchemaRace(err error) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && string(pqErr.Code) == "23505"
}

func (p *postgresSessionBackend) seal(sess Session) ([]byte, error) {
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

func (p *postgresSessionBackend) open(id string, blob []byte) (Session, error) {
	n := p.aead.NonceSize()
	if len(blob) < n {
		return Session{}, errors.New("session payload too short")
	}
	plain, err := p.aead.Open(nil, blob[:n], blob[n:], []byte(id))
	if err != nil {
		return Session{}, errors.New("session payload cannot be decrypted")
	}
	var sess Session
	if err := json.Unmarshal(plain, &sess); err != nil {
		return Session{}, err
	}
	if sess.ID != id {
		return Session{}, errors.New("session payload id mismatch")
	}
	return sess, nil
}

func (p *postgresSessionBackend) Put(ctx context.Context, sess Session, now time.Time) error {
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

func (p *postgresSessionBackend) Get(ctx context.Context, id string, now time.Time) (Session, bool, error) {
	var blob []byte
	err := p.db.QueryRowContext(ctx,
		`SELECT payload FROM ui_sessions WHERE id = $1 AND expires_at > $2`, id, now).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, err
	}
	sess, err := p.open(id, blob)
	if err != nil {
		return Session{}, false, err
	}
	return sess, true, nil
}

func (p *postgresSessionBackend) Delete(ctx context.Context, id string) error {
	_, err := p.db.ExecContext(ctx, `DELETE FROM ui_sessions WHERE id = $1`, id)
	return err
}

// NewBackend selects a backend from UI_SESSION_STORE
// (memory|postgres; default memory).
func NewBackend(ctx context.Context, kind, dsn, key string) (Backend, error) {
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
		pctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
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

func randomURLToken(rawBytes int) (string, error) {
	b := make([]byte, rawBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
