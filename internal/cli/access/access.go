// Package access owns routing for the access top-level command.
package access

import (
	"github.com/spf13/cobra"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/pkg/mcpdefaults"
)

// New returns the access command.
func New(runtime *core.Runtime) *cobra.Command {
	return NewWithManager(DefaultAccessManager(runtime))
}

// NewWithManager returns the access command using the provided manager.
func NewWithManager(mgr *AccessManager) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "access",
		Short: "Manage grants and agent sessions",
		Long: `Commands for managing MCPAccessGrant and MCPAgentSession resources that feed the gateway policy layer.

With mcp-runtime auth login, commands use the platform API by default for normal
user and admin workflows. Use --use-kube only for admin/dev/test direct
Kubernetes operations; it requires kubectl plus admin/operator kubeconfig and
RBAC access. For platform workflows, run mcp-runtime auth login --api-url
<platform-url> and stay on the platform API path.`,
	}

	mgr.BindUseKubeFlag(cmd)

	cmd.AddCommand(newGrantCmd(mgr), newSessionCmd(mgr), newExplainCmd(mgr))
	return cmd
}

func newGrantCmd(mgr *AccessManager) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "grant",
		Short: "Manage MCPAccessGrant resources",
	}
	cmd.AddCommand(newGrantInitCmd(mgr))
	cmd.AddCommand(newListCmd(mgr, GrantResource, "grants"))
	cmd.AddCommand(newGetCmd(mgr, GrantResource, "grant"))
	cmd.AddCommand(newApplyCmd(mgr, "grant"))
	cmd.AddCommand(newDeleteCmd(mgr, GrantResource, "grant"))
	cmd.AddCommand(newToggleCmd(mgr, GrantResource, "disable", "Disable a grant", true))
	cmd.AddCommand(newToggleCmd(mgr, GrantResource, "enable", "Enable a grant", false))
	cmd.AddCommand(newRevokeGrantSessionsCmd(mgr))
	return cmd
}

func newRevokeGrantSessionsCmd(mgr *AccessManager) *cobra.Command {
	var namespace string
	cmd := &cobra.Command{
		Use:   "revoke-sessions [grant-name]",
		Short: "Revoke every session issued from a grant",
		Long:  "Revoke active agent sessions linked to this grant while leaving the grant itself enabled. This action uses the platform API and records audit events.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.RevokeGrantSessions(args[0], namespace)
		},
	}
	cmd.Flags().StringVar(&namespace, "namespace", core.NamespaceMCPServers, "Grant namespace")
	return cmd
}

func newSessionCmd(mgr *AccessManager) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Manage MCPAgentSession resources",
	}
	cmd.AddCommand(newSessionInitCmd(mgr))
	cmd.AddCommand(newListCmd(mgr, SessionResource, "sessions"))
	cmd.AddCommand(newGetCmd(mgr, SessionResource, "session"))
	cmd.AddCommand(newApplyCmd(mgr, "session"))
	cmd.AddCommand(newDeleteCmd(mgr, SessionResource, "session"))
	cmd.AddCommand(newToggleCmd(mgr, SessionResource, "revoke", "Revoke an agent session", true))
	cmd.AddCommand(newToggleCmd(mgr, SessionResource, "unrevoke", "Clear the revoked flag on an agent session", false))
	return cmd
}

func newGrantInitCmd(mgr *AccessManager) *cobra.Command {
	opts := accessManifestInitOptions{}
	cmd := &cobra.Command{
		Use:   "init [name]",
		Short: "Initialize an MCPAccessGrant manifest",
		Long:  "Initialize an MCPAccessGrant YAML manifest for review or apply. Use --tool to seed allow rules and --side-effect to set the permitted side-effect classes.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Name = args[0]
			return mgr.InitGrantManifest(opts)
		},
	}
	bindAccessInitCommonFlags(cmd, &opts)
	cmd.Flags().StringArrayVar(&opts.SideEffects, "side-effect", []string{"read"}, "Allowed side effect class: read, write, or destructive; repeat for multiple")
	cmd.Flags().StringArrayVar(&opts.Tools, "tool", nil, "Tool name to allow; repeat for multiple tools")
	cmd.Flags().StringArrayVar(&opts.ToolRules, "tool-rule", nil, "Tool rule as name:allow|deny:low|medium|high; repeat for mixed trust or deny rules")
	cmd.Flags().StringVar(&opts.ExpiresAt, "expires-at", "", "Optional RFC3339 expiry timestamp")
	cmd.Flags().StringVar(&opts.ExpiresIn, "expires-in", "", "Optional relative expiry duration, for example 1h or 30m")
	cmd.Flags().StringVar(&opts.Output, "output", "grant.yaml", "Output manifest path")
	return cmd
}

