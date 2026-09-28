package metadata

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

// GenerateCRD generates a Kubernetes CRD YAML file for a single server metadata entry at the given output path.
func GenerateCRD(server *ServerMetadata, outputPath string) error {
	// Convert metadata to CRD
	mcpServer := &mcpv1alpha1.MCPServer{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "mcpruntime.org/v1alpha1",
			Kind:       "MCPServer",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      server.Name,
			Namespace: server.Namespace,
			Labels:    publishScopeLabels(server.Scope),
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Description: server.Description,
			TeamID:      server.TeamID,
			Image:       imageRefForClusterPull(server.Image),
			ImageTag:    server.ImageTag,
			Port:        server.Port,
			Replicas:    server.Replicas,
		},
	}

	// Set route (ingress path)
	mcpServer.Spec.IngressPath = server.Route
	mcpServer.Spec.IngressHost = server.IngressHost
	mcpServer.Spec.PublicPathPrefix = server.PublicPathPrefix

	// Set service port (default 80)
	if mcpServer.Spec.ServicePort == 0 {
		mcpServer.Spec.ServicePort = 80
	}

	// Convert resources
	if server.Resources != nil {
		mcpServer.Spec.Resources = *convertResourceRequirements(server.Resources)
	}

	// Convert environment variables
	if len(server.EnvVars) > 0 {
		mcpServer.Spec.EnvVars = make([]mcpv1alpha1.EnvVar, 0, len(server.EnvVars))
		for _, env := range server.EnvVars {
			mcpServer.Spec.EnvVars = append(mcpServer.Spec.EnvVars, mcpv1alpha1.EnvVar{
				Name:  env.Name,
				Value: env.Value,
			})
		}
	}

	if len(server.SecretEnvVars) > 0 {
		mcpServer.Spec.SecretEnvVars = make([]mcpv1alpha1.SecretEnvVar, 0, len(server.SecretEnvVars))
		for _, env := range server.SecretEnvVars {
			secretEnv := mcpv1alpha1.SecretEnvVar{Name: env.Name}
			if env.SecretKeyRef != nil {
				secretEnv.SecretKeyRef = &mcpv1alpha1.SecretKeyRef{
					Name: env.SecretKeyRef.Name,
					Key:  env.SecretKeyRef.Key,
				}
			}
			mcpServer.Spec.SecretEnvVars = append(mcpServer.Spec.SecretEnvVars, secretEnv)
		}
	}

	if len(server.Tools) > 0 {
		mcpServer.Spec.Tools = make([]mcpv1alpha1.ToolConfig, 0, len(server.Tools))
		for _, tool := range server.Tools {
			mcpTool := mcpv1alpha1.ToolConfig{
				Name:          tool.Name,
				Description:   tool.Description,
				RequiredTrust: mcpv1alpha1.TrustLevel(tool.RequiredTrust),
				SideEffect:    mcpv1alpha1.ToolSideEffect(tool.SideEffect),
				RiskLevel:     mcpv1alpha1.ToolRiskLevel(tool.RiskLevel),
			}
			if len(tool.Labels) > 0 {
				mcpTool.Labels = make(map[string]string, len(tool.Labels))
				for k, v := range tool.Labels {
					mcpTool.Labels[k] = v
				}
			}
			mcpServer.Spec.Tools = append(mcpServer.Spec.Tools, mcpTool)
		}
	}
	mcpServer.Spec.Prompts = convertInventoryItems(server.Prompts)
	mcpServer.Spec.MCPResources = convertInventoryItems(server.MCPResources)
	mcpServer.Spec.Tasks = convertInventoryItems(server.Tasks)

	if server.Auth != nil {
		mcpServer.Spec.Auth = &mcpv1alpha1.AuthConfig{
			TokenHeader: server.Auth.TokenHeader,
			IssuerURL:   server.Auth.IssuerURL,
			Audience:    server.Auth.Audience,
		}
	}

	if server.Policy != nil {
		mcpServer.Spec.Policy = &mcpv1alpha1.PolicyConfig{
			Mode:            mcpv1alpha1.PolicyMode(server.Policy.Mode),
			DefaultDecision: mcpv1alpha1.PolicyDecision(server.Policy.DefaultDecision),
			EnforceOn:       server.Policy.EnforceOn,
			PolicyVersion:   server.Policy.PolicyVersion,
		}
	}

	if server.Session != nil {
		mcpServer.Spec.Session = &mcpv1alpha1.SessionConfig{
			Required:            server.Session.Required,
			Store:               server.Session.Store,
			MaxLifetime:         server.Session.MaxLifetime,
			IdleTimeout:         server.Session.IdleTimeout,
			UpstreamTokenHeader: server.Session.UpstreamTokenHeader,
		}
	}

	if server.Gateway != nil {
		mcpServer.Spec.Gateway = &mcpv1alpha1.GatewayConfig{
			Enabled:     server.Gateway.Enabled,
			Image:       server.Gateway.Image,
			Port:        server.Gateway.Port,
			UpstreamURL: server.Gateway.UpstreamURL,
			StripPrefix: server.Gateway.StripPrefix,
		}
		if server.Gateway.Resources != nil {
			mcpServer.Spec.Gateway.Resources = convertResourceRequirements(server.Gateway.Resources)
		}
	}

	if server.Rollout != nil {
		mcpServer.Spec.Rollout = &mcpv1alpha1.RolloutConfig{
			Strategy:       mcpv1alpha1.RolloutStrategy(server.Rollout.Strategy),
			MaxUnavailable: server.Rollout.MaxUnavailable,
			MaxSurge:       server.Rollout.MaxSurge,
			CanaryReplicas: server.Rollout.CanaryReplicas,
		}
	}

	if server.Analytics != nil {
		mcpServer.Spec.Analytics = &mcpv1alpha1.AnalyticsConfig{
			Disabled:  server.Analytics.Disabled,
			IngestURL: server.Analytics.IngestURL,
			Source:    server.Analytics.Source,
			EventType: server.Analytics.EventType,
		}
		if server.Analytics.APIKeySecretRef != nil {
			mcpServer.Spec.Analytics.APIKeySecretRef = &mcpv1alpha1.SecretKeyRef{
				Name: server.Analytics.APIKeySecretRef.Name,
				Key:  server.Analytics.APIKeySecretRef.Key,
			}
		}
	}

	// Marshal to YAML
	data, err := yaml.Marshal(mcpServer)
	if err != nil {
		return fmt.Errorf("failed to marshal CRD: %w", err)
	}

	// Ensure output directory exists
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o750); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Write to file
	if err := os.WriteFile(outputPath, data, 0o600); err != nil {
		return fmt.Errorf("failed to write CRD file: %w", err)
	}

	return nil
}

