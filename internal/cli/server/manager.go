package server

// This file implements the "server" command for managing MCP server resources.
// It handles creating, listing, viewing, and deleting MCPServer custom resources.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/kube"
	"mcp-runtime/internal/cli/kubeerr"
	"mcp-runtime/internal/cli/platformapi"
	registrycli "mcp-runtime/internal/cli/registry"
	"mcp-runtime/pkg/mcpdefaults"
	"mcp-runtime/pkg/metadata"
	"mcp-runtime/pkg/publishscope"
)

// ServerManager handles MCP server operations with injected dependencies.
type ServerManager struct {
	kubectl *core.KubectlClient
	exec    core.Executor
	logger  *zap.Logger
	// useKube forces direct Kubernetes mode; when false, supported commands require platform API auth.
	useKube bool
}

// NewServerManager creates a ServerManager with the given dependencies.
func NewServerManager(kubectl *core.KubectlClient, logger *zap.Logger) *ServerManager {
	return &ServerManager{
		kubectl: kubectl,
		exec:    core.DefaultExecutor(),
		logger:  logger,
	}
}

// DefaultServerManager returns a ServerManager using the default kubectl client.
func DefaultServerManager(logger *zap.Logger) *ServerManager {
	return NewServerManager(core.DefaultKubectlClient(), logger)
}

// BindUseKubeFlag wires the shared --use-kube flag onto the command.
func (m *ServerManager) BindUseKubeFlag(cmd *cobra.Command) {
	cmd.PersistentFlags().BoolVar(&m.useKube, "use-kube", false, "Use direct Kubernetes mode with kubectl; requires admin/operator cluster access (admin/dev/test only)")
}

func (m *ServerManager) requireKubectlForMutation() error {
	if !m.useKube {
		return core.NewWithSentinel(nil, "this command requires `--use-kube` for direct Kubernetes mode. "+kubeerr.DirectModeGuidance)
	}
	return nil
}

// loadMetadataForRewrite validates a metadata file with the normal loader and
// returns its entries as written. Commands that rewrite .mcp metadata (init,
// build image) must not persist loader defaults: they would stamp servers that
// have no image with the registry.local placeholder, which no platform pulls.
func loadMetadataForRewrite(path string) (*metadata.RegistryFile, error) {
	if _, err := metadata.LoadFromFile(path); err != nil {
		return nil, err
	}
	// #nosec G304 -- path is the user-selected local .mcp metadata file.
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}
	var registry metadata.RegistryFile
	if err := yaml.Unmarshal(raw, &registry); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}
	return &registry, nil
}

func (m *ServerManager) InitServer(name, metadataDir, image, imageTag, scope, policyMode, defaultDecision string, sessionRequired bool, port int32, tools, toolSpecs []string, toolRisk string, force bool) error {
	name, err := validateManifestValue("name", name)
	if err != nil {
		return err
	}
	metadataDir = strings.TrimSpace(metadataDir)
	if metadataDir == "" {
		metadataDir = ".mcp"
	}
	image = strings.TrimSpace(image)
	if image == "" {
		image = name
	}
	image, err = validateManifestValue("image", image)
	if err != nil {
		return err
	}
	imageTag, err = validateManifestValue("tag", imageTag)
	if err != nil {
		return err
	}
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = string(publishscope.Tenant)
	}
	if _, err := publishscope.Normalize(scope); err != nil {
		return err
	}
	policyMode = strings.TrimSpace(policyMode)
	if policyMode == "" {
		policyMode = string(metadata.PolicyModeAllowList)
	}
	switch metadata.PolicyMode(policyMode) {
	case metadata.PolicyModeAllowList, metadata.PolicyModeObserve:
	default:
		return core.NewWithSentinel(nil, fmt.Sprintf("invalid policy mode %q; must be allow-list or observe", policyMode))
	}
	defaultDecision = strings.TrimSpace(defaultDecision)
	if defaultDecision == "" {
		defaultDecision = string(metadata.PolicyDecisionDeny)
	}
	switch metadata.PolicyDecision(defaultDecision) {
	case metadata.PolicyDecisionAllow, metadata.PolicyDecisionDeny:
	default:
		return core.NewWithSentinel(nil, fmt.Sprintf("invalid default decision %q; must be allow or deny", defaultDecision))
	}
	if port <= 0 {
		port = defaultDeployPort()
	}

	registry := metadata.RegistryFile{Version: "v1"}
	metadataPath := filepath.Join(metadataDir, "servers.yaml")
	if _, err := os.Stat(metadataPath); err == nil {
		existing, loadErr := loadMetadataForRewrite(metadataPath)
		if loadErr != nil {
			return core.WrapWithSentinel(core.ErrLoadMetadataFailed, loadErr, fmt.Sprintf("failed to load existing metadata file %q: %v", metadataPath, loadErr))
		}
		registry = *existing
	} else if err != nil && !os.IsNotExist(err) {
		return core.WrapWithSentinel(core.ErrLoadMetadataFailed, err, fmt.Sprintf("failed to inspect metadata file %q: %v", metadataPath, err))
	}
	if strings.TrimSpace(registry.Version) == "" {
		registry.Version = "v1"
	}

	var session *metadata.SessionConfig
	if sessionRequired {
		session = &metadata.SessionConfig{Required: true}
	}

	server := metadata.ServerMetadata{
		Name:             name,
		Description:      fmt.Sprintf("%s MCP server", name),
		Image:            image,
		ImageTag:         imageTag,
		Scope:            metadata.PublishScope(scope),
		PublicPathPrefix: name,
		Route:            mcpdefaults.DefaultIngressPath(name),
		Port:             port,
		Tools:            nil,
		Auth:             nil,
		Policy: &metadata.PolicyConfig{
			Mode:            metadata.PolicyMode(policyMode),
			DefaultDecision: metadata.PolicyDecision(defaultDecision),
		},
		Session: session,
		Gateway: &metadata.GatewayConfig{
			Enabled: boolPtr(true),
		},
	}
	toolMetadata, err := initToolMetadata(tools, toolSpecs, toolRisk)
	if err != nil {
		return err
	}
	server.Tools = toolMetadata

	replaced := false
	for i := range registry.Servers {
		if strings.TrimSpace(registry.Servers[i].Name) != name {
			continue
		}
		if !force {
			return core.NewWithSentinel(nil, fmt.Sprintf("metadata for server %q already exists in %s; pass --force to replace it", name, metadataPath))
		}
		registry.Servers[i] = server
		replaced = true
		break
	}
	if !replaced {
		registry.Servers = append(registry.Servers, server)
	}

	if err := os.MkdirAll(metadataDir, 0o750); err != nil {
		return core.WrapWithSentinel(nil, err, fmt.Sprintf("failed to create metadata directory %q: %v", metadataDir, err))
	}
	data, err := yaml.Marshal(&registry)
	if err != nil {
		return core.WrapWithSentinel(nil, err, fmt.Sprintf("failed to encode metadata: %v", err))
	}
	if err := os.WriteFile(metadataPath, data, 0o600); err != nil {
		return core.WrapWithSentinel(nil, err, fmt.Sprintf("failed to write metadata file %q: %v", metadataPath, err))
	}
	action := "Created"
	if replaced {
		action = "Updated"
	}
	core.Success(fmt.Sprintf("%s metadata for server %s at %s", action, name, metadataPath))
	if len(server.Tools) == 0 {
		core.Info("No tools were added; pass --tool <name> or edit .mcp/servers.yaml before governed tool calls")
	}
	return nil
}

