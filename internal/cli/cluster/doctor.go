package cluster

import (
	"github.com/spf13/cobra"

	clusterdoctor "mcp-runtime/internal/cli/cluster/doctor"
	"mcp-runtime/internal/cli/core"
)

func newClusterDoctorCmd(mgr *ClusterManager) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check whether a cluster is ready for MCP Runtime setup",
		Long:  "Run pre-setup checks for Kubernetes nodes, storage, ingress, public DNS, TLS, and cluster prerequisites before installing MCP Runtime. See docs/cluster-readiness.md for the full checklist.",
		RunE: func(cmd *cobra.Command, args []string) error {
			report := clusterdoctor.RunSetupDoctorAndPrint(mgr.KubectlRunner())
			if !report.AllOK() {
				return core.NewSetupStepFailedError()
			}
			return nil
		},
	}
	return cmd
}

func newClusterDiagnosticsCmd(mgr *ClusterManager) *cobra.Command {
	return &cobra.Command{
		Use:   "diagnostics",
		Short: "Diagnose an installed MCP Runtime cluster",
		Long:  "Run post-setup diagnostics for the Kubernetes distribution, MCP Runtime services, registry, image pulls, Sentinel dependencies, authentication, and MCPServer reconciliation. Each failure includes a targeted remediation. Setup already runs a shorter operational smoke gate (nodes, PVCs, Postgres, platform-api, Sentinel rollouts, auth probe); use this command for deeper follow-up.",
		RunE: func(cmd *cobra.Command, args []string) error {
			report := clusterdoctor.RunDoctorAndPrint(mgr.KubectlRunner())
			if !report.AllOK() {
				return core.NewSetupStepFailedError()
			}
			return nil
		},
	}
}
