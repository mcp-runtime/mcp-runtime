// Package cluster owns routing for the cluster top-level command.
package cluster

import (
	"github.com/spf13/cobra"

	"mcp-runtime/internal/cli/core"
)

// New returns the cluster command.
func New(runtime *core.Runtime) *cobra.Command {
	return NewWithManager(NewClusterManager(runtime.KubectlClient(), runtime.Executor(), runtime.Logger()))
}

// NewWithManager returns the cluster command using the provided manager.
func NewWithManager(mgr *ClusterManager) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cluster",
		Short: "Manage Kubernetes cluster",
		Long:  "Commands for managing the Kubernetes cluster",
	}

	var kubeconfig string
	var context string
	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize cluster configuration",
		Long:  "Initialize and configure the Kubernetes cluster for MCP platform",
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.InitCluster(kubeconfig, context)
		},
	}
	initCmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig file (default: ~/.kube/config; auto-detects /etc/rancher/k3s/k3s.yaml on k3s hosts)")
	initCmd.Flags().StringVar(&context, "context", "", "Kubernetes context to use")

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Check cluster status",
		Long:  "Check the status of the Kubernetes cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.CheckClusterStatus()
		},
	}

	var ingressMode string
	var ingressManifest string
	var forceIngressInstall bool
	var configKubeconfig string
	var configContext string
	var provider string
	var region string
	var clusterName string
	var resourceGroup string
	var project string
	var zone string
	configCmd := &cobra.Command{
		Use:   "config",
		Short: "Configure cluster settings",
		Long:  "Configure cluster settings like ingress and kubeconfig context",
		RunE: func(cmd *cobra.Command, args []string) error {
			if provider != "" {
				if err := mgr.ConfigureKubeconfigFromProvider(provider, region, clusterName, resourceGroup, project, zone, configKubeconfig); err != nil {
					return err
				}
			}
			if configKubeconfig != "" || configContext != "" || provider != "" {
				if err := mgr.ConfigureKubeconfig(configKubeconfig, configContext); err != nil {
					return err
				}
			}
			return mgr.ConfigureClusterWithValues(ingressMode, ingressManifest, forceIngressInstall)
		},
	}
	configCmd.Flags().StringVar(&ingressMode, "ingress", "traefik", "Ingress controller to install (traefik|none)")
	configCmd.Flags().StringVar(&ingressManifest, "ingress-manifest", "config/ingress/overlays/prod", "Manifest to apply when installing the ingress controller")
	configCmd.Flags().BoolVar(&forceIngressInstall, "force-ingress-install", false, "Force repo-managed ingress install when only an IngressClass exists; refuses active external Traefik")
	configCmd.Flags().StringVar(&configKubeconfig, "kubeconfig", "", "Path to kubeconfig file (default: ~/.kube/config; auto-detects /etc/rancher/k3s/k3s.yaml on k3s hosts)")
	configCmd.Flags().StringVar(&configContext, "context", "", "Kubernetes context to use")
	configCmd.Flags().StringVar(&provider, "provider", "", "Cloud provider for kubeconfig (eks; aks/gke planned)")
	configCmd.Flags().StringVar(&region, "region", "us-west-1", "Region for cloud provider kubeconfig")
	configCmd.Flags().StringVar(&clusterName, "name", "mcp-runtime", "Cluster name for cloud provider kubeconfig")
	configCmd.Flags().StringVar(&resourceGroup, "resource-group", "", "Resource group (AKS, planned)")
	configCmd.Flags().StringVar(&project, "project", "", "Project ID (GKE, planned)")
	configCmd.Flags().StringVar(&zone, "zone", "", "Zone (GKE, planned)")

	var provisionProvider string
	var provisionRegion string
	var nodeCount int
	var provisionClusterName string
	var provisionDryRun bool
	provisionCmd := &cobra.Command{
		Use:   "provision",
		Short: "Provision a new cluster",
		Long:  "Provision a new Kubernetes cluster (requires cloud provider credentials)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.ProvisionCluster(provisionProvider, provisionRegion, nodeCount, provisionClusterName, provisionDryRun)
		},
	}
	provisionCmd.Flags().StringVar(&provisionProvider, "provider", "kind", "Cloud provider (kind, gke, eks, aks)")
	provisionCmd.Flags().StringVar(&provisionRegion, "region", "us-west-1", "Region for cluster")
	provisionCmd.Flags().IntVar(&nodeCount, "nodes", 3, "Number of nodes")
	provisionCmd.Flags().StringVar(&provisionClusterName, "name", "mcp-runtime", "Cluster name (used by supported providers)")
	provisionCmd.Flags().BoolVar(&provisionDryRun, "dry-run", false, "Print the cluster config and command that would run without creating any cluster")

	cmd.AddCommand(initCmd)
	cmd.AddCommand(statusCmd)
	cmd.AddCommand(configCmd)
	cmd.AddCommand(provisionCmd)
	cmd.AddCommand(newClusterCertCmd(mgr))
	cmd.AddCommand(newClusterDoctorCmd(mgr))
	cmd.AddCommand(newMCPAuthConnectorCmd())
	cmd.AddCommand(newOperatorRBACCmd())
	cmd.AddCommand(newClusterDiagnosticsCmd(mgr))
	return cmd
}