func initToolMetadata(tools, toolSpecs []string, toolRisk string) ([]metadata.ToolConfig, error) {
	risk, err := normalizeMetadataRisk(toolRisk)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	out := make([]metadata.ToolConfig, 0, len(tools))
	for _, tool := range tools {
		tool = strings.TrimSpace(tool)
		if tool == "" {
			continue
		}
		if _, ok := seen[tool]; ok {
			continue
		}
		seen[tool] = struct{}{}
		out = append(out, metadata.ToolConfig{
			Name:          tool,
			Description:   fmt.Sprintf("%s tool", tool),
			RequiredTrust: metadata.TrustLevelLow,
			SideEffect:    metadata.ToolSideEffectRead,
			RiskLevel:     risk,
		})
	}
	for _, spec := range toolSpecs {
		spec = strings.TrimSpace(spec)
		if spec == "" {
			continue
		}
		parts := strings.Split(spec, ":")
		if len(parts) != 3 {
			return nil, core.NewWithSentinel(nil, fmt.Sprintf("unsupported tool spec %q (use name:low|medium|high:read|write|destructive)", spec))
		}
		name := strings.TrimSpace(parts[0])
		trust := metadata.TrustLevel(strings.ToLower(strings.TrimSpace(parts[1])))
		sideEffect := metadata.ToolSideEffect(strings.ToLower(strings.TrimSpace(parts[2])))
		if name == "" {
			return nil, core.NewWithSentinel(nil, fmt.Sprintf("unsupported tool spec %q: tool name is required", spec))
		}
		switch trust {
		case metadata.TrustLevelLow, metadata.TrustLevelMedium, metadata.TrustLevelHigh:
		default:
			return nil, core.NewWithSentinel(nil, fmt.Sprintf("unsupported tool spec %q: trust must be low, medium, or high", spec))
		}
		switch sideEffect {
		case metadata.ToolSideEffectRead, metadata.ToolSideEffectWrite, metadata.ToolSideEffectDestructive:
		default:
			return nil, core.NewWithSentinel(nil, fmt.Sprintf("unsupported tool spec %q: side effect must be read, write, or destructive", spec))
		}
		if _, ok := seen[name]; ok {
			return nil, core.NewWithSentinel(nil, fmt.Sprintf("duplicate tool metadata for %q", name))
		}
		seen[name] = struct{}{}
		out = append(out, metadata.ToolConfig{
			Name:          name,
			Description:   fmt.Sprintf("%s tool", name),
			RequiredTrust: trust,
			SideEffect:    sideEffect,
			RiskLevel:     risk,
		})
	}
	return out, nil
}

func normalizeMetadataRisk(raw string) (metadata.ToolRiskLevel, error) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return "", nil
	}
	switch metadata.ToolRiskLevel(raw) {
	case metadata.ToolRiskLevelLow, metadata.ToolRiskLevelMedium, metadata.ToolRiskLevelHigh:
		return metadata.ToolRiskLevel(raw), nil
	default:
		return "", core.NewWithSentinel(nil, fmt.Sprintf("invalid tool risk %q; must be low, medium, or high", raw))
	}
}

// Logger exposes the manager logger to foldered command packages.
func (m *ServerManager) Logger() *zap.Logger {
	return m.logger
}

// ListServers lists all MCP servers in the given namespace.
func (m *ServerManager) ListServers(namespace, team string) error {
	namespace = strings.TrimSpace(namespace)
	team = strings.TrimSpace(team)
	if namespace != "" && team != "" {
		return core.NewWithSentinel(nil, "use either --namespace or --team, not both")
	}
	plat, useK, err := platformapi.ResolvePlatformOrKube(m.useKube)
	if err != nil {
		return err
	}
	if useK && team != "" {
		return core.NewWithSentinel(nil, "cannot use --team with --use-kube")
	}
	if !useK {
		if team != "" {
			t, err := plat.GetTeam(context.Background(), team)
			if err != nil {
				return err
			}
			namespace = t.Namespace
		}
		if namespace != "" {
			namespace, err = validateManifestValue("namespace", namespace)
			if err != nil {
				return err
			}
		}
		items, err := plat.ListRuntimeServers(context.Background(), namespace)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "NAME\tNAMESPACE\tREADY\tSTATUS\tAGE")
		for _, s := range items {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.Name, s.Namespace, s.Ready, s.Status, s.Age)
		}
		_ = tw.Flush()
		return nil
	}
	if namespace == "" {
		namespace = core.NamespaceMCPServers
	}
	namespace, err = validateManifestValue("namespace", namespace)
	if err != nil {
		return err
	}

	// #nosec G204 -- namespace validated above; kubectl validates resource names.
	if err := m.kubectl.RunWithOutput([]string{"get", "mcpserver", "-n", namespace}, os.Stdout, os.Stderr); err != nil {
		wrappedErr := core.WrapWithSentinelAndContext(
			core.ErrListServersFailed,
			err,
			kubeerr.DirectModeFailureMessage(fmt.Sprintf("failed to list servers in namespace %q", namespace), err.Error()),
			map[string]any{"namespace": namespace, "component": "server"},
		)
		core.Error("Failed to list servers")
		core.LogStructuredError(m.logger, wrappedErr, "Failed to list servers")
		return wrappedErr
	}
	return nil
}

// GetServer retrieves details for a specific MCP server.
func (m *ServerManager) GetServer(name, namespace string) error {
	name, namespace, err := validateServerInput(name, namespace)
	if err != nil {
		return err
	}

	plat, useK, err := platformapi.ResolvePlatformOrKube(m.useKube)
	if err != nil {
		return err
	}
	if !useK {
		items, err := plat.ListRuntimeServers(context.Background(), namespace)
		if err != nil {
			return err
		}
		for _, s := range items {
			if s.Name == name && s.Namespace == namespace {
				b, _ := json.MarshalIndent(s, "", "  ")
				_, _ = os.Stdout.Write(append(b, '\n'))
				_, _ = os.Stderr.WriteString("# For the full MCPServer YAML, use mcp-runtime server get --use-kube ... with kubectl.\n")
				return nil
			}
		}
		return core.NewWithSentinel(core.ErrGetMCPServerFailed, fmt.Sprintf("server %q not found in namespace %q (platform API)", name, namespace))
	}

	// #nosec G204 -- name/namespace validated via validateServerInput.
	if err := m.kubectl.RunWithOutput([]string{"get", "mcpserver", name, "-n", namespace, "-o", "yaml"}, os.Stdout, os.Stderr); err != nil {
		wrappedErr := core.WrapWithSentinelAndContext(
			core.ErrGetMCPServerFailed,
			err,
			kubeerr.DirectModeFailureMessage(fmt.Sprintf("failed to get server %q in namespace %q", name, namespace), err.Error()),
			map[string]any{"server": name, "namespace": namespace, "component": "server"},
		)
		core.Error("Failed to get server")
		core.LogStructuredError(m.logger, wrappedErr, "Failed to get server")
		return wrappedErr
	}
	return nil
}

