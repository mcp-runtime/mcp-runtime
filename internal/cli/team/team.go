package team

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"golang.org/x/term"

	"mcp-runtime/internal/cli/core"
)

// teamUserPasswordPrompt is replaceable by tests and never echoes password input.
var teamUserPasswordPrompt = func(stderr io.Writer, prompt string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", fmt.Errorf("team user create requires a TTY password prompt or --password-stdin")
	}
	fmt.Fprint(stderr, prompt)
	password, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(stderr)
	return string(password), err
}

func readTeamUserPassword(cmd *cobra.Command, fromStdin bool) (string, error) {
	var password string
	if fromStdin {
		data, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return "", fmt.Errorf("read password from stdin: %w", err)
		}
		password = strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	} else {
		first, err := teamUserPasswordPrompt(cmd.ErrOrStderr(), "New platform account password: ")
		if err != nil {
			return "", fmt.Errorf("read password securely: %w", err)
		}
		second, err := teamUserPasswordPrompt(cmd.ErrOrStderr(), "Confirm platform account password: ")
		if err != nil {
			return "", fmt.Errorf("read password confirmation securely: %w", err)
		}
		if first != second {
			return "", fmt.Errorf("passwords do not match")
		}
		password = first
	}
	if len(password) < 8 {
		return "", fmt.Errorf("password must be at least 8 characters")
	}
	return password, nil
}

func New(runtime *core.Runtime) *cobra.Command {
	return NewWithManager(NewManager(runtime.Logger()))
}

func NewWithManager(mgr *Manager) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "team",
		Short: "Manage internal platform teams",
		Long:  "Create and list internal teams through the platform API.",
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List teams",
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.ListTeams()
		},
	}

	var name string
	createCmd := &cobra.Command{
		Use:   "create [slug]",
		Short: "Create a team and managed namespace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.CreateTeam(args[0], name)
		},
	}
	createCmd.Flags().StringVar(&name, "name", "", "Display name for the team (defaults to slug)")

	var userEmail string
	var userUsername string
	var userPasswordStdin bool
	var userRole string
	userCmd := &cobra.Command{
		Use:   "user",
		Short: "Manage password-login users for a team",
	}
	userCreateCmd := &cobra.Command{
		Use:   "create [team-slug]",
		Short: "Create a password-login user and add them to a team",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			email, err := core.ResolveEmailAlias(userEmail, userUsername)
			if err != nil {
				return err
			}
			password, err := readTeamUserPassword(cmd, userPasswordStdin)
			if err != nil {
				return err
			}
			return mgr.CreateTeamUser(args[0], email, password, userRole)
		},
	}
	userCreateCmd.Flags().StringVar(&userEmail, "email", "", "Platform account email")
	userCreateCmd.Flags().StringVar(&userUsername, "username", "", "Alias for --email")
	userCreateCmd.Flags().BoolVar(&userPasswordStdin, "password-stdin", false, "Read the platform account password from stdin instead of prompting")
	userCreateCmd.Flags().StringVar(&userRole, "role", "member", "Team role for the user: member or owner")

	var existingRole string
	userAddCmd := &cobra.Command{
		Use:   "add [team-slug] [user-id]",
		Short: "Add an existing platform user to a team or update their team role",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.AddTeamUser(args[0], args[1], existingRole)
		},
	}
	userAddCmd.Flags().StringVar(&existingRole, "role", "member", "Team role for the user: member or owner")

	userListCmd := &cobra.Command{
		Use:   "list [team-slug]",
		Short: "List users in a team",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.ListTeamUsers(args[0])
		},
	}
	var confirmLastOwner bool
	userRemoveCmd := &cobra.Command{
		Use:   "remove [team-slug] [user-id]",
		Short: "Remove a user's membership from a team",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.RemoveTeamUser(args[0], args[1], confirmLastOwner)
		},
	}
	userRemoveCmd.Flags().BoolVar(&confirmLastOwner, "confirm-last-owner", false, "Allow removing the team's only owner")
	userCmd.AddCommand(userCreateCmd, userAddCmd, userListCmd, userRemoveCmd)

	initCmd := &cobra.Command{
		Use:   "init [slug]",
		Short: "Deprecated: use team create instead",
		Long:  "team init required direct Kubernetes administration. Use `mcp-runtime team create <slug>` for the normal platform-backed flow.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.InitTeam(InitOptions{Slug: args[0]})
		},
	}

	cmd.AddCommand(listCmd, createCmd, userCmd, initCmd)
	return cmd
}
