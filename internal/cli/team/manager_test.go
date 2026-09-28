package team

import (
	"errors"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestInitTeamRejected(t *testing.T) {
	mgr := NewManager(zap.NewNop())

	err := mgr.InitTeam(InitOptions{Slug: "acme"})
	if err == nil {
		t.Fatal("expected team init rejection")
	}
	if !strings.Contains(err.Error(), "team create") {
		t.Fatalf("expected team create guidance, got %v", err)
	}
}

func TestAddTeamUserValidatesInputBeforeContactingAPI(t *testing.T) {
	mgr := NewManager(zap.NewNop())
	for _, tc := range []struct{ slug, userID, role, want string }{
		{"", "user-1", "member", "team slug and user ID"},
		{"acme", "", "member", "team slug and user ID"},
		{"acme", "user-1", "admin", "role must be member or owner"},
	} {
		if err := mgr.AddTeamUser(tc.slug, tc.userID, tc.role); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("AddTeamUser(%q, %q, %q) error = %v, want %q", tc.slug, tc.userID, tc.role, err, tc.want)
		}
	}
}

func TestTeamUserAddCommandIsRegistered(t *testing.T) {
	command, _, err := NewWithManager(NewManager(zap.NewNop())).Find([]string{"user", "add"})
	if err != nil || command.Name() != "add" {
		t.Fatalf("team user add command = %v, err = %v", command, err)
	}
	if command.Flags().Lookup("role") == nil {
		t.Fatal("team user add is missing --role")
	}
}

func TestTeamUserCreateDuplicateEmailGuidesExistingUserPath(t *testing.T) {
	err := explainTeamUserCreateError(errors.New(`API 400: pq: duplicate key value violates unique constraint "users_email_key"`))
	if !strings.Contains(err.Error(), "team user add") || strings.Contains(err.Error(), "users_email_key") {
		t.Fatalf("duplicate email guidance = %q", err)
	}
}
