package main

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeSessionDB is an in-process stand-in for the ui_sessions table. It is a
// database/sql driver that understands exactly the statements the Postgres
// session backend issues.
type fakeSessionDB struct {
	mu      sync.Mutex
	rows    map[string]fakeSessionRow
	failAll bool
}

type fakeSessionRow struct {
	expires time.Time
	payload []byte
}

var fakeDriverSeq atomic.Int64

func openFakeSessionDB(t *testing.T) (*sql.DB, *fakeSessionDB) {
	t.Helper()
	fake := &fakeSessionDB{rows: map[string]fakeSessionRow{}}
	name := fmt.Sprintf("fake-session-db-%d", fakeDriverSeq.Add(1))
	sql.Register(name, fakeDriver{fake})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, fake
}

type fakeDriver struct{ db *fakeSessionDB }

func (d fakeDriver) Open(string) (driver.Conn, error) { return fakeConn(d), nil }

type fakeConn struct{ db *fakeSessionDB }

func (c fakeConn) Prepare(q string) (driver.Stmt, error) { return &fakeStmt{c.db, q}, nil }
func (c fakeConn) Close() error                          { return nil }
func (c fakeConn) Begin() (driver.Tx, error)             { return nil, errors.New("tx unsupported") }

type fakeStmt struct {
	db *fakeSessionDB
	q  string
}

func (s *fakeStmt) Close() error  { return nil }
func (s *fakeStmt) NumInput() int { return -1 }

func (s *fakeStmt) Exec(args []driver.Value) (driver.Result, error) {
	s.db.mu.Lock()
	defer s.db.mu.Unlock()
	if s.db.failAll {
		return nil, errors.New("database unavailable")
	}
	switch {
	case strings.HasPrefix(s.q, "CREATE TABLE"), strings.HasPrefix(s.q, "CREATE INDEX"):
		return driver.RowsAffected(0), nil
	case strings.HasPrefix(s.q, "DELETE FROM ui_sessions WHERE expires_at"):
		now := args[0].(time.Time)
		var n int64
		for id, row := range s.db.rows {
			if !row.expires.After(now) {
				delete(s.db.rows, id)
				n++
			}
		}
		return driver.RowsAffected(n), nil
	case strings.HasPrefix(s.q, "DELETE FROM ui_sessions WHERE id"):
		delete(s.db.rows, args[0].(string))
		return driver.RowsAffected(1), nil
	case strings.HasPrefix(s.q, "INSERT INTO ui_sessions"):
		id := args[0].(string)
		if _, dup := s.db.rows[id]; dup {
			return nil, errors.New("duplicate key")
		}
		s.db.rows[id] = fakeSessionRow{expires: args[1].(time.Time), payload: append([]byte(nil), args[2].([]byte)...)}
		return driver.RowsAffected(1), nil
	}
	return nil, fmt.Errorf("unexpected exec %q", s.q)
}

func (s *fakeStmt) Query(args []driver.Value) (driver.Rows, error) {
	s.db.mu.Lock()
	defer s.db.mu.Unlock()
	if s.db.failAll {
		return nil, errors.New("database unavailable")
	}
	if !strings.HasPrefix(s.q, "SELECT payload FROM ui_sessions") {
		return nil, fmt.Errorf("unexpected query %q", s.q)
	}
	row, ok := s.db.rows[args[0].(string)]
	if !ok || !row.expires.After(args[1].(time.Time)) {
		return &fakeRows{}, nil
	}
	return &fakeRows{data: [][]driver.Value{{row.payload}}}, nil
}

type fakeRows struct {
	data [][]driver.Value
	i    int
}

func (r *fakeRows) Columns() []string { return []string{"payload"} }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.i >= len(r.data) {
		return io.EOF
	}
	copy(dest, r.data[r.i])
	r.i++
	return nil
}

const testSessionKey = "0123456789abcdef0123456789abcdef"

func newSharedStores(t *testing.T, now func() time.Time) (a, b *uiSessionStore, fake *fakeSessionDB) {
	t.Helper()
	db, fake := openFakeSessionDB(t)
	mk := func() *uiSessionStore {
		backend, err := newPostgresSessionBackend(context.Background(), db, testSessionKey)
		if err != nil {
			t.Fatal(err)
		}
		return newUISessionStoreWithBackend(backend, now)
	}
	return mk(), mk(), fake
}

func cookieRequest(sess uiSession) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/auth/status", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	return r
}

