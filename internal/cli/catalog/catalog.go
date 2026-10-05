package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/platformapi"
)

type Manager struct{}

func New(_ *core.Runtime) *cobra.Command {
	mgr := &Manager{}
	cmd := &cobra.Command{
		Use:   "catalog",
		Short: "Search MCP Runtime catalogs",
		Long:  "Search MCP tools exposed by servers visible to the logged-in platform user.",
	}

	var filters catalogFilters
	toolsCmd := &cobra.Command{
		Use:   "tools",
		Short: "List tools across visible MCP servers",
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.ListTools(filters)
		},
	}
	bindToolFilterFlags(toolsCmd, &filters)

	var detailFilters catalogFilters
	toolCmd := &cobra.Command{
		Use:   "tool [name]",
		Short: "Show one tool from the catalog",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.GetTool(args[0], detailFilters)
		},
	}
	bindToolFilterFlags(toolCmd, &detailFilters)

	cmd.AddCommand(toolsCmd, toolCmd)
	return cmd
}

type catalogFilters struct {
	Query      string
	Namespace  string
	Team       string
	Server     string
	Trust      string
	SideEffect string
	Risk       string
	Drift      string
	Output     string
}

func bindToolFilterFlags(cmd *cobra.Command, filters *catalogFilters) {
	cmd.Flags().StringVarP(&filters.Query, "query", "q", "", "Search text")
	cmd.Flags().StringVar(&filters.Namespace, "namespace", "", "Namespace filter")
	cmd.Flags().StringVar(&filters.Team, "team", "", "Team ID filter")
	cmd.Flags().StringVar(&filters.Server, "server", "", "Server name filter")
	cmd.Flags().StringVar(&filters.SideEffect, "side-effect", "", "Side effect filter: read, write, destructive")
	cmd.Flags().StringVar(&filters.Trust, "trust", "", "Required trust filter: low, medium, high")
	cmd.Flags().StringVar(&filters.Risk, "risk", "", "Risk filter: low, medium, high")
	cmd.Flags().StringVar(&filters.Drift, "drift", "", "Drift filter: declared, ungoverned, missing")
	cmd.Flags().StringVarP(&filters.Output, "output", "o", "table", "Output format: table, json, yaml")
}

func (m *Manager) ListTools(filters catalogFilters) error {
	client, err := platformapi.NewPlatformClient()
	if err != nil {
		return err
	}
	rows, err := client.ListRuntimeTools(context.Background(), filters.queryMap())
	if err != nil {
		return err
	}
	return printToolRows(rows, filters.Output)
}

func (m *Manager) GetTool(name string, filters catalogFilters) error {
	name = strings.TrimSpace(name)
	filtersMap := filters.queryMap()
	if strings.TrimSpace(filters.Query) == "" {
		filtersMap["query"] = name
	}
	client, err := platformapi.NewPlatformClient()
	if err != nil {
		return err
	}
	rows, err := client.ListRuntimeTools(context.Background(), filtersMap)
	if err != nil {
		return err
	}
	matched := make([]platformapi.RuntimeToolRow, 0, len(rows))
	for _, row := range rows {
		if strings.EqualFold(row.ToolName, strings.TrimSpace(name)) {
			if filters.Server != "" && row.ServerName != filters.Server {
				continue
			}
			matched = append(matched, row)
		}
	}
	if len(matched) == 0 {
		return core.NewWithBase(nil, fmt.Sprintf("tool %q not found", name))
	}
	return printToolRows(matched, filters.Output)
}

func (f catalogFilters) queryMap() map[string]string {
	return map[string]string{
		"query":       f.Query,
		"namespace":   f.Namespace,
		"team":        f.Team,
		"server":      f.Server,
		"trust":       f.Trust,
		"side_effect": f.SideEffect,
		"risk":        f.Risk,
		"drift":       f.Drift,
	}
}

func printToolRows(rows []platformapi.RuntimeToolRow, output string) error {
	switch strings.ToLower(strings.TrimSpace(output)) {
	case "", "table":
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "TOOL\tSERVER\tNAMESPACE\tTRUST\tSIDE_EFFECT\tRISK\tDRIFT")
		for _, row := range rows {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				row.ToolName,
				row.ServerName,
				row.Namespace,
				valueOrDash(row.RequiredTrust),
				valueOrDash(row.SideEffect),
				valueOrDash(row.RiskLevel),
				valueOrDash(row.DriftStatus),
			)
		}
		return tw.Flush()
	case "json":
		data, err := json.MarshalIndent(map[string]any{"tools": rows}, "", "  ")
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(append(data, '\n'))
		return err
	case "yaml":
		data, err := yaml.Marshal(map[string]any{"tools": rows})
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(data)
		return err
	default:
		return core.NewWithBase(nil, "output must be table, json, or yaml")
	}
}

func valueOrDash(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	return value
}
