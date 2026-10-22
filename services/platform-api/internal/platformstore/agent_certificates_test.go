package platformstore

import (
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"mcp-runtime/pkg/internalapi"
)

func TestAgentCertificatesCountsAllMatchingRowsButBoundsHistory(t *testing.T) {
	for _, human := range []string{"", "human-1"} {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		store := &Store{db: db}
		now := time.Now().UTC()
		item := internalapi.CertificateIssuance{Session: "session-1", Namespace: "team-a", Serial: "123", NotAfter: now.Add(time.Hour)}
		data, _ := json.Marshal(item)
		mock.ExpectQuery(regexp.QuoteMeta(agentCertificateQuery)).WithArgs("agent-1", sqlmock.AnyArg(), sqlmock.AnyArg(), human).
			WillReturnRows(sqlmock.NewRows([]string{"message", "created_at", "count"}).AddRow(string(data), now, 125))
		out, err := store.AgentCertificates(t.Context(), "agent-1", human)
		if err != nil {
			t.Fatal(err)
		}
		if out.Recorded != 125 || len(out.Recent) != 1 || out.Recent[0].Session != item.Session || !out.Recent[0].IssuedAt.Equal(now) {
			t.Fatalf("history %+v", out)
		}
		if out.Until.Sub(out.Since) != 7*24*time.Hour {
			t.Fatal("history window is not bounded")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
		_ = db.Close()
	}
}

func TestAgentCertificatesEmptyAndMalformedHistory(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		rows := sqlmock.NewRows([]string{"message", "created_at", "count"})
		if malformed {
			rows.AddRow("not-json", time.Now(), 1)
		}
		mock.ExpectQuery(regexp.QuoteMeta(agentCertificateQuery)).WillReturnRows(rows)
		out, err := (&Store{db: db}).AgentCertificates(t.Context(), "agent-1", "human-1")
		if (err != nil) != malformed {
			t.Fatalf("error=%v, malformed=%v", err, malformed)
		}
		if !malformed && (out.Recorded != 0 || out.Recent == nil) {
			t.Fatalf("empty result %+v", out)
		}
		_ = db.Close()
	}
}