// ConnectConfig prints client connection config for a platform-visible MCP server.
func (m *ServerManager) ConnectConfig(name, namespace, clientName, output string) error {
	name = strings.TrimSpace(name)
	namespace = strings.TrimSpace(namespace)
	if name == "" {
		return core.NewWithSentinel(nil, "server name is required")
	}
	if m.useKube {
		return core.NewWithSentinel(nil, "connect-config uses the platform API; omit --use-kube and run mcp-runtime auth login first")
	}
	plat, err := platformapi.NewPlatformClient()
	if err != nil {
		return err
	}
	items, err := plat.ListRuntimeServers(context.Background(), namespace)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.Name != name {
			continue
		}
		if namespace != "" && item.Namespace != namespace {
			continue
		}
		config, err := BuildConnectConfig(item, clientName)
		if err != nil {
			return err
		}
		return printConnectConfig(config, output)
	}
	if namespace != "" {
		return core.NewWithSentinel(nil, fmt.Sprintf("server %q not found in namespace %q", name, namespace))
	}
	return core.NewWithSentinel(nil, fmt.Sprintf("server %q not found", name))
}

func BuildConnectConfig(server platformapi.ServerListItem, clientName string) (map[string]any, error) {
	clientName = strings.ToLower(strings.TrimSpace(clientName))
	if clientName == "" {
		clientName = "json"
	}
	serverName := strings.TrimSpace(server.Name)
	if serverName == "" {
		serverName = "mcp-server"
	}
	url := strings.TrimSpace(server.Endpoint)
	if access := server.AccessJSON; len(access) > 0 {
		if servers, ok := access["mcpServers"].(map[string]any); ok && len(servers) > 0 {
			keys := make([]string, 0, len(servers))
			for key := range servers {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				serverName = key
				entry, ok := servers[key].(map[string]any)
				if !ok {
					continue
				}
				value, ok := entry["url"].(string)
				if !ok || strings.TrimSpace(value) == "" {
					continue
				}
				url = strings.TrimSpace(value)
				break
			}
		}
	}
	if url == "" {
		return nil, core.NewWithSentinel(nil, fmt.Sprintf("server %q has no connect URL", serverName))
	}
	switch clientName {
	case "claude", "cursor", "json", "raw":
		return map[string]any{
			"mcpServers": map[string]any{
				serverName: map[string]any{
					"type": "http",
					"url":  url,
				},
			},
		}, nil
	case "vscode", "vs-code":
		return map[string]any{
			"servers": map[string]any{
				serverName: map[string]any{
					"type": "http",
					"url":  url,
				},
			},
		}, nil
	default:
		return nil, core.NewWithSentinel(nil, "client must be claude, cursor, vscode, or json")
	}
}

func printConnectConfig(config map[string]any, output string) error {
	switch strings.ToLower(strings.TrimSpace(output)) {
	case "", "text", "json":
		data, err := json.MarshalIndent(config, "", "  ")
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(append(data, '\n'))
		return err
	case "yaml":
		data, err := yaml.Marshal(config)
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(data)
		return err
	default:
		return core.NewWithSentinel(nil, "output must be text, json, or yaml")
	}
}

// CreateServer creates a new MCP server with the given parameters.
func (m *ServerManager) CreateServer(name, namespace, image, imageTag string) error {
	if err := m.requireKubectlForMutation(); err != nil {
		return err
	}
	if image == "" {
		return core.ErrImageRequired
	}

	name, namespace, err := validateServerInput(name, namespace)
	if err != nil {
		return err
	}
	if image, err = validateManifestValue("image", image); err != nil {
		return err
	}
	if imageTag, err = validateManifestValue("tag", imageTag); err != nil {
		return err
	}

	m.logger.Info("Creating MCP server", zap.String("name", name), zap.String("image", image))

	manifest := mcpServerManifest{
		APIVersion: "mcpruntime.org/v1alpha1",
		Kind:       "MCPServer",
		Metadata: manifestMetadata{
			Name:      name,
			Namespace: namespace,
		},
		Spec: manifestSpec{
			Image:       image,
			ImageTag:    imageTag,
			Replicas:    1,
			Port:        core.GetDefaultServerPort(),
			ServicePort: 80,
			IngressPath: mcpdefaults.DefaultIngressPath(name),
		},
	}

	manifestBytes, err := yaml.Marshal(manifest)
	if err != nil {
		wrappedErr := core.WrapWithSentinelAndContext(
			core.ErrMarshalManifestFailed,
			err,
			fmt.Sprintf("failed to marshal manifest: %v", err),
			map[string]any{"server": name, "namespace": namespace, "component": "server"},
		)
		core.Error("Failed to marshal manifest")
		core.LogStructuredError(m.logger, wrappedErr, "Failed to marshal manifest")
		return wrappedErr
	}

	if err := kube.ApplyManifestContent(m.kubectl.CommandArgs, string(manifestBytes)); err != nil {
		wrappedErr := core.WrapWithSentinelAndContext(
			core.ErrCreateServerFailed,
			err,
			kubeerr.DirectModeFailureMessage(fmt.Sprintf("failed to create server %q", name), err.Error()),
			map[string]any{"server": name, "namespace": namespace, "image": image, "component": "server"},
		)
		core.Error("Failed to create server")
		core.LogStructuredError(m.logger, wrappedErr, "Failed to create server")
		return wrappedErr
	}
	return nil
}