func publishScopeLabels(scope PublishScope) map[string]string {
	if scope == "" {
		return nil
	}
	return map[string]string{"mcpruntime.org/scope": string(scope)}
}

func convertInventoryItems(items []InventoryItem) []mcpv1alpha1.InventoryItem {
	if len(items) == 0 {
		return nil
	}
	converted := make([]mcpv1alpha1.InventoryItem, 0, len(items))
	for _, item := range items {
		mcpItem := mcpv1alpha1.InventoryItem{
			Name:        item.Name,
			Description: item.Description,
		}
		if len(item.Labels) > 0 {
			mcpItem.Labels = make(map[string]string, len(item.Labels))
			for k, v := range item.Labels {
				mcpItem.Labels[k] = v
			}
		}
		converted = append(converted, mcpItem)
	}
	return converted
}

func convertResourceRequirements(resources *ResourceRequirements) *mcpv1alpha1.ResourceRequirements {
	if resources == nil {
		return nil
	}
	converted := &mcpv1alpha1.ResourceRequirements{}
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

func imageRefForClusterPull(image string) string {
	image = strings.TrimSpace(image)
	pullHost := ResolveRegistryPullHost()
	if pullHost == "" {
		return image
	}
	if pullHost == normalizeRegistryHost(ResolveRegistryHost()) {
		return image
	}
	registry, hasRegistry := imageRegistryHost(image)
	if hasRegistry && !isPlatformRegistryHost(registry) {
		return image
	}
	if rewritten, ok := RewriteImageRegistryHost(image, pullHost); ok {
		return rewritten
	}
	return image
}

func imageRegistryHost(image string) (string, bool) {
	first, _, found := strings.Cut(image, "/")
	if !found || !(strings.Contains(first, ".") || strings.Contains(first, ":") || first == "localhost") {
		return "", false
	}
	return normalizeRegistryHost(first), true
}

func isPlatformRegistryHost(host string) bool {
	host = normalizeRegistryHost(host)
	if host == "" {
		return false
	}
	for _, candidate := range []string{
		os.Getenv(envMCPRegistryIngressHost),
		os.Getenv(envMCPRegistryHost),
		os.Getenv(envMCPRegistryEndpoint),
		ResolveRegistryHost(),
	} {
		if normalized := normalizeRegistryHost(candidate); normalized != "" && normalized == host {
			return true
		}
	}
	if domain := platformDomainFromEnv(); domain != "" && host == registryHostForDomain(domain) {
		return true
	}
	// The local default and loopback names are platform placeholders: images
	// built for development are tagged with them and must still be rewritten
	// to the in-cluster pull host once a real registry is configured.
	if host == normalizeRegistryHost(DefaultRegistryHost) {
		return true
	}
	name := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		name = h
	}
	return name == "localhost" || name == "127.0.0.1"
}

func normalizeRegistryHost(host string) string {
	host = strings.TrimSpace(host)
	if scheme := strings.Index(host, "://"); scheme >= 0 {
		host = host[scheme+3:]
	}
	if before, _, found := strings.Cut(host, "/"); found {
		host = before
	}
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "/"))
}

// GenerateCRDsFromRegistry renders CRD YAML files for every server in a registry into outputDir.
func GenerateCRDsFromRegistry(registry *RegistryFile, outputDir string) error {
	if err := os.MkdirAll(outputDir, 0o750); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	for _, server := range registry.Servers {
		outputPath := filepath.Join(outputDir, fmt.Sprintf("%s.yaml", server.Name))
		if err := GenerateCRD(&server, outputPath); err != nil {
			return fmt.Errorf("failed to generate CRD for %s: %w", server.Name, err)
		}
	}

	return nil
}
