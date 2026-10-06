package platformstore

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
)

func TestCreateTeamUserTransaction(t *testing.T) {
	for _, scenario := range []string{"success", "missing team", "duplicate email", "identity failure", "membership failure", "commit failure"} {
		t.Run(scenario, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			store := &Store{db: db}
			failure := errors.New("database operation failed")
			mock.ExpectBegin()
			team := mock.ExpectQuery("(?s)SELECT t.id, t.slug, t.display_name.*LEFT JOIN namespaces n.*FOR SHARE OF t").WithArgs("acme")
			if scenario == "missing team" {
				team.WillReturnRows(sqlmock.NewRows([]string{"id", "slug", "name", "namespace"}))
			} else {
				team.WillReturnRows(sqlmock.NewRows([]string{"id", "slug", "name", "namespace"}).AddRow("team-id", "acme", "Acme", "mcp-team-acme"))
				user := mock.ExpectExec("INSERT INTO users").WithArgs(sqlmock.AnyArg(), "member@example.com", RoleUser)
				if scenario == "duplicate email" {
					user.WillReturnError(&pq.Error{Code: "23505", Constraint: "users_email_key", Detail: "private SQL data"})
				} else {
					user.WillReturnResult(sqlmock.NewResult(1, 1))
					identity := mock.ExpectExec("INSERT INTO auth_identities").WithArgs(sqlmock.AnyArg(), passwordProvider, "member@example.com", sqlmock.AnyArg())
					if scenario == "identity failure" {
						identity.WillReturnError(failure)
					} else {
						identity.WillReturnResult(sqlmock.NewResult(1, 1))
						membership := mock.ExpectExec("INSERT INTO team_memberships").WithArgs(sqlmock.AnyArg(), "team-id", sqlmock.AnyArg(), TeamRoleMember)
						if scenario == "membership failure" {
							membership.WillReturnError(failure)
						} else {
							membership.WillReturnResult(sqlmock.NewResult(1, 1))
						}
					}
				}
			}
			switch scenario {
			case "success":
				mock.ExpectCommit()
			case "commit failure":
				mock.ExpectCommit().WillReturnError(failure)
			default:
				mock.ExpectRollback()
			}
			user, membership, err := store.CreateTeamUser(context.Background(), " ACME ", " Member@example.com ", " password123 ", TeamRoleMember)
			switch scenario {
			case "success":
				if err != nil || user.ID == "" || membership.UserID != user.ID || membership.TeamSlug != "acme" {
					t.Fatalf("user=%+v membership=%+v error=%v", user, membership, err)
				}
			case "missing team":
				if !errors.Is(err, sql.ErrNoRows) {
					t.Fatalf("error=%v", err)
				}
			case "duplicate email":
				if !errors.Is(err, ErrEmailAlreadyRegistered) {
					t.Fatalf("error=%v", err)
				}
			default:
				if !errors.Is(err, failure) {
					t.Fatalf("error=%v", err)
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCreateTeamUserValidatesBeforePersistence(t *testing.T) {
	store := NewForTest(nil)
	for _, tc := range []struct{ email, password, role, message string }{
		{"invalid", "password123", "member", "valid email required"},
		{"member@example.com", "short", "member", "password must be at least 8 characters"},
		{"member@example.com", "password123", "admin", "membership role must be owner or member"},
	} {
		_, _, err := store.CreateTeamUser(t.Context(), "acme", tc.email, tc.password, tc.role)
		var input UserInputError
		if !errors.As(err, &input) || err.Error() != tc.message {
			t.Fatalf("error=%v want=%s", err, tc.message)
		}
	}
}

func TestUpsertMembershipRejectsInvalidInputBeforePersistence(t *testing.T) {
	store := NewForTest(nil)
	for _, tc := range []struct{ userID, role, message string }{
		{"not-a-uuid", "member", "valid user ID required"},
		{"", "member", "userID is required"},
		{"valid-user-id", "admin", "membership role must be owner or member"},
	} {
		_, err := store.UpsertTeamMembership(t.Context(), "acme", tc.userID, tc.role)
		var input UserInputError
		if !errors.As(err, &input) || input.Error() != tc.message {
			t.Fatalf("error=%v want=%s", err, tc.message)
		}
	}
}