func (m *ServerManager) DeployServer(name, namespace, team, scope, image, imageTag string, replicas, port, servicePort int32, metadataFile, metadataDir string, update bool) error {
	if m.useKube {
		return core.NewWithSentinel(nil, "server deploy uses the platform API; remove --use-kube")
	}
	name, err := validateManifestValue("name", name)
	if err != nil {
		return err
	}
	if strings.TrimSpace(image) != "" {
		image, err = validateManifestValue("image", image)
		if err != nil {
			return err
		}
	}
	imageTag, err = validateManifestValue("tag", imageTag)
	if err != nil {
		return err
	}
	namespace = strings.TrimSpace(namespace)
	team = strings.TrimSpace(team)
	scope = strings.TrimSpace(scope)
	selectedMetadata, err := selectDeployMetadata(name, metadataFile, metadataDir)
	if err != nil {
		return err
	}
	if selectedMetadata != nil {
		if scope == "" {
			scope = string(selectedMetadata.Scope)
		}
		if namespace == "" && team == "" && (scope == "" || scope == string(publishscope.Tenant)) {
			if !(scope == string(publishscope.Tenant) && strings.TrimSpace(selectedMetadata.Namespace) == core.NamespaceMCPServers) {
				namespace = selectedMetadata.Namespace
			}
		}
		if image == "" {
			image = selectedMetadata.Image
		}
		if strings.TrimSpace(imageTag) == "" || imageTag == "latest" && strings.TrimSpace(selectedMetadata.ImageTag) != "" {
			imageTag = selectedMetadata.ImageTag
		}
	}
	if _, err := publishscope.Normalize(scope); err != nil {
		return err
	}
	if namespace != "" && team != "" {
		return core.NewWithSentinel(nil, "use either --namespace or --team, not both")
	}
	if scope != "" && namespace != "" && scope != string(publishscope.Tenant) {
		return core.NewWithSentinel(nil, "--scope public/org resolves the platform catalog namespace; use --scope tenant when passing --namespace")
	}
	if scope != "" && team != "" && scope != string(publishscope.Tenant) {
		return core.NewWithSentinel(nil, "--scope public/org cannot be combined with --team")
	}
	plat, err := platformapi.NewPlatformClient()
	if err != nil {
		return platformapi.AuthRequiredError(err)
	}
	if team != "" {
		t, err := plat.GetTeam(context.Background(), team)
		if err != nil {
			return err
		}
		namespace = t.Namespace
	}
	if namespace == "" && scope == string(publishscope.Tenant) {
		principal, err := plat.CurrentPrincipal(context.Background())
		if err != nil {
			return err
		}
		switch len(principal.Teams) {
		case 0:
			return core.NewWithSentinel(nil, "tenant deploy requires a team membership; ask an admin to add you to a team")
		case 1:
			namespace = principal.Teams[0].Namespace
		default:
			return core.NewWithSentinel(nil, "tenant deploy has multiple team memberships; pass --team <slug> or --namespace <team-namespace>")
		}
	}
	if namespace != "" {
		namespace, err = validateManifestValue("namespace", namespace)
		if err != nil {
			return err
		}
	}
	spec := buildDeployServerSpec(name, image, imageTag, replicas, port, servicePort)
	if err := applyDeployMetadataDefaults(&spec, name, metadataFile, metadataDir); err != nil {
		return err
	}
	if strings.TrimSpace(spec.Image) == "" {
		return core.NewWithSentinel(core.ErrImageRequired, "image is required unless .mcp metadata provides image; pass --image or run from a directory with .mcp/servers.yaml")
	}
	// Metadata written by an older CLI (or with no registry configured) can
	// name the in-cluster registry DNS name or the registry.local placeholder.
	// Neither is pullable by a remote platform, so deploy the ref from the
	// registry of the active platform login that `server push` published to.
	if rewritten, ok := registrycli.PublicImageRefForSavedLogin(spec.Image); ok {
		core.Warn(fmt.Sprintf("Image %s names a registry that is only reachable inside a cluster; deploying %s from your platform login's registry instead", spec.Image, rewritten))
		spec.Image = rewritten
	}
	expectedImage := strings.TrimSpace(spec.Image)
	applied, err := plat.ApplyRuntimeServerWithScopeUpdate(context.Background(), name, namespace, scope, spec, update)
	if err != nil {
		return err
	}
	ready, err := waitForDeployedServer(context.Background(), plat, applied.Name, applied.Namespace, expectedImage, imageTag)
	if err != nil {
		return err
	}
	core.Success(fmt.Sprintf("Deployed server %s in namespace %s (status %s)", ready.Name, ready.Namespace, ready.Status))
	return nil
}

// GenerateManifests renders MCPServer YAML from .mcp metadata for review,
// GitOps, or admin workflows. Normal user deploys should call DeployServer.
func (m *ServerManager) GenerateManifests(metadataFile, metadataDir, outputDir string) error {
	registry, err := loadDeployMetadata(metadataFile, metadataDir)
	if err != nil {
		return err
	}
	if registry == nil || len(registry.Servers) == 0 {
		err := core.ErrNoServersInMetadata
		core.Error("No servers found in metadata")
		core.LogStructuredError(m.logger, err, "No servers found in metadata")
		return err
	}
	outputDir = strings.TrimSpace(outputDir)
	if outputDir == "" {
		outputDir = "manifests"
	}
	if err := metadata.GenerateCRDsFromRegistry(registry, outputDir); err != nil {
		wrappedErr := core.WrapWithSentinelAndContext(
			core.ErrGenerateCRDsFailed,
			err,
			fmt.Sprintf("failed to generate MCPServer manifests: %v", err),
			map[string]any{"output_dir": outputDir, "server_count": len(registry.Servers), "component": "server"},
		)
		core.Error("Failed to generate MCPServer manifests")
		core.LogStructuredError(m.logger, wrappedErr, "Failed to generate MCPServer manifests")
		return wrappedErr
	}
	files, _ := filepath.Glob(filepath.Join(outputDir, "*.yaml"))
	for _, file := range files {
		core.Success(fmt.Sprintf("Generated: %s", file))
	}
	return nil
}

var serverDeployPollInterval = 2 * time.Second

func waitForDeployedServer(ctx context.Context, plat *platformapi.PlatformClient, name, namespace, expectedImage, expectedTag string) (platformapi.ServerListItem, error) {
	timeout := core.GetDeploymentTimeout()
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	var last *platformapi.ServerListItem
	for {
		servers, err := plat.ListRuntimeServers(ctx, namespace)
		if err != nil {
			return platformapi.ServerListItem{}, core.WrapWithSentinel(
				core.ErrListServersFailed,
				err,
				fmt.Sprintf("wait for deployed server readiness: %v", err),
			)
		}
		for i := range servers {
			if servers[i].Name != name {
				continue
			}
			last = &servers[i]
			if strings.EqualFold(strings.TrimSpace(servers[i].Ready), "true") || strings.EqualFold(strings.TrimSpace(servers[i].Status), "ready") {
				if err := validateDeployedServerImage(servers[i], expectedImage, expectedTag); err != nil {
					return platformapi.ServerListItem{}, err
				}
				return servers[i], nil
			}
			break
		}
		if time.Now().After(deadline) {
			if last != nil {
				return platformapi.ServerListItem{}, core.NewWithSentinel(
					nil,
					fmt.Sprintf(
						"server %s was applied in namespace %s but did not become ready within %s (status=%s ready=%s)",
						name,
						namespace,
						timeout.Round(time.Second),
						strings.TrimSpace(last.Status),
						strings.TrimSpace(last.Ready),
					),
				)
			}
			return platformapi.ServerListItem{}, core.NewWithSentinel(
				nil,
				fmt.Sprintf("server %s was applied in namespace %s but did not appear in runtime inventory within %s", name, namespace, timeout.Round(time.Second)),
			)
		}
		select {
		case <-ctx.Done():
			return platformapi.ServerListItem{}, core.WrapWithSentinel(
				core.ErrListServersFailed,
				ctx.Err(),
				fmt.Sprintf("wait for deployed server readiness: %v", ctx.Err()),
			)
		case <-time.After(serverDeployPollInterval):
		}
	}
}

func validateDeployedServerImage(server platformapi.ServerListItem, expectedImage, expectedTag string) error {
	expectedImage = strings.TrimSpace(expectedImage)
	if expectedImage == "" {
		return nil
	}
	gotImage := strings.TrimSpace(server.Image)
	if gotImage == "" {
		return nil
	}
	if !deployImageRefsEquivalent(expectedImage, gotImage) {
		return core.NewWithSentinel(
			nil,
			fmt.Sprintf(
				"server %s deployed with image %q but runtime inventory reports %q; verify server push and deploy image references match",
				server.Name,
				expectedImage,
				gotImage,
			),
		)
	}
	expectedTag = strings.TrimSpace(expectedTag)
	gotTag := strings.TrimSpace(server.ImageTag)
	if expectedTag != "" && gotTag != "" && gotTag != expectedTag {
		return core.NewWithSentinel(
			nil,
			fmt.Sprintf(
				"server %s deployed with tag %q but runtime inventory reports %q",
				server.Name,
				expectedTag,
				gotTag,
			),
		)
	}
	return nil
}

