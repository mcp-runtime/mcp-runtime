package team

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"mcp-runtime/internal/cli/platformapi"
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

func TestTeamUserCreateUsesSecurePasswordInputs(t *testing.T) {
	command, _, err := NewWithManager(NewManager(zap.NewNop())).Find([]string{"user", "create"})
	if err != nil {
		t.Fatalf("find team user create: %v", err)
	}
	if command.Flags().Lookup("password") != nil {
		t.Fatal("team user create must not accept a password as a command-line argument")
	}
	if command.Flags().Lookup("password-stdin") == nil {
		t.Fatal("team user create is missing --password-stdin")
	}
}

func TestTeamUserRemoveCommandIsRegistered(t *testing.T) {
	command, _, err := NewWithManager(NewManager(zap.NewNop())).Find([]string{"user", "remove"})
	if err != nil || command.Name() != "remove" {
		t.Fatalf("team user remove command = %v, err = %v", command, err)
	}
	if command.Flags().Lookup("confirm-last-owner") == nil {
		t.Fatal("team user remove is missing --confirm-last-owner")
	}
}

func TestReadTeamUserPasswordFromStdin(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader("password123\n"))
	got, err := readTeamUserPassword(cmd, true)
	if err != nil || got != "password123" {
		t.Fatalf("readTeamUserPassword() = %q, %v", got, err)
	}
}

func TestReadTeamUserPasswordRejectsWeakInput(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader("short\n"))
	if _, err := readTeamUserPassword(cmd, true); err == nil || !strings.Contains(err.Error(), "at least 8") {
		t.Fatalf("readTeamUserPassword() error = %v", err)
	}
}

func TestReadTeamUserPasswordPromptsTwiceWithoutEchoing(t *testing.T) {
	old := teamUserPasswordPrompt
	t.Cleanup(func() { teamUserPasswordPrompt = old })
	values := []string{"password123", "password123"}
	var prompts []string
	teamUserPasswordPrompt = func(_ io.Writer, prompt string) (string, error) {
		prompts = append(prompts, prompt)
		value := values[0]
		values = values[1:]
		return value, nil
	}
	got, err := readTeamUserPassword(&cobra.Command{}, false)
	if err != nil || got != "password123" {
		t.Fatalf("readTeamUserPassword() = %q, %v", got, err)
	}
	if len(prompts) != 2 || prompts[0] == prompts[1] {
		t.Fatalf("prompts = %#v, want password and confirmation", prompts)
	}
}

func TestReadTeamUserPasswordRejectsMismatch(t *testing.T) {
	old := teamUserPasswordPrompt
	t.Cleanup(func() { teamUserPasswordPrompt = old })
	values := []string{"password123", "different123"}
	teamUserPasswordPrompt = func(io.Writer, string) (string, error) {
		value := values[0]
		values = values[1:]
		return value, nil
	}
	if _, err := readTeamUserPassword(&cobra.Command{}, false); err == nil || !strings.Contains(err.Error(), "do not match") {
		t.Fatalf("readTeamUserPassword() error = %v", err)
	}
}

func TestIsLastTeamOwner(t *testing.T) {
	members := []platformapi.TeamMembership{
		{UserID: "owner-1", Role: "owner"},
		{UserID: "member-1", Role: "member"},
	}
	if !isLastTeamOwner(members, "owner-1") {
		t.Fatal("expected only owner to require confirmation")
	}
	members = append(members, platformapi.TeamMembership{UserID: "owner-2", Role: "owner"})
	if isLastTeamOwner(members, "owner-1") {
		t.Fatal("expected one of multiple owners not to be the last owner")
	}
}

func TestTeamUserCreateDuplicateEmailGuidesExistingUserPath(t *testing.T) {
	err := explainTeamUserCreateError(errors.New(`API 400: pq: duplicate key value violates unique constraint "users_email_key"`))
	if !strings.Contains(err.Error(), "team user add") || strings.Contains(err.Error(), "users_email_key") {
		t.Fatalf("duplicate email guidance = %q", err)
	}
}
