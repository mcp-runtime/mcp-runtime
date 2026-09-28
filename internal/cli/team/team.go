package team

import (
	"github.com/spf13/cobra"

	"mcp-runtime/internal/cli/core"
)

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
	var userPassword string
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
			return mgr.CreateTeamUser(args[0], email, userPassword, userRole)
		},
	}
	userCreateCmd.Flags().StringVar(&userEmail, "email", "", "Platform account email")
	userCreateCmd.Flags().StringVar(&userUsername, "username", "", "Alias for --email")
	userCreateCmd.Flags().StringVar(&userPassword, "password", "", "Platform account password (prefer a private shell or environment-managed invocation)")
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
	userCmd.AddCommand(userCreateCmd, userAddCmd, userListCmd)

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
