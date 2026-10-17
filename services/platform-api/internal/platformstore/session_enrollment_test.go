package platformstore

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"mcp-runtime/pkg/sessionstate"
)

func TestEnrollmentCompletionRejectsCurrentRevocationOrChangedIdentity(t *testing.T) {
	for _, scenario := range []string{"revoked", "different UID", "expired"} {
		t.Run(scenario, func(t *testing.T) {
			s, mock := sessionTestStore(t)
			item := sessionFixture()
			item.Binding.ExpiresAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
			item.Annotations = map[string]string{sessionstate.AdapterEnrollmentPendingAnnotation: "true"}
			switch scenario {
			case "revoked":
				item.Binding.Revoked = true
			case "different UID":
				item.UID = "replacement"
			case "expired":
				item.Binding.ExpiresAt = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
			}
			data, _ := json.Marshal(item)
			mock.ExpectBegin()
			expectStateLock(mock)
			mock.ExpectQuery("SELECT data FROM sessions WHERE namespace=.*AND name=").WithArgs(item.Namespace, string(item.Binding.Name)).WillReturnRows(sqlmock.NewRows([]string{"data"}).AddRow(data))
			mock.ExpectRollback()
			if err := s.CompleteSessionEnrollment(context.Background(), item.Namespace, string(item.Binding.Name), "session-uid"); !errors.Is(err, sessionstate.ErrConflict) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestEnrollmentCompletionPreservesCurrentAuthorizationAndMetadata(t *testing.T) {
	s, mock := sessionTestStore(t)
	item := sessionFixture()
	item.Binding.ExpiresAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	item.Annotations = map[string]string{sessionstate.AdapterEnrollmentPendingAnnotation: "true", "unrelated": "retained"}
	data, _ := json.Marshal(item)
	mock.ExpectBegin()
	expectStateLock(mock)
	mock.ExpectQuery("SELECT data FROM sessions WHERE namespace=.*AND name=").WithArgs(item.Namespace, string(item.Binding.Name)).WillReturnRows(sqlmock.NewRows([]string{"data"}).AddRow(data))
	mock.ExpectQuery("SELECT status,team_id::text FROM agents").WithArgs(string(item.Binding.AgentID)).WillReturnRows(sqlmock.NewRows([]string{"status", "team"}).AddRow("active", string(item.Binding.TeamID)))
	delete(item.Annotations, sessionstate.AdapterEnrollmentPendingAnnotation)
	want, _ := json.Marshal(item)
	mock.ExpectExec("INSERT INTO sessions").WithArgs(item.ID, item.Namespace, string(item.Binding.Name), item.ServerNamespace, item.ServerName, item.ServerUID, string(item.Binding.AgentID), string(item.Binding.TeamID), item.AuthorityTeamID, item.GrantNamespace, item.GrantName, item.Binding.Revoked, sqlmock.AnyArg(), string(want)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("UPDATE session_sync_state SET sequence=sequence\\+1").WillReturnRows(sqlmock.NewRows([]string{"sequence"}).AddRow(11))
	mock.ExpectExec("INSERT INTO session_changes").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := s.CompleteSessionEnrollment(context.Background(), item.Namespace, string(item.Binding.Name), item.UID); err != nil {
		t.Fatal(err)
	}
}

func TestPendingEnrollmentCleanupChecksCurrentStateInsideMutation(t *testing.T) {
	for _, scenario := range []string{"completed", "other owner", "different UID", "pending"} {
		t.Run(scenario, func(t *testing.T) {
			s, mock := sessionTestStore(t)
			item := sessionFixture()
			item.Binding.HumanID = "owner"
			item.Annotations = map[string]string{sessionstate.AdapterEnrollmentPendingAnnotation: "true"}
			switch scenario {
			case "completed":
				delete(item.Annotations, sessionstate.AdapterEnrollmentPendingAnnotation)
			case "other owner":
				item.Binding.HumanID = "other"
			case "different UID":
				item.UID = "replacement"
			}
			data, _ := json.Marshal(item)
			mock.ExpectBegin()
			expectStateLock(mock)
			mock.ExpectQuery("SELECT data FROM sessions WHERE namespace=.*AND name=").WithArgs(item.Namespace, string(item.Binding.Name)).WillReturnRows(sqlmock.NewRows([]string{"data"}).AddRow(data))
			if scenario == "pending" {
				mock.ExpectExec("INSERT INTO session_deleted_identities").WithArgs(item.ID).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec("DELETE FROM sessions WHERE namespace=").WithArgs(item.Namespace, string(item.Binding.Name)).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectQuery("UPDATE session_sync_state SET sequence=sequence\\+1").WillReturnRows(sqlmock.NewRows([]string{"sequence"}).AddRow(11))
				mock.ExpectExec("INSERT INTO session_changes").WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
			} else {
				mock.ExpectRollback()
			}
			err := s.DeletePendingSession(context.Background(), item.Namespace, string(item.Binding.Name), "session-uid", "owner")
			if scenario == "pending" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, sessionstate.ErrConflict) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
