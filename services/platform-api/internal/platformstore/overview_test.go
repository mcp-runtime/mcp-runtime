package platformstore

import (
	"errors"
	"github.com/DATA-DOG/go-sqlmock"
	"testing"
)

func TestOverviewCountsIncludesAllDirectoryRowsAndExcludesInactiveSessions(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := &Store{db: db}
	query := `(?s)SELECT.*COUNT\(\*\) FROM teams WHERE deleted_at IS NULL.*COUNT\(\*\) FROM users WHERE deleted_at IS NULL.*COUNT\(\*\) FROM agents.*COUNT\(\*\) FROM sessions s WHERE NOT s.revoked.*s.expires_at > now\(\).*session_retired_servers.*a.status='active'`
	mock.ExpectQuery(query).WillReturnRows(sqlmock.NewRows([]string{"teams", "users", "agents", "sessions"}).AddRow(300, 1000, 3500, 888))
	out, err := store.OverviewCounts(t.Context())
	if err != nil || out.Users != 1000 || out.ActiveSessions != 888 {
		t.Fatalf("counts=%+v err=%v", out, err)
	}
	mock.ExpectQuery(query).WillReturnError(errors.New("database offline"))
	if _, err := store.OverviewCounts(t.Context()); err == nil {
		t.Fatal("failed totals read treated as zero")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
