// Package registry owns routing for the registry top-level command.
package registry

import (
	"github.com/spf13/cobra"

	"mcp-runtime/internal/cli/core"
)

// New returns the registry command.
func New(runtime *core.Runtime) *cobra.Command {
	return NewWithManager(NewRegistryManager(runtime.KubectlClient(), runtime.Executor(), runtime.Logger()))
}

// NewWithManager returns the registry command using the provided manager.
func NewWithManager(mgr *RegistryManager) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "registry",
		Short: "Manage container registry",
		Long:  "Commands for managing the container registry",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	var namespace string
	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Check registry status",
		Long:  "Check the status of the container registry",
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.CheckRegistryStatus(namespace)
		},
	}
	statusCmd.Flags().StringVar(&namespace, "namespace", core.NamespaceRegistry, "Registry namespace")

	infoCmd := &cobra.Command{
		Use:   "info",
		Short: "Show registry information",
		Long:  "Show registry URL and connection information",
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.ShowRegistryInfo()
		},
	}

	var url string
	var username string
	var password string
	var operatorImage string
	var provisionDryRun bool
	provisionCmd := &cobra.Command{
		Use:   "provision",
		Short: "Configure an external registry",
		Long:  "Configure an external registry to be used for operator/runtime images",
		RunE: func(cmd *cobra.Command, args []string) error {
			return RunRegistryProvision(mgr, url, username, password, operatorImage, provisionDryRun)
		},
	}
	provisionCmd.Flags().StringVar(&url, "url", "", "External registry URL (e.g., registry.mcpruntime.com)")
	provisionCmd.Flags().StringVar(&username, "username", "", "Registry username (optional)")
	provisionCmd.Flags().StringVar(&password, "password", "", "Registry password (optional)")
	provisionCmd.Flags().StringVar(&operatorImage, "operator-image", "", "Optional: build and push operator image to this external registry (e.g., <registry>/mcp-runtime-operator:latest)")
	provisionCmd.Flags().BoolVar(&provisionDryRun, "dry-run", false, "Print what would be done without saving config, logging in, or pushing images")

	var nativeOpts nativeAuthOptions
	secureCmd := &cobra.Command{Use: "enable-auth", Short: "Enable repository-scoped authentication on the bundled registry", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return mgr.enableNativeAuth(cmd.Context(), nativeOpts) }}
	secureCmd.Flags().StringVar(&nativeOpts.Realm, "realm", "", "Public HTTPS platform token URL ending /api/v1/registry/token")
	secureCmd.Flags().BoolVar(&nativeOpts.DryRun, "dry-run", false, "Inspect targets without changing resources or creating credentials")
	secureCmd.Flags().BoolVar(&nativeOpts.TestMode, "test-mode", false, "Allow a local HTTP token realm for disposable tests")
	secureCmd.Flags().StringSliceVar(&nativeOpts.Namespaces, "pull-namespace", nil, "Runtime namespaces to provision (default: all existing managed namespaces)")
	secureCmd.Flags().BoolVar(&nativeOpts.AllowBundledBroker, "allow-bundled-broker", false, "Accept a platform-api image served by this registry (new broker pulls then need a running broker)")
	cmd.AddCommand(statusCmd, infoCmd, provisionCmd, secureCmd)
	return cmd
}
