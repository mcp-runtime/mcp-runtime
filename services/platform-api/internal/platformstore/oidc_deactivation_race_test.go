package platformstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lib/pq"
)

// Pause the real Postgres tombstone result before Scan returns. This exposes
// the former gap between the inactive check and the later identity lookup,
// without adding a test hook to product code or substituting database behavior.
type oidcBarrierConnector struct {
	driver.Connector
	ready   chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *oidcBarrierConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &oidcBarrierConn{Conn: conn, barrier: c}, nil
}

type oidcBarrierConn struct {
	driver.Conn
	barrier *oidcBarrierConnector
}

func (c *oidcBarrierConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if conn, ok := c.Conn.(driver.ConnBeginTx); ok {
		return conn.BeginTx(ctx, opts)
	}
	return c.Conn.Begin()
}
func (c *oidcBarrierConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}
func (c *oidcBarrierConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
	if err == nil && strings.Contains(query, "SELECT EXISTS") && strings.Contains(query, "auth_identities") && strings.Contains(query, "u.deleted_at IS NOT NULL") {
		return &oidcBarrierRows{Rows: rows, ctx: ctx, barrier: c.barrier}, nil
	}
	return rows, err
}

type oidcBarrierRows struct {
	driver.Rows
	ctx     context.Context
	barrier *oidcBarrierConnector
}

func (r *oidcBarrierRows) Next(values []driver.Value) error {
	r.barrier.once.Do(func() { close(r.barrier.ready) })
	select {
	case <-r.barrier.release:
		return r.Rows.Next(values)
	case <-r.ctx.Done():
		return r.ctx.Err()
	}
}

func TestPostgresOIDCChangedEmailCannotRaceAccountDeactivation(t *testing.T) {
	s, dsn := lifecyclePostgresStoreWithDSN(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	admin, err := s.CreatePasswordUser(ctx, "admin@example.test", "password-for-test", "admin")
	if err != nil {
		t.Fatal(err)
	}
	original, err := s.EnsureOIDCUser(ctx, "oidc:race", "subject", "original@example.test", "user")
	if err != nil {
		t.Fatal(err)
	}
	connector, err := pq.NewConnector(dsn)
	if err != nil {
		t.Fatal(err)
	}
	barrier := &oidcBarrierConnector{Connector: connector, ready: make(chan struct{}), release: make(chan struct{})}
	raceDB := sql.OpenDB(barrier)
	t.Cleanup(func() { raceDB.Close() })
	oidc := &Store{db: raceDB}
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(barrier.release) }) }
	defer unblock()
	type oidcResult struct {
		user User
		err  error
	}
	oidcDone := make(chan oidcResult, 1)
	go func() {
		user, err := oidc.EnsureOIDCUser(ctx, "oidc:race", "subject", "changed@example.test", "user")
		oidcDone <- oidcResult{user, err}
	}()
	select {
	case <-barrier.ready:
	case <-ctx.Done():
		t.Fatal("OIDC did not reach the tombstone-read barrier")
	}
	deactDB, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { deactDB.Close() })
	deactDB.SetMaxOpenConns(1)
	var deactivationPID int
	if err = deactDB.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&deactivationPID); err != nil {
		t.Fatal(err)
	}
	deactivator := &Store{db: deactDB}
	deactDone := make(chan error, 1)
	go func() {
		_, err := deactivator.DeactivateUser(ctx, original.ID, admin.ID, AuditEvent{})
		deactDone <- err
	}()
	// Fixed code holds the users lock during the tombstone read. Old code lets
	// deactivation commit at this barrier, then creates/rebinds a new account.
	deactivationFinished := false
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for waiting := true; waiting; {
		select {
		case err = <-deactDone:
			if err != nil {
				t.Fatal(err)
			}
			deactivationFinished = true
			waiting = false
		case <-ticker.C:
			var blocked bool
			if err = s.db.QueryRowContext(ctx, `SELECT COALESCE(wait_event_type='Lock',false) FROM pg_stat_activity WHERE pid=$1`, deactivationPID).Scan(&blocked); err != nil {
				t.Fatal(err)
			}
			if blocked {
				waiting = false
			}
		case <-ctx.Done():
			t.Fatal("deactivation neither completed nor reached the expected transaction lock")
		}
	}
	unblock()
	var out oidcResult
	select {
	case out = <-oidcDone:
	case <-ctx.Done():
		t.Fatal("OIDC did not finish after releasing its barrier")
	}
	if !deactivationFinished {
		select {
		case err = <-deactDone:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("deactivation did not finish")
		}
	}
	if out.err != nil {
		t.Fatalf("OIDC lookup before deactivation failed: %v", out.err)
	}
	if out.user.ID != original.ID {
		t.Fatalf("deactivated OIDC subject rebound to a new account: original=%s rebound=%s", original.ID, out.user.ID)
	}
	var bound string
	if err = s.db.QueryRowContext(ctx, `SELECT user_id::text FROM auth_identities WHERE provider='oidc:race' AND subject='subject'`).Scan(&bound); err != nil {
		t.Fatal(err)
	}
	if bound != original.ID {
		t.Fatalf("OIDC binding changed to %s", bound)
	}
	if _, err = s.PrincipalForUserID(ctx, original.ID); err != sql.ErrNoRows {
		t.Fatalf("original account is active after cleanup: %v", err)
	}
	var created int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE email='changed@example.test'`).Scan(&created); err != nil || created != 0 {
		t.Fatalf("changed-email accounts=%d err=%v", created, err)
	}
}

var _ driver.Rows = (*oidcBarrierRows)(nil)