func deployImageRefsEquivalent(expectedImage, gotImage string) bool {
	expected := normalizeDeployImageForCompare(expectedImage)
	got := normalizeDeployImageForCompare(gotImage)
	if expected == "" || got == "" {
		return true
	}
	if expected == got {
		return true
	}
	return strings.HasSuffix(got, "/"+expected) || strings.HasSuffix(expected, "/"+got)
}

func normalizeDeployImageForCompare(image string) string {
	image = strings.TrimSpace(image)
	if image == "" {
		return ""
	}
	if idx := strings.LastIndex(image, "@"); idx > 0 {
		image = image[:idx]
	}
	if idx := strings.LastIndex(image, ":"); idx > 0 {
		suffix := image[idx+1:]
		if !strings.Contains(suffix, "/") {
			image = image[:idx]
		}
	}
	first, rest, found := strings.Cut(image, "/")
	if found && (strings.Contains(first, ".") || strings.Contains(first, ":") || first == "localhost") {
		image = rest
	}
	return strings.Trim(image, "/")
}

func buildDeployServerSpec(name, image, imageTag string, replicas, port, servicePort int32) mcpv1alpha1.MCPServerSpec {
	return mcpv1alpha1.MCPServerSpec{
		Image:            image,
		ImageTag:         imageTag,
		Replicas:         &replicas,
		Port:             port,
		ServicePort:      servicePort,
		PublicPathPrefix: name,
		IngressPath:      "/" + name + "/mcp",
		Gateway:          &mcpv1alpha1.GatewayConfig{Enabled: mcpv1alpha1.BoolPtr(true)},
	}
}

func applyDeployMetadataDefaults(spec *mcpv1alpha1.MCPServerSpec, name, metadataFile, metadataDir string) error {
	if spec == nil {
		return nil
	}
	registry, err := loadDeployMetadata(metadataFile, metadataDir)
	if err != nil {
		return err
	}
	if registry == nil || len(registry.Servers) == 0 {
		return nil
	}

	var selected *metadata.ServerMetadata
	for i := range registry.Servers {
		if strings.TrimSpace(registry.Servers[i].Name) == name {
			selected = &registry.Servers[i]
			break
		}
	}
	if selected == nil && len(registry.Servers) == 1 {
		selected = &registry.Servers[0]
	}
	if selected == nil {
		return nil
	}
	mergeDeployMetadata(spec, selected)
	return nil
}

func loadDeployMetadata(metadataFile, metadataDir string) (*metadata.RegistryFile, error) {
	metadataFile = strings.TrimSpace(metadataFile)
	metadataDir = strings.TrimSpace(metadataDir)
	if metadataFile != "" {
		registry, err := metadata.LoadFromFile(metadataFile)
		if err != nil {
			return nil, core.WrapWithSentinel(core.ErrLoadMetadataFailed, err, fmt.Sprintf("failed to load metadata file %q: %v", metadataFile, err))
		}
		return registry, nil
	}
	if metadataDir == "" {
		metadataDir = ".mcp"
	}
	if _, err := os.Stat(metadataDir); err != nil {
		if os.IsNotExist(err) && metadataDir == ".mcp" {
			return nil, nil
		}
		return nil, core.WrapWithSentinel(core.ErrLoadMetadataFailed, err, fmt.Sprintf("failed to inspect metadata directory %q: %v", metadataDir, err))
	}
	registry, err := metadata.LoadFromDirectory(metadataDir)
	if err != nil {
		return nil, core.WrapWithSentinel(core.ErrLoadMetadataFailed, err, fmt.Sprintf("failed to load metadata directory %q: %v", metadataDir, err))
	}
	return registry, nil
}

func selectDeployMetadata(name, metadataFile, metadataDir string) (*metadata.ServerMetadata, error) {
	registry, err := loadDeployMetadata(metadataFile, metadataDir)
	if err != nil {
		return nil, err
	}
	if registry == nil || len(registry.Servers) == 0 {
		return nil, nil
	}
	for i := range registry.Servers {
		if strings.TrimSpace(registry.Servers[i].Name) == name {
			return &registry.Servers[i], nil
		}
	}
	if len(registry.Servers) == 1 {
		return &registry.Servers[0], nil
	}
	return nil, core.NewWithSentinel(nil, fmt.Sprintf("no metadata entry for server %q", name))
}

func mergeDeployMetadata(spec *mcpv1alpha1.MCPServerSpec, src *metadata.ServerMetadata) {
	if strings.TrimSpace(spec.Image) == "" {
		spec.Image = src.Image
	}
	if strings.TrimSpace(spec.ImageTag) == "" || strings.TrimSpace(spec.ImageTag) == "latest" && strings.TrimSpace(src.ImageTag) != "" {
		spec.ImageTag = src.ImageTag
	}
	if strings.TrimSpace(spec.Description) == "" {
		spec.Description = src.Description
	}
	if src.Port > 0 {
		spec.Port = src.Port
	}
	if src.Replicas != nil {
		spec.Replicas = src.Replicas
	}
	if strings.TrimSpace(src.IngressHost) != "" {
		spec.IngressHost = strings.TrimSpace(src.IngressHost)
	}
	if len(spec.Tools) == 0 {
		spec.Tools = make([]mcpv1alpha1.ToolConfig, 0, len(src.Tools))
		for _, tool := range src.Tools {
			spec.Tools = append(spec.Tools, mcpv1alpha1.ToolConfig{
				Name:          tool.Name,
				Description:   tool.Description,
				RequiredTrust: mcpv1alpha1.TrustLevel(tool.RequiredTrust),
				SideEffect:    mcpv1alpha1.ToolSideEffect(tool.SideEffect),
				RiskLevel:     mcpv1alpha1.ToolRiskLevel(tool.RiskLevel),
				Labels:        copyStringMap(tool.Labels),
			})
		}
	}
	if len(spec.Prompts) == 0 {
		spec.Prompts = convertDeployInventory(src.Prompts)
	}
	if len(spec.MCPResources) == 0 {
		spec.MCPResources = convertDeployInventory(src.MCPResources)
	}
	if len(spec.Tasks) == 0 {
		spec.Tasks = convertDeployInventory(src.Tasks)
	}
	if spec.Resources.Limits == nil && spec.Resources.Requests == nil && src.Resources != nil {
		spec.Resources = convertDeployResources(src.Resources)
	}
	if len(src.EnvVars) > 0 {
		spec.EnvVars = mergeDeployEnvVars(spec.EnvVars, src.EnvVars)
	}
	if len(spec.SecretEnvVars) == 0 {
		spec.SecretEnvVars = convertDeploySecretEnvVars(src.SecretEnvVars)
	}
	if src.Auth != nil {
		spec.Auth = &mcpv1alpha1.AuthConfig{
			TokenHeader: src.Auth.TokenHeader,
			IssuerURL:   src.Auth.IssuerURL,
			Audience:    src.Auth.Audience,
		}
	}
	if src.Policy != nil {
		spec.Policy = &mcpv1alpha1.PolicyConfig{
			Mode:            mcpv1alpha1.PolicyMode(src.Policy.Mode),
			DefaultDecision: mcpv1alpha1.PolicyDecision(src.Policy.DefaultDecision),
			EnforceOn:       src.Policy.EnforceOn,
			PolicyVersion:   src.Policy.PolicyVersion,
		}
	}
	if src.Session != nil {
		spec.Session = &mcpv1alpha1.SessionConfig{
			Required:            src.Session.Required,
			Store:               src.Session.Store,
			MaxLifetime:         src.Session.MaxLifetime,
			IdleTimeout:         src.Session.IdleTimeout,
			UpstreamTokenHeader: src.Session.UpstreamTokenHeader,
		}
	}
	if src.Gateway != nil {
		spec.Gateway = &mcpv1alpha1.GatewayConfig{
			Enabled:     src.Gateway.Enabled,
			Image:       src.Gateway.Image,
			Port:        src.Gateway.Port,
			UpstreamURL: src.Gateway.UpstreamURL,
			StripPrefix: src.Gateway.StripPrefix,
		}
		if spec.Gateway.Enabled == nil {
			spec.Gateway.Enabled = mcpv1alpha1.BoolPtr(true)
		}
		if src.Gateway.Resources != nil {
			resources := convertDeployResources(src.Gateway.Resources)
			spec.Gateway.Resources = &resources
		}
	}
	if spec.Analytics == nil && src.Analytics != nil {
		spec.Analytics = &mcpv1alpha1.AnalyticsConfig{
			Disabled:  src.Analytics.Disabled,
			IngestURL: src.Analytics.IngestURL,
			Source:    src.Analytics.Source,
			EventType: src.Analytics.EventType,
		}
		if src.Analytics.APIKeySecretRef != nil {
			spec.Analytics.APIKeySecretRef = &mcpv1alpha1.SecretKeyRef{
				Name: src.Analytics.APIKeySecretRef.Name,
				Key:  src.Analytics.APIKeySecretRef.Key,
			}
		}
	}
	if spec.Rollout == nil && src.Rollout != nil {
		spec.Rollout = &mcpv1alpha1.RolloutConfig{
			Strategy:       mcpv1alpha1.RolloutStrategy(src.Rollout.Strategy),
			MaxUnavailable: src.Rollout.MaxUnavailable,
			MaxSurge:       src.Rollout.MaxSurge,
			CanaryReplicas: src.Rollout.CanaryReplicas,
		}
	}
}

