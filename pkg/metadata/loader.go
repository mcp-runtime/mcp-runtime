package metadata

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"mcp-runtime/pkg/credentialheaders"
	"mcp-runtime/pkg/mcpdefaults"
	"mcp-runtime/pkg/publishscope"
)

const DefaultRegistryHost = "registry.local"

// LoadFromFile reads a single registry YAML file from disk and applies default values.
func LoadFromFile(filePath string) (*RegistryFile, error) {
	cleanPath := filepath.Clean(filePath)
	// #nosec G304 -- path is user-supplied for local metadata loading.
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	var registry RegistryFile
	if err := yaml.Unmarshal(data, &registry); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	// Set defaults
	for i := range registry.Servers {
		if err := setDefaults(&registry.Servers[i]); err != nil {
			name := registry.Servers[i].Name
			if strings.TrimSpace(name) == "" {
				name = fmt.Sprintf("#%d", i+1)
			}
			return nil, fmt.Errorf("server %s: %w", name, err)
		}
	}

	return &registry, nil
}

// LoadFromDirectory aggregates all .yaml/.yml registry files in a directory into one registry object.
func LoadFromDirectory(dirPath string) (*RegistryFile, error) {
	files, err := filepath.Glob(filepath.Join(dirPath, "*.yaml"))
	if err != nil {
		return nil, fmt.Errorf("failed to list files: %w", err)
	}

	ymlFiles, err := filepath.Glob(filepath.Join(dirPath, "*.yml"))
	if err != nil {
		return nil, fmt.Errorf("failed to list files: %w", err)
	}

	files = append(files, ymlFiles...)

	var allServers []ServerMetadata
	for _, file := range files {
		registry, err := LoadFromFile(file)
		if err != nil {
			return nil, fmt.Errorf("failed to load %s: %w", file, err)
		}
		allServers = append(allServers, registry.Servers...)
	}

	return &RegistryFile{
		Version: "v1",
		Servers: allServers,
	}, nil
}

