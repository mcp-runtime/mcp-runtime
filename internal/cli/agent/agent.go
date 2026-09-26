package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/platformapi"
)

func New(runtime *core.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Manage team-owned governance agents",
		Long:  "Create and manage immutable, team-scoped agent identities used by access grants and sessions. Deactivation retains history and revokes active sessions.",
	}
	cmd.AddCommand(newCreateCmd(), newListCmd(), newGetCmd(), newRenameCmd(), newStatusCmd("deactivate", false), newStatusCmd("reactivate", true))
	_ = runtime
	return cmd
}

func newCreateCmd() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "create [team-slug]",
		Short: "Create a managed agent in a team",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(name) == "" {
				return fmt.Errorf("--name is required")
			}
			client, err := platformapi.NewPlatformClient()
			if err != nil {
				return err
			}
			agent, err := client.CreateAgent(context.Background(), args[0], name)
			if err != nil {
				return err
			}
			core.Success(fmt.Sprintf("Created agent %s (%s) in team %s", agent.Name, agent.ID, agent.TeamSlug))
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Human-readable name (1-64 characters)")
	return cmd
}

func newListCmd() *cobra.Command {
	var status, query, cursor string
	var limit int
	cmd := &cobra.Command{
		Use:   "list [team-slug]",
		Short: "List agents in a team",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := platformapi.NewPlatformClient()
			if err != nil {
				return err
			}
			page, err := client.ListAgents(context.Background(), args[0], status, query, cursor, limit)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "ID\tNAME\tSTATUS\tTEAM")
			for _, item := range page.Agents {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", item.ID, item.Name, item.Status, item.TeamSlug)
			}
			if err := w.Flush(); err != nil {
				return err
			}
			if page.NextCursor != "" {
				core.Info("More agents are available; pass --cursor " + page.NextCursor)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "Filter by active or inactive status")
	cmd.Flags().StringVar(&query, "query", "", "Filter by agent name")
	cmd.Flags().StringVar(&cursor, "cursor", "", "Continue a paginated result")
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum records (1-200)")
	return cmd
}

func newGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get [agent-id]",
		Short: "Get an agent by immutable ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := platformapi.NewPlatformClient()
			if err != nil {
				return err
			}
			agent, err := client.GetAgent(context.Background(), args[0])
			if err != nil {
				return err
			}
			fmt.Printf("ID: %s\nName: %s\nTeam: %s (%s)\nStatus: %s\nCreated: %s\nUpdated: %s\n", agent.ID, agent.Name, agent.TeamSlug, agent.TeamID, agent.Status, agent.CreatedAt, agent.UpdatedAt)
			return nil
		},
	}
}

func newRenameCmd() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "rename [agent-id]",
		Short: "Rename an agent without changing its ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(name) == "" {
				return fmt.Errorf("--name is required")
			}
			client, err := platformapi.NewPlatformClient()
			if err != nil {
				return err
			}
			agent, err := client.RenameAgent(context.Background(), args[0], name)
			if err != nil {
				return err
			}
			core.Success(fmt.Sprintf("Renamed agent %s to %q", agent.ID, agent.Name))
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "New agent name (1-64 characters)")
	return cmd
}

func newStatusCmd(action string, active bool) *cobra.Command {
	return &cobra.Command{
		Use:   action + " [agent-id]",
		Short: strings.ToUpper(action[:1]) + action[1:] + " an agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := platformapi.NewPlatformClient()
			if err != nil {
				return err
			}
			agent, err := client.SetAgentActive(context.Background(), args[0], active)
			if err != nil {
				return err
			}
			core.Success(fmt.Sprintf("Agent %s is %s", agent.ID, agent.Status))
			return nil
		},
	}
}
