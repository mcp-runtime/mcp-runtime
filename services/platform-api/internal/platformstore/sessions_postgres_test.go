package platformstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"mcp-runtime/pkg/policy"
	"mcp-runtime/pkg/sessionstate"
)

// This integration test requires an explicitly configured disposable Postgres
// database. It never reads POSTGRES_DSN or ambient cluster credentials.
func TestPostgresSessionSyncLifecycle(t *testing.T) {
	dsn := os.Getenv("PLATFORM_SESSION_TEST_DSN")
	if dsn == "" {
		t.Skip("set PLATFORM_SESSION_TEST_DSN to a disposable Postgres database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := "session_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Error(err)
		}
	}()
	if strings.Contains(dsn, "://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		q.Set("search_path", schema+",public")
		u.RawQuery = q.Encode()
		dsn = u.String()
	} else {
		dsn += " search_path=" + schema + ",public"
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &Store{db: db}
	if err = s.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.initializeSessions(ctx); err != nil {
		t.Fatal(err)
	}
	team := uuid.NewString()
	if _, err = db.ExecContext(ctx, `INSERT INTO teams(id,slug,display_name) VALUES($1,'team','Team');`, team); err != nil {
		t.Fatal(err)
	}
	agent := "agt_01arz3ndektsv4rrffq69g5fav"
	if _, err = db.ExecContext(ctx, `INSERT INTO agents(id,team_id,name,name_normalized,status) VALUES($1,$2,'Agent','agent','active')`, agent, team); err != nil {
		t.Fatal(err)
	}
	base := sessionFixture()
	base.Binding.AgentID = policy.AgentID(agent)
	base.Binding.TeamID = policy.TeamID(team)
	base.UID = ""
	base.CreatedAt = time.Time{}
	item, err := s.PutSession(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	server := sessionstate.Server{Namespace: base.ServerNamespace, Name: base.ServerName, UID: base.ServerUID}
	snap, err := s.SessionSnapshot(ctx, server)
	if err != nil || len(snap.Sessions) != 1 {
		t.Fatalf("snapshot=%+v err=%v", snap, err)
	}
	if err = s.SetSessionRevoked(ctx, item.Namespace, string(item.Binding.Name), true, "test_revoked"); err != nil {
		t.Fatal(err)
	}
	changes, err := s.SessionChanges(ctx, server, snap.Epoch, snap.Sequence)
	if err != nil || len(changes.Changes) != 1 || changes.Changes[0].Operation != "delete" {
		t.Fatalf("changes=%+v err=%v", changes, err)
	}
	if err = s.SetSessionRevoked(ctx, item.Namespace, string(item.Binding.Name), false, ""); err != nil {
		t.Fatal(err)
	}
	// A writer racing deactivation either commits first and gets revoked in the
	// same transaction, or observes the inactive directory record and fails.
	var wg sync.WaitGroup
	errs := make(chan error, 33)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			candidate := base
			candidate.Binding.Name = policy.SessionID(fmt.Sprintf("race-%d", i))
			_, err := s.PutSession(ctx, candidate)
			if err != nil && !errors.Is(err, sessionstate.ErrInactiveAgent) {
				errs <- err
			}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := s.SetAgentStatus(ctx, agent, "inactive", "")
		if err != nil {
			errs <- err
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	snap, err = s.SessionSnapshot(ctx, server)
	if err != nil || len(snap.Sessions) != 0 {
		t.Fatalf("active sessions after deactivation=%d err=%v", len(snap.Sessions), err)
	}
	if _, err = s.SetAgentStatus(ctx, agent, "active", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RevokeSessions(ctx, sessionstate.Filter{ServerUID: server.UID}, "server_retired"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PutSession(ctx, base); !errors.Is(err, sessionstate.ErrConflict) {
		t.Fatalf("retired generation accepted: %v", err)
	}
	if err = s.SetSessionRevoked(ctx, item.Namespace, string(item.Binding.Name), false, ""); !errors.Is(err, sessionstate.ErrConflict) {
		t.Fatalf("retired session unrevoked: %v", err)
	}
	base.ServerUID = "generation-2"
	base.Binding.Name = "new-generation"
	newItem, err := s.PutSession(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteSessionVersioned(ctx, newItem.Namespace, string(newItem.Binding.Name), newItem.UID); err != nil {
		t.Fatal(err)
	}
	base.ServerUID = "generation-3"
	if _, err = s.PutSession(ctx, base); !errors.Is(err, sessionstate.ErrConflict) {
		t.Fatalf("deleted certificate name reused after republish: %v", err)
	}
	deletedName := base.Binding.Name
	base.Binding.Name = "fresh-generation"
	if _, err = s.PutSession(ctx, base); err != nil {
		t.Fatalf("new name rejected: %v", err)
	}
	// Advance the global sequence and write once to exercise real retention
	// pruning without synthesizing 100,000 session records. Identity reservations
	// and persisted agent revocations must outlive the bounded change log.
	if _, err = db.ExecContext(ctx, `UPDATE session_sync_state SET sequence=sequence+$1 WHERE singleton`, sessionChangeRetention+1); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PutSession(ctx, base); err != nil {
		t.Fatal(err)
	}
	var historicalDeletes int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM session_changes WHERE data->>'id'=$1 AND data->>'operation'='delete'`, newItem.ID).Scan(&historicalDeletes); err != nil {
		t.Fatal(err)
	}
	if historicalDeletes != 0 {
		t.Fatal("deleted change event was not pruned")
	}
	deletedCandidate := base
	deletedCandidate.Binding.Name = deletedName
	if _, err = s.PutSession(ctx, deletedCandidate); !errors.Is(err, sessionstate.ErrConflict) {
		t.Fatalf("deleted identity reservation lost after pruning: %v", err)
	}
	revoked, found, err := s.GetSession(ctx, item.Namespace, string(item.Binding.Name))
	if err != nil || !found || !revoked.Binding.Revoked {
		t.Fatalf("agent revocation lost after pruning: %+v found=%v err=%v", revoked, found, err)
	}
	retiredCandidate := base
	retiredCandidate.ServerUID = server.UID
	retiredCandidate.Binding.Name = "new-name-old-server"
	if _, err = s.PutSession(ctx, retiredCandidate); !errors.Is(err, sessionstate.ErrConflict) {
		t.Fatalf("retired server reservation lost after pruning: %v", err)
	}
	// A restore fence works even when sequence remains numerically unchanged.
	snap, err = s.SessionSnapshot(ctx, sessionstate.Server{Namespace: server.Namespace, UID: base.ServerUID})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.initializeSessions(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SessionChanges(ctx, server, snap.Epoch, snap.Sequence); !errors.Is(err, sessionstate.ErrReset) {
		t.Fatalf("old epoch accepted: %v", err)
	}
	newSnap, err := s.SessionSnapshot(ctx, server)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `UPDATE session_sync_state SET sequence=sequence-1 WHERE singleton`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SessionChanges(ctx, server, newSnap.Epoch, newSnap.Sequence); !errors.Is(err, sessionstate.ErrReset) {
		t.Fatalf("cursor ahead accepted: %v", err)
	}
}