func newSessionInitCmd(mgr *AccessManager) *cobra.Command {
	opts := accessManifestInitOptions{}
	cmd := &cobra.Command{
		Use:   "init [name]",
		Short: "Initialize an MCPAgentSession manifest",
		Long:  "Initialize an MCPAgentSession YAML manifest for review or apply. Adapter auto-refresh usually creates sessions automatically; this command is for explicit/admin session manifests.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Name = args[0]
			return mgr.InitSessionManifest(opts)
		},
	}
	bindAccessInitCommonFlags(cmd, &opts)
	cmd.Flags().StringVar(&opts.UpstreamSecretName, "upstream-token-secret", "", "Optional Secret name containing an upstream token")
	cmd.Flags().StringVar(&opts.UpstreamSecretKey, "upstream-token-key", "token", "Secret key for --upstream-token-secret")
	cmd.Flags().StringVar(&opts.ExpiresAt, "expires-at", "", "Optional RFC3339 expiry timestamp")
	cmd.Flags().StringVar(&opts.ExpiresIn, "expires-in", "", "Optional relative expiry duration, for example 1h or 30m")
	cmd.Flags().BoolVar(&opts.Revoked, "revoked", false, "Initialize the session as revoked")
	cmd.Flags().StringVar(&opts.Output, "output", "session.yaml", "Output manifest path")
	return cmd
}

func bindAccessInitCommonFlags(cmd *cobra.Command, opts *accessManifestInitOptions) {
	cmd.Flags().StringVar(&opts.Namespace, "namespace", core.NamespaceMCPServers, "Manifest namespace")
	cmd.Flags().StringVar(&opts.Server, "server", "", "Target MCPServer name")
	cmd.Flags().StringVar(&opts.ServerNamespace, "server-namespace", "", "Target MCPServer namespace (default: --namespace)")
	cmd.Flags().StringVar(&opts.HumanID, "human-id", "", "Human subject ID")
	cmd.Flags().StringVar(&opts.AgentID, "agent-id", "", "Agent subject ID")
	cmd.Flags().StringVar(&opts.TeamID, "team-id", "", "Team subject ID")
	cmd.Flags().StringVar(&opts.Trust, "trust", "low", "Trust level: low, medium, or high")
	cmd.Flags().StringVar(&opts.PolicyVersion, "policy-version", mcpdefaults.PolicyVersion, "Policy version")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "Replace output file if it already exists")
	_ = cmd.MarkFlagRequired("server")
}

func newListCmd(mgr *AccessManager, resource, label string) *cobra.Command {
	var namespace string
	var allNamespaces bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List access " + label,
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.ListAccessResources(resource, namespace, allNamespaces)
		},
	}
	cmd.Flags().StringVar(&namespace, "namespace", "", "Namespace to inspect")
	cmd.Flags().BoolVar(&allNamespaces, "all-namespaces", true, "List resources across all namespaces when no namespace is specified")
	return cmd
}

func newGetCmd(mgr *AccessManager, resource, label string) *cobra.Command {
	var namespace string
	cmd := &cobra.Command{
		Use:   "get [name]",
		Short: "Get an access " + label,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.GetAccessResource(resource, args[0], namespace)
		},
	}
	cmd.Flags().StringVar(&namespace, "namespace", core.NamespaceMCPServers, "Namespace")
	return cmd
}

func newApplyCmd(mgr *AccessManager, label string) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Apply a " + label + " manifest",
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.ApplyAccessResource(file)
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "Manifest file to apply")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

func newDeleteCmd(mgr *AccessManager, resource, label string) *cobra.Command {
	var namespace string
	cmd := &cobra.Command{
		Use:   "delete [name]",
		Short: "Delete an access " + label,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.DeleteAccessResource(resource, args[0], namespace)
		},
	}
	cmd.Flags().StringVar(&namespace, "namespace", core.NamespaceMCPServers, "Namespace")
	return cmd
}

func newToggleCmd(mgr *AccessManager, resource, use, short string, value bool) *cobra.Command {
	var namespace string
	cmd := &cobra.Command{
		Use:   use + " [name]",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.ToggleAccessResource(resource, args[0], namespace, value)
		},
	}
	cmd.Flags().StringVar(&namespace, "namespace", core.NamespaceMCPServers, "Namespace")
	return cmd
}