func TestSharedSessionVisibleAcrossReplicas(t *testing.T) {
	now := time.Now()
	podA, podB, _ := newSharedStores(t, func() time.Time { return now })

	sess, err := podA.createSession(context.Background(), uiSession{
		Principal:      sessionPrincipal{Role: "admin", Subject: "alice"},
		UpstreamAPIKey: "upstream-secret",
	})
	if err != nil {
		t.Fatal(err)
	}

	// status and admin-check served by a different replica than login.
	rec := httptest.NewRecorder()
	handleStatus(podB).ServeHTTP(rec, cookieRequest(sess))
	if !strings.Contains(rec.Body.String(), `"authenticated":true`) || !strings.Contains(rec.Body.String(), sess.CSRFToken) {
		t.Fatalf("replica B status = %s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	handleAdminCheck(podB, nil, nil, false).ServeHTTP(rec, cookieRequest(sess))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("replica B admin-check = %d", rec.Code)
	}
	got, ok := podB.get(sess.ID)
	if !ok || got.UpstreamAPIKey != "upstream-secret" || got.Principal.Subject != "alice" {
		t.Fatalf("replica B session = %+v ok=%v", got, ok)
	}

	// logout on replica B revokes it everywhere.
	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	handleLogout(podB).ServeHTTP(httptest.NewRecorder(), req)
	if _, ok := podA.get(sess.ID); ok {
		t.Fatal("session still valid on replica A after logout on B")
	}
}

func TestSharedSessionExpiryAndPurge(t *testing.T) {
	now := time.Now()
	clock := &now
	podA, podB, fake := newSharedStores(t, func() time.Time { return *clock })
	sess, err := podA.createSession(context.Background(), uiSession{ExpiresAt: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := podB.get(sess.ID); !ok {
		t.Fatal("expected live session")
	}
	later := now.Add(2 * time.Minute)
	clock = &later
	if _, ok := podB.get(sess.ID); ok {
		t.Fatal("expired session accepted")
	}
	if _, err := podA.createSession(context.Background(), uiSession{ExpiresAt: later.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	n := len(fake.rows)
	fake.mu.Unlock()
	if n != 1 {
		t.Fatalf("expired row not purged on create, rows=%d", n)
	}
}

func TestSharedSessionPayloadEncryptedAndBoundToID(t *testing.T) {
	now := time.Now()
	podA, podB, fake := newSharedStores(t, func() time.Time { return now })
	s1, err := podA.createSession(context.Background(), uiSession{UpstreamAPIKey: "upstream-secret", UpstreamAuthHeader: "Bearer tok"})
	if err != nil {
		t.Fatal(err)
	}
	s2, err := podA.createSession(context.Background(), uiSession{})
	if err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	for _, row := range fake.rows {
		if bytes.Contains(row.payload, []byte("upstream-secret")) || bytes.Contains(row.payload, []byte("Bearer tok")) {
			t.Fatal("payload stored in plaintext")
		}
	}
	// Move s1's payload under s2's ID: must be rejected.
	row1 := fake.rows[s1.ID]
	row2 := fake.rows[s2.ID]
	row2.payload = row1.payload
	fake.rows[s2.ID] = row2
	fake.mu.Unlock()
	if _, ok := podB.get(s2.ID); ok {
		t.Fatal("swapped payload accepted")
	}
}

func TestSharedSessionWrongKeyRejected(t *testing.T) {
	now := time.Now()
	db, _ := openFakeSessionDB(t)
	good, err := newPostgresSessionBackend(context.Background(), db, testSessionKey)
	if err != nil {
		t.Fatal(err)
	}
	bad, err := newPostgresSessionBackend(context.Background(), db, strings.Repeat("z", 40))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := newUISessionStoreWithBackend(good, func() time.Time { return now }).createSession(context.Background(), uiSession{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := newUISessionStoreWithBackend(bad, func() time.Time { return now }).get(sess.ID); ok {
		t.Fatal("session decrypted with wrong key")
	}
}

func TestSharedSessionBackendOutageFailsClosed(t *testing.T) {
	now := time.Now()
	podA, podB, fake := newSharedStores(t, func() time.Time { return now })
	sess, err := podA.createSession(context.Background(), uiSession{Principal: sessionPrincipal{Role: "admin"}})
	if err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.failAll = true
	fake.mu.Unlock()
	if _, ok := podB.get(sess.ID); ok {
		t.Fatal("session accepted while backend down")
	}
	if _, err := podA.createSession(context.Background(), uiSession{}); err == nil {
		t.Fatal("create should fail while backend down")
	}
}

func TestMemoryBackendIsPerStore(t *testing.T) {
	now := time.Now()
	a := newUISessionStore(func() time.Time { return now })
	b := newUISessionStore(func() time.Time { return now })
	sess, err := a.createSession(context.Background(), uiSession{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := a.get(sess.ID); !ok {
		t.Fatal("memory store lost session")
	}
	if _, ok := b.get(sess.ID); ok {
		t.Fatal("memory stores must not share sessions")
	}
}

func TestBuildSessionBackendConfig(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"", "memory", " MEMORY "} {
		b, err := buildSessionBackend(ctx, kind, "", "")
		if err != nil {
			t.Fatalf("%q: %v", kind, err)
		}
		if _, ok := b.(*memorySessionBackend); !ok {
			t.Fatalf("%q: backend = %T", kind, b)
		}
	}
	if _, err := buildSessionBackend(ctx, "postgres", "", testSessionKey); err == nil {
		t.Fatal("postgres without DSN must fail")
	}
	if _, err := buildSessionBackend(ctx, "redis", "", ""); err == nil {
		t.Fatal("unknown backend must fail")
	}
	db, _ := openFakeSessionDB(t)
	if _, err := newPostgresSessionBackend(ctx, db, "short"); err == nil {
		t.Fatal("short encryption key must fail")
	}
}
