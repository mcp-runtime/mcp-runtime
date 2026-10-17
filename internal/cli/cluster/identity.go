package cluster

import (
	"strings"

	"github.com/spf13/cobra"

	clusterdoctor "mcp-runtime/internal/cli/cluster/doctor"
	"mcp-runtime/internal/cli/core"
)

func newClusterIdentityCmd(mgr *ClusterManager) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "identity",
		Short: "Inspect workload adapter identity health",
		Long:  "Read the deployed workload issuer, SPIFFE trust-domain configuration, public CA metadata, and selected server route client-certificate trust without changing cluster resources.",
	}
	var server string
	status := &cobra.Command{
		Use:   "status",
		Short: "Check workload issuer and trust-domain health",
		Long:  "Report operator/runtime API configuration, workload ClusterIssuer readiness, CA expiry and fingerprints, and whether the selected server route requests client certificates. Secret private keys and credentials are never read or printed.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			parts := strings.Split(server, "/")
			if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
				return core.NewWithBase(core.ErrFieldRequired, "--server is required in <namespace>/<name> form")
			}
			check := clusterdoctor.CheckWorkloadIdentity(mgr.KubectlRunner(), parts[0], parts[1])
			core.Section("Workload adapter identity")
			if check.OK {
				core.Success(check.Name + " — " + check.Detail)
				return nil
			}
			core.Error(check.Name + " — " + check.Detail)
			if check.Remedy != "" {
				core.Info("  Remedy: " + check.Remedy)
			}
			return core.NewSetupStepFailedError()
		},
	}
	status.Flags().StringVar(&server, "server", "", "Selected MCPServer as <namespace>/<name>")
	_ = status.MarkFlagRequired("server")
	cmd.AddCommand(status)
	return cmd
}
