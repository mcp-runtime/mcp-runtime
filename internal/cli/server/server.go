// Package server owns routing for the server top-level command.
package server

import (
	"fmt"
	"math"
	"strings"

	"github.com/spf13/cobra"

	"mcp-runtime/internal/cli/core"
	registrycli "mcp-runtime/internal/cli/registry"
	"mcp-runtime/pkg/mcpdefaults"
	"mcp-runtime/pkg/metadata"
)

// New returns the server command.
func New(runtime *core.Runtime) *cobra.Command {
	return NewWithManager(NewServerManager(runtime.KubectlClient(), runtime.Logger()))
}

// NewWithManager returns the server command using the provided manager.
func NewWithManager(mgr *ServerManager) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Manage MCP servers",
		Long: `Commands for managing MCP server deployments.

With mcp-runtime auth login, supported commands use the platform API by default
for normal user and admin workflows. Use --use-kube only for admin/dev/test
direct Kubernetes operations; it requires kubectl plus admin/operator kubeconfig
and RBAC access.

Create, apply, export, patch, and logs are direct Kubernetes operations and
require --use-kube. For platform workflows, use mcp-runtime auth login
--api-url <platform-url> and platform-backed commands such as list, deploy,
delete, status, and policy.

For building images from source, use 'server build'.
For pushing MCP server images, use 'server push'.`,
	}

	mgr.BindUseKubeFlag(cmd)

	var initMetadataDir string
	var initImage string
	var initTag string
	var initScope string
	var initPolicyMode string
	var initDefaultDecision string
	var initSessionRequired bool
	var initPort int32
	var initTools []string
	var initToolSpecs []string
	var initToolRisk string
	var initForce bool
	var initFromServer string
	initCmd := &cobra.Command{
		Use:   "init [name]",
		Short: "Initialize .mcp metadata for a server",
		Long:  "Initialize .mcp/servers.yaml metadata for a server. Use --tool flags to seed governed tool metadata, or --from-server to discover tools from a running MCP server automatically. Then build, push, and deploy with server build image, server push, and server deploy.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if initFromServer != "" {
				core.Info(fmt.Sprintf("Discovering tools from %s ...", initFromServer))
				discovered, err := DiscoverToolsFromServer(initFromServer)
				if err != nil {
					return fmt.Errorf("--from-server %s: %w", initFromServer, err)
				}
				if len(discovered) == 0 {
					core.Warn("No tools returned by the server; metadata will have an empty tool list")
				} else {
					core.Info(fmt.Sprintf("Discovered %d tool(s): %s", len(discovered), strings.Join(discovered, ", ")))
					// Merge: discovered tools first, then any manually specified --tool
					// flags that are not already in the discovered set.
					manualSet := make(map[string]struct{}, len(initTools))
					for _, t := range discovered {
						manualSet[t] = struct{}{}
					}
					merged := append([]string{}, discovered...)
					for _, t := range initTools {
						if _, exists := manualSet[strings.TrimSpace(t)]; !exists {
							merged = append(merged, t)
						}
					}
					initTools = merged
				}
			}
			return mgr.InitServer(args[0], initMetadataDir, initImage, initTag, initScope, initPolicyMode, initDefaultDecision, initSessionRequired, initPort, initTools, initToolSpecs, initToolRisk, initForce)
		},
	}
	initCmd.Flags().StringVar(&initMetadataDir, "metadata-dir", ".mcp", "Directory where servers.yaml will be written")
	initCmd.Flags().StringVar(&initImage, "image", "", "Container image repository (default: server name)")
	initCmd.Flags().StringVar(&initTag, "tag", "latest", "Container image tag")
	initCmd.Flags().StringVar(&initScope, "scope", "tenant", "Publish scope: tenant, org, or public")
	initCmd.Flags().StringVar(&initPolicyMode, "policy-mode", string(metadata.PolicyModeAllowList), "Policy mode: allow-list or observe")
	initCmd.Flags().StringVar(&initDefaultDecision, "default-decision", string(metadata.PolicyDecisionDeny), "Default policy decision: allow or deny")
	initCmd.Flags().BoolVar(&initSessionRequired, "session-required", true, "Require adapter-issued agent sessions")
	initCmd.Flags().Int32Var(&initPort, "port", defaultDeployPort(), "Container port")
	initCmd.Flags().StringArrayVar(&initTools, "tool", nil, "Tool name to add with read side-effect metadata; repeat for multiple tools")
	initCmd.Flags().StringArrayVar(&initToolSpecs, "tool-spec", nil, "Tool metadata as name:low|medium|high:read|write|destructive; repeat for mixed trust or side effects")
	initCmd.Flags().StringVar(&initToolRisk, "tool-risk", "", "Informational risk level for seeded tools: low, medium, or high")
	initCmd.Flags().BoolVar(&initForce, "force", false, "Replace an existing metadata entry with the same server name")
	initCmd.Flags().StringVar(&initFromServer, "from-server", "", "Discover tools by calling tools/list on a running MCP server at this URL (e.g. http://localhost:8088); discovered names are merged with --tool flags")

	var namespace string
	var team string
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List MCP servers",
		Long: `List MCP servers visible to the signed-in user: public and organization
catalogs where enabled, the personal namespace, and the user's teams.
Admins see all servers. No filter is required.

Use --team <slug> to list one authorized team (for example, acme, not
mcp-team-acme). Use --namespace to select a catalog or personal namespace
directly; the platform API still checks read access. --team and --namespace
are mutually exclusive.

With --use-kube, --namespace selects a Kubernetes namespace (default:
mcp-servers); --team is unavailable.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.ListServers(namespace, team)
		},
	}
	listCmd.Flags().StringVar(&namespace, "namespace", "", "Catalog or personal namespace filter; Kubernetes namespace with --use-kube")
	listCmd.Flags().StringVar(&team, "team", "", "Filter to an authorized team slug (for example, acme; platform API only)")

	var getNamespace string
	getCmd := &cobra.Command{
		Use:   "get [name]",
		Short: "Get MCP server details",
		Long:  "Get detailed information about an MCP server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.GetServer(args[0], getNamespace)
		},
	}
	getCmd.Flags().StringVar(&getNamespace, "namespace", core.NamespaceMCPServers, "Namespace")

	var createNamespace string
	var image string
	var imageTag string
	var file string
	createCmd := &cobra.Command{
		Use:   "create [name]",
		Short: "Create an MCP server",
		Long:  "Create a new MCP server deployment",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if file != "" {
				return mgr.CreateServerFromFile(file)
			}
			return mgr.CreateServer(args[0], createNamespace, image, imageTag)
		},
	}
	createCmd.Flags().StringVar(&createNamespace, "namespace", core.NamespaceMCPServers, "Namespace")
	createCmd.Flags().StringVar(&image, "image", "", "Container image")
	createCmd.Flags().StringVar(&imageTag, "tag", "latest", "Image tag")
	createCmd.Flags().StringVar(&file, "file", "", "YAML file with server spec")

	var applyFile string
	applyCmd := &cobra.Command{
		Use:   "apply",
		Short: "Apply an MCP server manifest",
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.ApplyServerFromFile(applyFile)
		},
	}
	applyCmd.Flags().StringVar(&applyFile, "file", "", "YAML file with MCPServer manifest")
	_ = applyCmd.MarkFlagRequired("file")

	var deployNamespace string
	var deployTeam string
	var deployScope string
	var deployImage string
	var deployTag string
	var deployReplicas int32
	var deployPort int32
	var deployServicePort int32
	var deployMetadataFile string
	var deployMetadataDir string
	var deployUpdate bool
	deployCmd := &cobra.Command{
		Use:   "deploy [name]",
		Short: "Deploy an MCP server through the platform API",
		Long:  "Deploy a new MCPServer through the authenticated platform API identity. When .mcp metadata is present, deploy includes its image, tool side-effect metadata, prompts, resources, and tasks. If the server already exists, pass --update to redeploy it.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.DeployServer(args[0], deployNamespace, deployTeam, deployScope, deployImage, deployTag, deployReplicas, deployPort, deployServicePort, deployMetadataFile, deployMetadataDir, deployUpdate)
		},
	}
	deployCmd.Flags().StringVar(&deployNamespace, "namespace", "", "Target namespace (optional when --team is provided)")
	deployCmd.Flags().StringVar(&deployTeam, "team", "", "Team slug to resolve target namespace")
	deployCmd.Flags().StringVar(&deployScope, "scope", "", "Publish scope: tenant, org, or public")
	deployCmd.Flags().StringVar(&deployImage, "image", "", "Container image repository (optional when .mcp metadata provides image)")
	deployCmd.Flags().StringVar(&deployTag, "tag", "latest", "Container image tag")
	deployCmd.Flags().Int32Var(&deployReplicas, "replicas", 1, "Replica count")
	deployCmd.Flags().Int32Var(&deployPort, "port", defaultDeployPort(), "Container port")
	deployCmd.Flags().Int32Var(&deployServicePort, "service-port", 80, "Service port")
	deployCmd.Flags().StringVar(&deployMetadataFile, "metadata-file", "", "Path to .mcp metadata file")
	deployCmd.Flags().StringVar(&deployMetadataDir, "metadata-dir", ".mcp", "Directory containing .mcp metadata files")
	deployCmd.Flags().BoolVar(&deployUpdate, "update", false, "Update an existing server instead of failing when the name already exists")

	var generateMetadataFile string
	var generateMetadataDir string
	var generateOutputDir string
	generateCmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate MCPServer manifests from .mcp metadata",
		Long:  "Generate MCPServer YAML manifests from .mcp metadata. Normal user deploys should use server deploy directly; this command is for review, GitOps, and admin workflows that need YAML output.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.GenerateManifests(generateMetadataFile, generateMetadataDir, generateOutputDir)
		},
	}
	generateCmd.Flags().StringVar(&generateMetadataFile, "metadata-file", "", "Path to .mcp metadata file")
	generateCmd.Flags().StringVar(&generateMetadataDir, "metadata-dir", ".mcp", "Directory containing .mcp metadata files")
	generateCmd.Flags().StringVar(&generateOutputDir, "output", "manifests", "Output directory for generated MCPServer manifests")

	var exportNamespace string
	var exportFile string
	exportCmd := &cobra.Command{
		Use:   "export [name]",
		Short: "Export an MCP server manifest",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.ExportServer(args[0], exportNamespace, exportFile)
		},
	}
	exportCmd.Flags().StringVar(&exportNamespace, "namespace", core.NamespaceMCPServers, "Namespace")
	exportCmd.Flags().StringVar(&exportFile, "file", "", "Write the manifest to a file instead of stdout")

	var patchNamespace string
	var patchType string
	var patch string
	var patchFile string
	patchCmd := &cobra.Command{
		Use:   "patch [name]",
		Short: "Patch an MCP server manifest",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.PatchServer(args[0], patchNamespace, patchType, patch, patchFile)
		},
	}
	patchCmd.Flags().StringVar(&patchNamespace, "namespace", core.NamespaceMCPServers, "Namespace")
	patchCmd.Flags().StringVar(&patchType, "type", "merge", "Patch type (merge|json|strategic)")
	patchCmd.Flags().StringVar(&patch, "patch", "", "Inline JSON/YAML patch document")
	patchCmd.Flags().StringVar(&patchFile, "patch-file", "", "Path to a JSON/YAML patch document")

	var deleteNamespace string
	deleteCmd := &cobra.Command{
		Use:   "delete [name]",
		Short: "Delete an MCP server",
		Long:  "Delete an MCP server deployment",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.DeleteServer(args[0], deleteNamespace)
		},
	}
	deleteCmd.Flags().StringVar(&deleteNamespace, "namespace", core.NamespaceMCPServers, "Namespace")

	var logsNamespace string
	var follow bool
	var previous bool
	var tail int
	var since string
	logsCmd := &cobra.Command{
		Use:   "logs [name]",
		Short: "View server logs",
		Long:  "View logs from an MCP server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.ViewServerLogs(args[0], logsNamespace, follow, previous, tail, since)
		},
	}
	logsCmd.Flags().StringVar(&logsNamespace, "namespace", core.NamespaceMCPServers, "Namespace")
	logsCmd.Flags().BoolVar(&follow, "follow", false, "Follow log output")
	logsCmd.Flags().BoolVar(&previous, "previous", false, "Show logs from the previous container instance")
	logsCmd.Flags().IntVar(&tail, "tail", 200, "Number of recent log lines to show (-1 for all)")
	logsCmd.Flags().StringVar(&since, "since", "", "Only return logs newer than a relative duration like 5m or 1h")

	var statusNamespace string
	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show MCP server runtime status (pods, images, pull secrets)",
		Long:  "List MCPServer resources with their Deployment/pod status, image, and pull secrets.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.ServerStatus(statusNamespace)
		},
	}
	statusCmd.Flags().StringVar(&statusNamespace, "namespace", core.NamespaceMCPServers, "Namespace to inspect")

	var connectNamespace string
	var connectClient string
	var connectOutput string
	connectCmd := &cobra.Command{
		Use:   "connect-config [name]",
		Short: "Print client connection config for an MCP server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.ConnectConfig(args[0], connectNamespace, connectClient, connectOutput)
		},
	}
	connectCmd.Flags().StringVar(&connectNamespace, "namespace", core.NamespaceMCPServers, "Namespace")
	connectCmd.Flags().StringVar(&connectClient, "client", "json", "Client config: claude, cursor, vscode, or json")
	connectCmd.Flags().StringVarP(&connectOutput, "output", "o", "text", "Output format: text, json, or yaml")

	var policyNamespace string
	policyCmd := &cobra.Command{
		Use:   "policy",
		Short: "Inspect rendered gateway policy for an MCP server",
	}
	inspectCmd := &cobra.Command{
		Use:   "inspect [name]",
		Short: "Show the rendered gateway policy document for a server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.InspectServerPolicy(args[0], policyNamespace)
		},
	}
	inspectCmd.Flags().StringVar(&policyNamespace, "namespace", core.NamespaceMCPServers, "Namespace")
	policyCmd.AddCommand(inspectCmd)

	buildCmd := &cobra.Command{
		Use:   "build",
		Short: "Build MCP server images (push via `server push`)",
	}
	buildCmd.AddCommand(newBuildImageCmd(mgr.Logger()))

	var pushImage string
	var pushName string
	var pushScope string
	pushCmd := &cobra.Command{
		Use:   "push",
		Short: "Push a server image through the platform API",
		Long:  "Save a local Docker image and publish it through the authenticated platform registry API. Supports tenant, org, and public scopes.",
		RunE: func(cmd *cobra.Command, args []string) error {
			executor := mgr.exec
			if executor == nil {
				executor = core.DefaultExecutor()
			}
			registryManager := registrycli.NewRegistryManager(mgr.kubectl, executor, mgr.logger)
			return registrycli.RunRegistryPush(cmd.Context(), registryManager, pushImage, "", pushName, pushScope)
		},
	}
	pushCmd.Flags().StringVar(&pushImage, "image", "", "Local image to push (required)")
	pushCmd.Flags().StringVar(&pushName, "name", "", "Override target repository/name")
	pushCmd.Flags().StringVar(&pushScope, "scope", "", "Publish scope: tenant, org, or public")

	cmd.AddCommand(initCmd, listCmd, getCmd, createCmd, applyCmd, deployCmd, generateCmd, exportCmd, patchCmd, deleteCmd, logsCmd, statusCmd, connectCmd, policyCmd, buildCmd, pushCmd, newValidateCmd())
	return cmd
}

func defaultDeployPort() int32 {
	port := core.GetDefaultServerPort()
	if port <= 0 || port > math.MaxInt32 {
		return int32(mcpdefaults.MCPServerPort)
	}
	return int32(port)
}
