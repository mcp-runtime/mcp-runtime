// Package ops owns routing for the ops top-level command.
package ops

import (
	"github.com/spf13/cobra"

	"mcp-runtime/internal/cli/core"
)

// New returns the ops command.
func New(runtime *core.Runtime) *cobra.Command {
	return NewWithManager(DefaultManager(runtime))
}

// NewWithManager returns the ops command using the provided manager.
func NewWithManager(mgr *Manager) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ops",
		Short: "Operate the bundled platform stack (admin only)",
		Long:  "Inspect and operate the bundled platform analytics, gateway, and observability stack. These commands require admin/operator Kubernetes access with kubectl; normal users should use the platform API and dashboard instead.",
	}

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show platform stack status",
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.ShowStatus()
		},
	}

	var follow bool
	var previous bool
	var tail int
	var since string
	logsCmd := &cobra.Command{
		Use:       "logs [component]",
		Short:     "View logs for a platform component",
		Args:      cobra.ExactArgs(1),
		ValidArgs: ComponentKeys(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.ViewLogs(args[0], follow, previous, tail, since)
		},
	}
	logsCmd.Flags().BoolVar(&follow, "follow", false, "Follow log output")
	logsCmd.Flags().BoolVar(&previous, "previous", false, "Show logs from the previous container instance")
	logsCmd.Flags().IntVar(&tail, "tail", 200, "Number of recent log lines to show (-1 for all)")
	logsCmd.Flags().StringVar(&since, "since", "", "Only return logs newer than a relative duration like 5m or 1h")

	eventsCmd := &cobra.Command{
		Use:   "events",
		Short: "Show recent Kubernetes events for the platform namespaces",
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.ShowEvents()
		},
	}

	var localPort int
	var address string
	portForwardCmd := &cobra.Command{
		Use:   "port-forward [target]",
		Short: "Port-forward a common platform service",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.PortForwardTarget(args[0], localPort, address)
		},
	}
	portForwardCmd.Flags().IntVar(&localPort, "port", 0, "Local port to bind (defaults to the target service port)")
	portForwardCmd.Flags().StringVar(&address, "address", "127.0.0.1", "Addresses to listen on")

	var restartAll bool
	restartCmd := &cobra.Command{
		Use:   "restart [component]",
		Short: "Restart one or all platform workloads",
		Args: func(cmd *cobra.Command, args []string) error {
			if restartAll && len(args) == 0 {
				return nil
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			component := ""
			if len(args) > 0 {
				component = args[0]
			}
			return mgr.Restart(component, restartAll)
		},
	}
	restartCmd.Flags().BoolVar(&restartAll, "all", false, "Restart every platform workload")

	grafanaCmd := &cobra.Command{
		Use:   "grafana",
		Short: "Diagnose and recover bundled Grafana admin credentials",
		Long:  "Diagnose and recover the bundled Grafana admin account. The platform ingress gate and Grafana's own login are separate layers: these commands probe from inside the Grafana pod, so they report only the Grafana login layer.",
	}
	grafanaCheckCmd := &cobra.Command{
		Use:   "check",
		Short: "Check that Grafana accepts the configured admin credentials (read-only)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.ShowGrafanaCheck()
		},
	}
	var resetYes bool
	grafanaResetCmd := &cobra.Command{
		Use:   "reset-admin-password",
		Short: "Back up Grafana and reset the persisted admin password to the configured value",
		Long:  "Reset the persisted Grafana admin password to the value in mcp-grafana-credentials. Runs only when drift is detected, backs up the Grafana database first, passes the password to the Grafana CLI over stdin inside the pod, and verifies authenticated API access afterwards. Dashboards and datasources are preserved.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.ResetGrafanaAdminPassword(resetYes)
		},
	}
	grafanaResetCmd.Flags().BoolVar(&resetYes, "yes", false, "Confirm the backup and reset")
	grafanaCmd.AddCommand(grafanaCheckCmd, grafanaResetCmd)

	cmd.AddCommand(statusCmd, logsCmd, eventsCmd, portForwardCmd, restartCmd, grafanaCmd)
	return cmd
}
