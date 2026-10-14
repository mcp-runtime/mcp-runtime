package platformstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"mcp-runtime/pkg/policy"
	"mcp-runtime/pkg/sessionstate"
)

func sessionFixture() sessionstate.Session {
	return sessionstate.Session{ID: "tenant/sess", Namespace: "tenant", ServerNamespace: "tenant", ServerName: "server", ServerUID: "generation-1", UID: "session-uid", Binding: policy.Binding{Name: "sess", Namespace: "tenant", AgentID: "agent", TeamID: "team", ConsentedTrust: "low"}, CreatedAt: time.Unix(1000, 0).UTC()}
}
func sessionTestStore(t *testing.T) (*Store, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})
	return &Store{db: db}, mock
}
func expectStateLock(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT sequence FROM session_sync_state WHERE singleton FOR UPDATE`)).WillReturnRows(sqlmock.NewRows([]string{"sequence"}).AddRow(10))
}

func TestSessionSnapshotOverConfigMapLimit(t *testing.T) {
	s, mock := sessionTestStore(t)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT epoch,sequence FROM session_sync_state").WillReturnRows(sqlmock.NewRows([]string{"epoch", "sequence"}).AddRow("epoch-1", 3001))
	rows := sqlmock.NewRows([]string{"data"})
	for i := 0; i < 3001; i++ {
		item := sessionFixture()
		item.ID = fmt.Sprintf("tenant/sess-%04d", i)
		item.Binding.Name = policy.SessionID(fmt.Sprintf("sess-%04d", i))
		data, _ := json.Marshal(item)
		rows.AddRow(data)
	}
	mock.ExpectQuery("SELECT data FROM sessions WHERE server_namespace=.*AND server_uid=.*AND NOT revoked").WithArgs("tenant", "generation-1").WillReturnRows(rows)
	mock.ExpectCommit()
	snapshot, err := s.SessionSnapshot(context.Background(), sessionstate.Server{Namespace: "tenant", UID: "generation-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Sessions) != 3001 || snapshot.Sequence != 3001 {
		t.Fatalf("snapshot size/sequence=%d/%d", len(snapshot.Sessions), snapshot.Sequence)
	}
}

func TestSessionChangesRestoreAndRetentionFence(t *testing.T) {
	for _, tt := range []struct {
		name, epoch string
		after       int64
	}{{"different epoch", "old", 10}, {"restored lower sequence", "current", 101}, {"compacted", "current", 4}, {"negative", "current", -1}} {
		t.Run(tt.name, func(t *testing.T) {
			s, mock := sessionTestStore(t)
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT epoch,sequence,floor").WillReturnRows(sqlmock.NewRows([]string{"epoch", "sequence", "floor"}).AddRow("current", 100, 5))
			mock.ExpectRollback()
			_, err := s.SessionChanges(context.Background(), sessionstate.Server{Namespace: "tenant", UID: "generation-1"}, tt.epoch, tt.after)
			if !errors.Is(err, sessionstate.ErrReset) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestSessionChangeTombstonesAndScopedPagination(t *testing.T) {
	s, mock := sessionTestStore(t)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT epoch,sequence,floor").WillReturnRows(sqlmock.NewRows([]string{"epoch", "sequence", "floor"}).AddRow("current", 1200, 5))
	rows := sqlmock.NewRows([]string{"data"})
	for i := 1; i <= 1001; i++ {
		change := sessionstate.Change{Kind: "session", ID: fmt.Sprintf("tenant/s-%d", i), Sequence: int64(i + 5), Operation: "delete"}
		data, _ := json.Marshal(change)
		rows.AddRow(data)
	}
	mock.ExpectQuery("SELECT data FROM session_changes WHERE server_namespace=.*AND server_uid=.*AND sequence>").WithArgs("tenant", "generation-1", int64(5), 1001).WillReturnRows(rows)
	mock.ExpectCommit()
	changes, err := s.SessionChanges(context.Background(), sessionstate.Server{Namespace: "tenant", UID: "generation-1"}, "current", 5)
	if err != nil {
		t.Fatal(err)
	}
	if !changes.More || len(changes.Changes) != 1000 || changes.Sequence != 1005 || changes.Changes[0].Session != nil {
		t.Fatalf("invalid pagination %+v", changes)
	}
}

func TestPutSessionRejectsRetiredUIDBeforeAgentCheck(t *testing.T) {
	s, mock := sessionTestStore(t)
	mock.ExpectBegin()
	expectStateLock(mock)
	mock.ExpectQuery("SELECT EXISTS.*session_retired_servers").WithArgs("generation-1").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectRollback()
	_, err := s.PutSession(context.Background(), sessionFixture())
	if !errors.Is(err, sessionstate.ErrConflict) {
		t.Fatalf("error=%v", err)
	}
}

func TestPutSessionRejectsInactiveOrWrongTeam(t *testing.T) {
	for _, tt := range []struct{ status, team string }{{"inactive", "team"}, {"active", "other"}} {
		t.Run(tt.status+tt.team, func(t *testing.T) {
			s, mock := sessionTestStore(t)
			mock.ExpectBegin()
			expectStateLock(mock)
			mock.ExpectQuery("SELECT EXISTS.*session_retired_servers").WithArgs("generation-1").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
			mock.ExpectQuery("SELECT status,team_id::text FROM agents WHERE id=.*FOR UPDATE").WithArgs("agent").WillReturnRows(sqlmock.NewRows([]string{"status", "team"}).AddRow(tt.status, tt.team))
			mock.ExpectRollback()
			_, err := s.PutSession(context.Background(), sessionFixture())
			if !errors.Is(err, sessionstate.ErrInactiveAgent) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestAgentDeactivationRollsBackWhenRevocationFails(t *testing.T) {
	s, mock := sessionTestStore(t)
	mock.ExpectBegin()
	expectStateLock(mock)
	mock.ExpectQuery("UPDATE agents SET status=").WithArgs("agent", "inactive", "actor").WillReturnRows(sqlmock.NewRows([]string{"id", "team", "name", "status", "created_by", "created_at", "updated_at", "deactivated_at", "deactivated_by"}).AddRow("agent", "team", "Agent", "inactive", "", time.Now(), time.Now(), time.Now(), "actor"))
	mock.ExpectQuery("SELECT data FROM sessions WHERE NOT revoked AND true AND agent_id=").WithArgs("agent").WillReturnError(errors.New("postgres unavailable"))
	mock.ExpectRollback()
	if _, err := s.SetAgentStatus(context.Background(), "agent", "inactive", "actor"); err == nil {
		t.Fatal("deactivation succeeded without revocation")
	}
}

func TestRevokeServerCommitsRetirementFence(t *testing.T) {
	s, mock := sessionTestStore(t)
	mock.ExpectBegin()
	expectStateLock(mock)
	mock.ExpectExec("INSERT INTO session_retired_servers").WithArgs("generation-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT data FROM sessions WHERE NOT revoked AND true AND server_uid=").WithArgs("generation-1").WillReturnRows(sqlmock.NewRows([]string{"data"}))
	mock.ExpectCommit()
	n, err := s.RevokeSessions(context.Background(), sessionstate.Filter{ServerUID: "generation-1"}, "server_retired")
	if err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestGetSessionScopesNamespaceAndName(t *testing.T) {
	s, mock := sessionTestStore(t)
	item := sessionFixture()
	data, _ := json.Marshal(item)
	mock.ExpectQuery("SELECT data FROM sessions WHERE namespace=.*AND name=").WithArgs("tenant", "sess").WillReturnRows(sqlmock.NewRows([]string{"data"}).AddRow(data))
	out, ok, err := s.GetSession(context.Background(), "tenant", "sess")
	if err != nil || !ok || out.ServerUID != item.ServerUID {
		t.Fatalf("out=%+v ok=%v err=%v", out, ok, err)
	}
}