func setDefaults(server *ServerMetadata) error {
	scope, err := publishscope.Normalize(string(server.Scope))
	if err != nil {
		return err
	}
	server.Scope = PublishScope(scope)

	// Set default image if not provided (will be updated by build command).
	// ResolveRegistryHost names the platform registry for docker push/build. Pipeline
	// generation rewrites unqualified or platform-registry MCPServer image refs to
	// ResolveRegistryPullHost for kubelet pulls, while preserving external registries.
	if server.Image == "" {
		repository := server.Name
		if alias, ok := publishscope.RegistryAlias(scope); ok {
			repository = alias + "/" + repository
		}
		server.Image = fmt.Sprintf("%s/%s", ResolveRegistryHost(), repository)
	}
	if server.ImageTag == "" {
		server.ImageTag = "latest"
	}
	if server.Route == "" {
		server.Route = mcpdefaults.DefaultIngressPath(server.Name)
	} else if server.Route[0] != '/' {
		server.Route = "/" + server.Route
	}
	if server.Port == 0 {
		server.Port = mcpdefaults.MCPServerPort
	}
	if server.Replicas == nil {
		replicas := int32(1)
		server.Replicas = &replicas
	}
	if server.Namespace == "" {
		if namespace, ok := publishscope.CatalogNamespace(scope); ok {
			server.Namespace = namespace
		} else {
			server.Namespace = mcpdefaults.MCPServersNamespace
		}
	}
	if server.Auth != nil && strings.EqualFold(strings.TrimSpace(server.Auth.Mode), mcpdefaults.AuthModeHeader) {
		if server.Auth.CredentialPresence == "" {
			server.Auth.CredentialPresence = mcpdefaults.CredentialPresenceAny
		}
		if server.Policy == nil {
			server.Policy = &PolicyConfig{
				Mode:            PolicyModeAllowList,
				DefaultDecision: PolicyDecisionDeny,
				EnforceOn:       mcpdefaults.PolicyEnforceOn,
				PolicyVersion:   mcpdefaults.PolicyVersion,
			}
		}
	} else if server.Auth != nil {
		if server.Auth.TokenHeader == "" {
			server.Auth.TokenHeader = mcpdefaults.AuthTokenHeader
		}
	}
	if server.Policy != nil {
		if server.Policy.Mode == "" {
			server.Policy.Mode = PolicyModeAllowList
		}
		if server.Policy.DefaultDecision == "" {
			server.Policy.DefaultDecision = PolicyDecisionDeny
		}
		if server.Policy.EnforceOn == "" {
			server.Policy.EnforceOn = mcpdefaults.PolicyEnforceOn
		}
		if server.Policy.PolicyVersion == "" {
			server.Policy.PolicyVersion = mcpdefaults.PolicyVersion
		}
	}
	if server.Session != nil {
		if server.Session.Store == "" {
			server.Session.Store = mcpdefaults.SessionStore
		}
		if server.Session.MaxLifetime == "" {
			server.Session.MaxLifetime = mcpdefaults.SessionMaxLife
		}
		if server.Session.IdleTimeout == "" {
			server.Session.IdleTimeout = mcpdefaults.SessionIdleTime
		}
		if server.Session.UpstreamTokenHeader == "" {
			server.Session.UpstreamTokenHeader = mcpdefaults.SessionUpstream
		}
	}
	for i := range server.Tools {
		if server.Tools[i].RequiredTrust == "" {
			server.Tools[i].RequiredTrust = TrustLevelLow
		}
	}
	if server.Gateway != nil && GatewayIsEnabled(server.Gateway) {
		if server.Gateway.Port == 0 {
			server.Gateway.Port = mcpdefaults.MCPGatewayPort
		}
		if server.Gateway.UpstreamURL == "" {
			server.Gateway.UpstreamURL = fmt.Sprintf("http://127.0.0.1:%d", server.Port)
		}
	}
	if server.Analytics != nil && !server.Analytics.Disabled {
		if server.Analytics.Source == "" {
			server.Analytics.Source = server.Name + "-gateway"
		}
		if server.Analytics.EventType == "" {
			server.Analytics.EventType = "mcp.request"
		}
	}
	if server.Rollout != nil {
		if server.Rollout.Strategy == "" {
			server.Rollout.Strategy = RolloutStrategyRollingUpdate
		}
		if server.Rollout.MaxUnavailable == "" {
			server.Rollout.MaxUnavailable = "25%"
		}
		if server.Rollout.MaxSurge == "" {
			server.Rollout.MaxSurge = "25%"
		}
	}
	return validateHeaderAuth(server)
}

func validateHeaderAuth(server *ServerMetadata) error {
	if server == nil || server.Auth == nil || !strings.EqualFold(strings.TrimSpace(server.Auth.Mode), mcpdefaults.AuthModeHeader) {
		if server != nil && server.Policy != nil && (len(server.Policy.DelegatedToolRules) > 0 || server.Policy.MaxSideEffect != "") {
			return fmt.Errorf("delegated tool rules require auth.mode header")
		}
		return nil
	}
	if server.Auth.TokenHeader != "" || server.Auth.IssuerURL != "" || server.Auth.Audience != "" || len(server.Auth.Scopes) > 0 {
		return fmt.Errorf("header mode cannot set tokenHeader, issuerURL, audience, or scopes")
	}
	if err := credentialheaders.NormalizeNames(server.Auth.Headers); err != nil {
		return err
	}
	if _, err := credentialheaders.NormalizePresence(server.Auth.CredentialPresence); err != nil {
		return err
	}
	if server.Session != nil {
		return fmt.Errorf("header mode cannot use a Runtime session")
	}
	if server.Policy != nil && (server.Policy.Mode != "" && server.Policy.Mode != PolicyModeAllowList || server.Policy.DefaultDecision != "" && server.Policy.DefaultDecision != PolicyDecisionDeny) {
		return fmt.Errorf("header mode requires allow-list policy and default decision deny")
	}
	return nil
}
