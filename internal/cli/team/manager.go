package team

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"go.uber.org/zap"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/platformapi"
)

type Manager struct {
	logger *zap.Logger
}

func NewManager(logger *zap.Logger) *Manager {
	return &Manager{logger: logger}
}

// InitOptions is kept only so the deprecated team init command can reject with a
// clear message without preserving the old direct-kubectl implementation.
type InitOptions struct {
	Slug string
}

func (m *Manager) ListTeams() error {
	client, err := platformapi.NewPlatformClient()
	if err != nil {
		return err
	}
	teams, err := client.ListTeams(context.Background())
	if err != nil {
		return err
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SLUG\tNAME\tNAMESPACE")
	for _, team := range teams {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", team.Slug, team.Name, team.Namespace)
	}
	_ = tw.Flush()
	return nil
}

func (m *Manager) CreateTeam(slug, name string) error {
	client, err := platformapi.NewPlatformClient()
	if err != nil {
		return err
	}
	slug = strings.TrimSpace(slug)
	name = strings.TrimSpace(name)
	if name == "" {
		name = slug
	}
	team, err := client.CreateTeam(context.Background(), slug, name)
	if err != nil {
		return err
	}
	core.Success(fmt.Sprintf("Created team %s (namespace: %s)", team.Slug, team.Namespace))
	return nil
}

func (m *Manager) ListTeamUsers(slug string) error {
	client, err := platformapi.NewPlatformClient()
	if err != nil {
		return err
	}
	members, err := client.ListTeamMembers(context.Background(), strings.TrimSpace(slug))
	if err != nil {
		return err
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "EMAIL\tUSER ID\tROLE\tTEAM\tNAMESPACE")
	for _, member := range members {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", member.Email, member.UserID, member.Role, member.TeamSlug, member.TeamNamespace)
	}
	_ = tw.Flush()
	return nil
}

func (m *Manager) CreateTeamUser(slug, email, password, role string) error {
	client, err := platformapi.NewPlatformClient()
	if err != nil {
		return err
	}
	slug = strings.TrimSpace(slug)
	email = strings.TrimSpace(email)
	role = strings.TrimSpace(role)
	if email == "" {
		return errors.New("email is required (use --email or --username)")
	}
	if strings.TrimSpace(password) == "" {
		return errors.New("password is required")
	}
	if role == "" {
		role = "member"
	}
	member, err := client.CreateTeamUser(context.Background(), slug, email, password, role)
	if err != nil {
		return explainTeamUserCreateError(err)
	}
	core.Success(fmt.Sprintf("Ensured user %s in team %s as %s", member.Email, member.TeamSlug, member.Role))
	return nil
}

func explainTeamUserCreateError(err error) error {
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "users_email_key") || strings.Contains(message, "duplicate key value") {
		return errors.New("platform user already exists; find their user ID in the platform UI and run mcp-runtime team user add <team-slug> <user-id> --role member (or --role owner)")
	}
	return err
}

func (m *Manager) AddTeamUser(slug, userID, role string) error {
	slug = strings.TrimSpace(slug)
	userID = strings.TrimSpace(userID)
	role = strings.TrimSpace(role)
	if slug == "" || userID == "" {
		return errors.New("team slug and user ID are required")
	}
	if role != "member" && role != "owner" {
		return errors.New("role must be member or owner")
	}
	client, err := platformapi.NewPlatformClient()
	if err != nil {
		return err
	}
	member, err := client.UpsertTeamMember(context.Background(), slug, userID, role)
	if err != nil {
		return err
	}
	core.Success(fmt.Sprintf("Ensured user %s in team %s as %s", member.UserID, member.TeamSlug, member.Role))
	return nil
}

func (m *Manager) InitTeam(opts InitOptions) error {
	return core.NewWithSentinel(
		nil,
		`team init is direct Kubernetes administration and requires admin cluster access with kubectl; use "mcp-runtime team create <slug>" for the normal platform-backed flow`,
	)
}