func convertDeployInventory(items []metadata.InventoryItem) []mcpv1alpha1.InventoryItem {
	if len(items) == 0 {
		return nil
	}
	out := make([]mcpv1alpha1.InventoryItem, 0, len(items))
	for _, item := range items {
		out = append(out, mcpv1alpha1.InventoryItem{
			Name:        item.Name,
			Description: item.Description,
			Labels:      copyStringMap(item.Labels),
		})
	}
	return out
}

func mergeDeployEnvVars(existing []mcpv1alpha1.EnvVar, items []metadata.EnvVar) []mcpv1alpha1.EnvVar {
	out := append([]mcpv1alpha1.EnvVar(nil), existing...)
	index := make(map[string]int, len(out))
	for i, item := range out {
		index[item.Name] = i
	}
	for _, item := range items {
		if i, ok := index[item.Name]; ok {
			out[i].Value = item.Value
			continue
		}
		index[item.Name] = len(out)
		out = append(out, mcpv1alpha1.EnvVar{Name: item.Name, Value: item.Value})
	}
	return out
}

func convertDeployResources(resources *metadata.ResourceRequirements) mcpv1alpha1.ResourceRequirements {
	if resources == nil {
		return mcpv1alpha1.ResourceRequirements{}
	}
	converted := mcpv1alpha1.ResourceRequirements{}
	if resources.Limits != nil {
		converted.Limits = &mcpv1alpha1.ResourceList{
			CPU:    resources.Limits.CPU,
			Memory: resources.Limits.Memory,
		}
	}
	if resources.Requests != nil {
		converted.Requests = &mcpv1alpha1.ResourceList{
			CPU:    resources.Requests.CPU,
			Memory: resources.Requests.Memory,
		}
	}
	return converted
}

func convertDeploySecretEnvVars(items []metadata.SecretEnvVar) []mcpv1alpha1.SecretEnvVar {
	if len(items) == 0 {
		return nil
	}
	out := make([]mcpv1alpha1.SecretEnvVar, 0, len(items))
	for _, item := range items {
		converted := mcpv1alpha1.SecretEnvVar{Name: item.Name}
		if item.SecretKeyRef != nil {
			converted.SecretKeyRef = &mcpv1alpha1.SecretKeyRef{
				Name: item.SecretKeyRef.Name,
				Key:  item.SecretKeyRef.Key,
			}
		}
		out = append(out, converted)
	}
	return out
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// ApplyServerFromFile applies an MCPServer manifest from disk.
func (m *ServerManager) ApplyServerFromFile(file string) error {
	if err := m.requireKubectlForMutation(); err != nil {
		return err
	}
	if err := kube.ApplyManifestFromFile(m.kubectl.CommandArgs, file, os.Stdout, os.Stderr); err != nil {
		wrappedErr := core.WrapWithSentinelAndContext(
			nil,
			err,
			kubeerr.DirectModeFailureMessage(fmt.Sprintf("failed to apply server manifest from file %q", file), err.Error()),
			map[string]any{"file": file, "component": "server"},
		)
		core.Error("Failed to apply server manifest")
		core.LogStructuredError(m.logger, wrappedErr, "Failed to apply server manifest")
		return wrappedErr
	}
	return nil
}

// CreateServerFromFile creates an MCP server from a YAML file.
func (m *ServerManager) CreateServerFromFile(file string) error {
	if err := m.requireKubectlForMutation(); err != nil {
		return err
	}
	absPath, err := kube.ResolveRegularFilePath(file)
	if err != nil {
		core.Error("Cannot access file")
		core.LogStructuredError(m.logger, err, "Cannot access file")
		return err
	}

	manifestBytes, err := kube.ReadFileAtPath(absPath)
	if err != nil {
		wrappedErr := core.WrapWithSentinel(core.ErrFileNotAccessible, err, fmt.Sprintf("cannot read file %q: %v", file, err))
		core.Error("Cannot access file")
		core.LogStructuredError(m.logger, wrappedErr, "Cannot access file")
		return wrappedErr
	}

	if err := kube.ApplyManifestContent(m.kubectl.CommandArgs, string(manifestBytes)); err != nil {
		wrappedErr := core.WrapWithSentinelAndContext(
			core.ErrCreateServerFailed,
			err,
			kubeerr.DirectModeFailureMessage(fmt.Sprintf("failed to create server from file %q", file), err.Error()),
			map[string]any{"file": file, "component": "server"},
		)
		core.Error("Failed to create server from file")
		core.LogStructuredError(m.logger, wrappedErr, "Failed to create server from file")
		return wrappedErr
	}
	return nil
}

// ExportServer exports an MCPServer manifest to stdout or a file.
func (m *ServerManager) ExportServer(name, namespace, file string) error {
	if err := m.requireKubectlForMutation(); err != nil {
		return err
	}
	name, namespace, err := validateServerInput(name, namespace)
	if err != nil {
		return err
	}

	cmd, err := m.kubectl.CommandArgs([]string{"get", "mcpserver", name, "-n", namespace, "-o", "yaml"})
	if err != nil {
		return err
	}
	output, execErr := cmd.CombinedOutput()
	if execErr != nil {
		detail := kubeerr.CommandDetail(string(output), execErr)
		wrappedErr := core.WrapWithSentinelAndContext(
			nil,
			execErr,
			kubeerr.DirectModeFailureMessage(fmt.Sprintf("failed to export server %q in namespace %q", name, namespace), detail),
			map[string]any{"server": name, "namespace": namespace, "component": "server"},
		)
		core.Error("Failed to export server")
		core.LogStructuredError(m.logger, wrappedErr, "Failed to export server")
		return wrappedErr
	}

	if file != "" {
		if err := kube.WriteOutputFile(file, output); err != nil {
			wrappedErr := core.WrapWithSentinelAndContext(
				nil,
				err,
				fmt.Sprintf("failed to write server manifest to %q: %v", file, err),
				map[string]any{"server": name, "namespace": namespace, "file": file, "component": "server"},
			)
			core.Error("Failed to write server manifest")
			core.LogStructuredError(m.logger, wrappedErr, "Failed to write server manifest")
			return wrappedErr
		}
		return nil
	}

	if _, err := os.Stdout.Write(output); err != nil {
		wrappedErr := core.WrapWithSentinelAndContext(
			nil,
			err,
			fmt.Sprintf("failed to write server manifest to stdout: %v", err),
			map[string]any{"server": name, "namespace": namespace, "component": "server"},
		)
		core.Error("Failed to write server manifest")
		core.LogStructuredError(m.logger, wrappedErr, "Failed to write server manifest")
		return wrappedErr
	}
	return nil
}

// PatchServer patches an existing MCPServer resource using merge/json/strategic patch types.
func (m *ServerManager) PatchServer(name, namespace, patchType, patch, patchFile string) error {
	if err := m.requireKubectlForMutation(); err != nil {
		return err
	}
	name, namespace, err := validateServerInput(name, namespace)
	if err != nil {
		return err
	}

	patchType = strings.TrimSpace(strings.ToLower(patchType))
	switch patchType {
	case "merge", "json", "strategic":
	default:
		return core.NewWithSentinel(nil, fmt.Sprintf("unsupported patch type %q (use merge|json|strategic)", patchType))
	}

	inlinePatch := strings.TrimSpace(patch)
	patchFile = strings.TrimSpace(patchFile)
	switch {
	case inlinePatch == "" && patchFile == "":
		return core.NewWithSentinel(nil, "either --patch or --patch-file is required")
	case inlinePatch != "" && patchFile != "":
		return core.NewWithSentinel(nil, "use either --patch or --patch-file, not both")
	}

	normalizedPatch := inlinePatch
	if patchFile != "" {
		normalizedPatch, err = kube.NormalizePatchFile(patchFile)
	} else {
		normalizedPatch, err = kube.NormalizePatchDocument(inlinePatch)
	}
	if err != nil {
		wrappedErr := core.WrapWithSentinelAndContext(
			nil,
			err,
			fmt.Sprintf("failed to prepare patch for server %q: %v", name, err),
			map[string]any{"server": name, "namespace": namespace, "patch_type": patchType, "component": "server"},
		)
		core.Error("Failed to prepare server patch")
		core.LogStructuredError(m.logger, wrappedErr, "Failed to prepare server patch")
		return wrappedErr
	}

	args := []string{"patch", "mcpserver", name, "-n", namespace, "--type", patchType, "--patch", normalizedPatch}
	if err := m.kubectl.RunWithOutput(args, os.Stdout, os.Stderr); err != nil {
		wrappedErr := core.WrapWithSentinelAndContext(
			nil,
			err,
			kubeerr.DirectModeFailureMessage(fmt.Sprintf("failed to patch server %q in namespace %q", name, namespace), err.Error()),
			map[string]any{"server": name, "namespace": namespace, "patch_type": patchType, "component": "server"},
		)
		core.Error("Failed to patch server")
		core.LogStructuredError(m.logger, wrappedErr, "Failed to patch server")
		return wrappedErr
	}

	return nil
}

// InspectServerPolicy prints the rendered gateway policy ConfigMap content for a server.
func (m *ServerManager) InspectServerPolicy(name, namespace string) error {
	name, namespace, err := validateServerInput(name, namespace)
	if err != nil {
		return err
	}

	plat, useK, err := platformapi.ResolvePlatformOrKube(m.useKube)
	if err != nil {
		return err
	}
	if !useK {
		b, err := plat.GetRuntimePolicy(context.Background(), namespace, name)
		if err != nil {
			wrappedErr := core.WrapWithSentinelAndContext(
				nil,
				err,
				fmt.Sprintf("platform API policy for server %q: %v", name, err),
				map[string]any{"server": name, "namespace": namespace, "component": "server"},
			)
			core.Error("Failed to read server policy")
			core.LogStructuredError(m.logger, wrappedErr, "Failed to read server policy")
			return wrappedErr
		}
		var pretty map[string]interface{}
		if err := json.Unmarshal(b, &pretty); err != nil {
			_, _ = os.Stdout.Write(b)
			_, _ = os.Stdout.WriteString("\n")
		} else {
			enc, _ := json.MarshalIndent(pretty, "", "  ")
			_, _ = os.Stdout.Write(append(enc, '\n'))
		}
		return nil
	}

	configMapName := mcpdefaults.GatewayPolicyConfigMapName(name)
	args := []string{"get", "configmap", configMapName, "-n", namespace, "-o", `go-template={{index .data "policy.json"}}`}
	cmd, err := m.kubectl.CommandArgs(args)
	if err != nil {
		return err
	}
	output, execErr := cmd.CombinedOutput()
	if execErr != nil {
		detail := kubeerr.CommandDetail(string(output), execErr)
		wrappedErr := core.WrapWithSentinelAndContext(
			nil,
			execErr,
			kubeerr.DirectModeFailureMessage(fmt.Sprintf("failed to inspect rendered policy for server %q in namespace %q", name, namespace), detail),
			map[string]any{"server": name, "namespace": namespace, "component": "server"},
		)
		core.Error("Failed to inspect server policy")
		core.LogStructuredError(m.logger, wrappedErr, "Failed to inspect server policy")
		return wrappedErr
	}

	if len(output) > 0 {
		if _, err := os.Stdout.Write(output); err != nil {
			wrappedErr := core.WrapWithSentinelAndContext(
				nil,
				err,
				fmt.Sprintf("failed to write rendered policy to stdout: %v", err),
				map[string]any{"server": name, "namespace": namespace, "component": "server"},
			)
			core.Error("Failed to inspect server policy")
			core.LogStructuredError(m.logger, wrappedErr, "Failed to inspect server policy")
			return wrappedErr
		}
	}
	if len(output) == 0 || output[len(output)-1] != '\n' {
		fmt.Fprintln(os.Stdout)
	}

	return nil
}

// DeleteServer deletes an MCP server.
func (m *ServerManager) DeleteServer(name, namespace string) error {
	name, namespace, err := validateServerInput(name, namespace)
	if err != nil {
		return err
	}

	plat, useK, err := platformapi.ResolvePlatformOrKube(m.useKube)
	if err != nil {
		return err
	}
	if !useK {
		if err := plat.DeleteRuntimeServer(context.Background(), namespace, name); err != nil {
			return err
		}
		core.Success(fmt.Sprintf("Retired server %s in namespace %s", name, namespace))
		return nil
	}

	m.logger.Info("Deleting MCP server", zap.String("name", name))

	// #nosec G204 -- name/namespace validated via validateServerInput.
	if err := m.kubectl.RunWithOutput([]string{"delete", "mcpserver", name, "-n", namespace}, os.Stdout, os.Stderr); err != nil {
		wrappedErr := core.WrapWithSentinelAndContext(
			core.ErrDeleteServerFailed,
			err,
			kubeerr.DirectModeFailureMessage(fmt.Sprintf("failed to delete server %q in namespace %q", name, namespace), err.Error()),
			map[string]any{"server": name, "namespace": namespace, "component": "server"},
		)
		core.Error("Failed to delete server")
		core.LogStructuredError(m.logger, wrappedErr, "Failed to delete server")
		return wrappedErr
	}
	return nil
}

// ViewServerLogs views logs from an MCP server.
func (m *ServerManager) ViewServerLogs(name, namespace string, follow, previous bool, tail int, since string) error {
	if err := m.requireKubectlForMutation(); err != nil {
		return err
	}
	name, namespace, err := validateServerInput(name, namespace)
	if err != nil {
		return err
	}

	args := []string{
		"logs",
		"-l", core.LabelApp + "=" + name,
		"-n", namespace,
		"--all-containers=true",
		"--tail", strconv.Itoa(tail),
	}
	if follow {
		args = append(args, "-f")
	}
	if previous {
		args = append(args, "--previous")
	}
	if s := strings.TrimSpace(since); s != "" {
		args = append(args, "--since", s)
	}

	// #nosec G204 -- name/namespace validated via validateServerInput.
	if err := m.kubectl.RunWithOutput(args, os.Stdout, os.Stderr); err != nil {
		wrappedErr := core.WrapWithSentinelAndContext(
			core.ErrViewServerLogsFailed,
			err,
			kubeerr.DirectModeFailureMessage(fmt.Sprintf("failed to view logs for server %q in namespace %q", name, namespace), err.Error()),
			map[string]any{"server": name, "namespace": namespace, "component": "server"},
		)
		core.Error("Failed to view server logs")
		core.LogStructuredError(m.logger, wrappedErr, "Failed to view server logs")
		return wrappedErr
	}
	return nil
}

// ServerStatus shows the status of MCP servers in a namespace.
func (m *ServerManager) ServerStatus(namespace string) error {
	core.Header(fmt.Sprintf("MCP Servers in %s", namespace))
	core.DefaultPrinter.Println()

	plat, useK, err := platformapi.ResolvePlatformOrKube(m.useKube)
	if err != nil {
		return err
	}
	if !useK {
		items, err := plat.ListRuntimeServers(context.Background(), namespace)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			core.Warn("No MCP servers found in namespace " + namespace)
		} else {
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "NAME\tNAMESPACE\tREADY\tSTATUS\tAGE")
			for _, s := range items {
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.Name, s.Namespace, s.Ready, s.Status, s.Age)
			}
			_ = tw.Flush()
		}
		core.Info("Pod details need kubectl. Run with --use-kube for full status including pods.")
		return nil
	}

	// Get MCPServer details
	// #nosec G204 -- namespace from CLI flag; kubectl validates namespace names.
	getServersCmd, err := m.kubectl.CommandArgs([]string{"get", "mcpserver", "-n", namespace, "-o", "jsonpath={range .items[*]}{.metadata.name}|{.spec.image}:{.spec.imageTag}|{.spec.replicas}|{.spec.ingressPath}|{.spec.useProvisionedRegistry}{\"\\n\"}{end}"})
	if err != nil {
		return err
	}
	out, err := getServersCmd.CombinedOutput()
	if err != nil {
		errDetails := strings.TrimSpace(string(out))
		if errDetails == "" {
			errDetails = err.Error()
		}
		core.DefaultPrinter.Println("ERROR: Failed to list MCP servers: " + kubeerr.WithDirectModeHint(errDetails))
		wrappedErr := core.WrapWithSentinelAndContext(
			core.ErrGetMCPServerFailed,
			err,
			kubeerr.DirectModeFailureMessage("kubectl get mcpserver failed", errDetails),
			map[string]any{"namespace": namespace, "component": "server"},
		)
		core.LogStructuredError(m.logger, wrappedErr, "Failed to get MCP servers")
		return wrappedErr
	}

	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		core.Warn("No MCP servers found in namespace " + namespace)
		return nil
	}
	rawLines := strings.Split(trimmed, "\n")
	lines := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		core.Warn("No MCP servers found in namespace " + namespace)
		return nil
	}

	// Build table
	tableData := [][]string{
		{"Name", "Image", "Replicas", "Path", "Registry"},
	}

	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) >= 5 {
			name := parts[0]
			image := parts[1]
			replicas := parts[2]
			path := parts[3]
			useProv := parts[4]

			registry := "custom"
			if useProv == "true" {
				registry = "provisioned"
			}

			tableData = append(tableData, []string{name, image, replicas, path, registry})
		}
	}

	if len(tableData) > 1 {
		core.TableBoxed(tableData)
	}

	// Pod status section
	core.DefaultPrinter.Println()
	core.Section("Pod Status")

	// #nosec G204 -- namespace from CLI flag; fixed label selector.
	podCmd, err := m.kubectl.CommandArgs([]string{"get", "pods", "-n", namespace, "-l", core.SelectorManagedBy, "-o", "custom-columns=NAME:.metadata.name,READY:.status.containerStatuses[0].ready,STATUS:.status.phase,RESTARTS:.status.containerStatuses[0].restartCount"})
	if err != nil {
		return err
	}
	podOut, err := podCmd.Output()
	if err != nil {
		core.Warn("Failed to list pods: " + err.Error())
		return nil
	}
	trimmedPods := strings.TrimSpace(string(podOut))
	if trimmedPods == "" {
		return nil
	}
	rawPodLines := strings.Split(trimmedPods, "\n")
	podLines := make([]string, 0, len(rawPodLines))
	for _, line := range rawPodLines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		podLines = append(podLines, line)
	}
	if len(podLines) > 1 {
		podData := [][]string{}
		for _, pl := range podLines {
			podData = append(podData, strings.Fields(pl))
		}
		core.Table(podData)
	} else {
		core.Info("No pods found")
	}

	return nil
}

type mcpServerManifest struct {
	APIVersion string           `yaml:"apiVersion"`
	Kind       string           `yaml:"kind"`
	Metadata   manifestMetadata `yaml:"metadata"`
	Spec       manifestSpec     `yaml:"spec"`
}

type manifestMetadata struct {
	Name      string `yaml:"name"`
	Namespace string `yaml:"namespace"`
}

type manifestSpec struct {
	Image       string `yaml:"image"`
	ImageTag    string `yaml:"imageTag"`
	Replicas    int    `yaml:"replicas"`
	Port        int    `yaml:"port"`
	ServicePort int    `yaml:"servicePort"`
	IngressPath string `yaml:"ingressPath"`
}
