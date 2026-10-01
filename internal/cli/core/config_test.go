package core

import (
	"testing"
	"time"
)

func TestParseDurationEnv(t *testing.T) {
	t.Setenv("MCP_DEPLOYMENT_TIMEOUT", "2s")
	if got := parseDurationEnv("MCP_DEPLOYMENT_TIMEOUT", 5*time.Second); got != 2*time.Second {
		t.Fatalf("expected 2s, got %s", got)
	}

	t.Setenv("MCP_DEPLOYMENT_TIMEOUT", "bad")
	if got := parseDurationEnv("MCP_DEPLOYMENT_TIMEOUT", 5*time.Second); got != 5*time.Second {
		t.Fatalf("expected default on invalid duration, got %s", got)
	}
}

func TestParseIntEnv(t *testing.T) {
	t.Setenv("MCP_REGISTRY_PORT", "6000")
	if got := parseIntEnv("MCP_REGISTRY_PORT", 5000); got != 6000 {
		t.Fatalf("expected 6000, got %d", got)
	}

	t.Setenv("MCP_REGISTRY_PORT", "-1")
	if got := parseIntEnv("MCP_REGISTRY_PORT", 5000); got != 5000 {
		t.Fatalf("expected default on negative value, got %d", got)
	}

	t.Setenv("MCP_REGISTRY_PORT", "bad")
	if got := parseIntEnv("MCP_REGISTRY_PORT", 5000); got != 5000 {
		t.Fatalf("expected default on invalid int, got %d", got)
	}
}

func TestGetEnvOrDefault(t *testing.T) {
	t.Setenv("MCP_SKOPEO_IMAGE", "example/image:tag")
	if got := getEnvOrDefault("MCP_SKOPEO_IMAGE", "default"); got != "example/image:tag" {
		t.Fatalf("expected env value, got %q", got)
	}

	t.Setenv("MCP_SKOPEO_IMAGE", "")
	if got := getEnvOrDefault("MCP_SKOPEO_IMAGE", "default"); got != "default" {
		t.Fatalf("expected default value, got %q", got)
	}
}

func TestLoadCLIConfigWithProvisionedRegistry(t *testing.T) {
	t.Setenv("MCP_PLATFORM_DOMAIN", "")
	t.Setenv("MCP_MCP_INGRESS_HOST", "")
	t.Setenv("MCP_DEPLOYMENT_TIMEOUT", "3s")
	t.Setenv("MCP_CERT_TIMEOUT", "30s")
	t.Setenv("MCP_REGISTRY_PORT", "6000")
	t.Setenv("MCP_REGISTRY_HOST", "")
	t.Setenv("MCP_REGISTRY_ENDPOINT", "10.43.39.164:5000")
	t.Setenv("MCP_REGISTRY_INGRESS_HOST", "registry.prod.mcpruntime.com")
	t.Setenv("MCP_SKOPEO_IMAGE", "example/skopeo:latest")
	t.Setenv("MCP_OPERATOR_IMAGE", "example/operator:latest")
	t.Setenv("MCP_GATEWAY_PROXY_IMAGE", "example/mcp-gateway:latest")
	t.Setenv("MCP_IMAGE_PLATFORM", "linux/amd64")
	t.Setenv("MCP_GATEWAY_OTEL_EXPORTER_OTLP_ENDPOINT", "http://custom-otel:4318")
	t.Setenv("MCP_SENTINEL_INGEST_URL", "http://mcp-ingest.mcp-observability.svc.cluster.local:8081/events")
	t.Setenv("MCP_INGRESS_READINESS_MODE", "permissive")
	t.Setenv("MCP_DEFAULT_SERVER_PORT", "9000")
	t.Setenv("PROVISIONED_REGISTRY_URL", "registry.mcpruntime.com")
	t.Setenv("PROVISIONED_REGISTRY_USERNAME", "user")
	t.Setenv("PROVISIONED_REGISTRY_PASSWORD", "pass")

	cfg := LoadCLIConfig()
	if cfg.DeploymentTimeout != 3*time.Second {
		t.Fatalf("expected deployment timeout 3s, got %s", cfg.DeploymentTimeout)
	}
	if cfg.CertTimeout != 30*time.Second {
		t.Fatalf("expected cert timeout 30s, got %s", cfg.CertTimeout)
	}
	if cfg.RegistryPort != 6000 {
		t.Fatalf("expected registry port 6000, got %d", cfg.RegistryPort)
	}
	if cfg.RegistryEndpoint != "10.43.39.164:5000" {
		t.Fatalf("expected registry endpoint override, got %q", cfg.RegistryEndpoint)
	}
	if cfg.RegistryIngressHost != "registry.prod.mcpruntime.com" {
		t.Fatalf("expected registry ingress host override, got %q", cfg.RegistryIngressHost)
	}
	if cfg.McpIngressHost != "" {
		t.Fatalf("expected empty mcp ingress host, got %q", cfg.McpIngressHost)
	}
	if cfg.SkopeoImage != "example/skopeo:latest" {
		t.Fatalf("expected skopeo image override, got %q", cfg.SkopeoImage)
	}
	if cfg.OperatorImage != "example/operator:latest" {
		t.Fatalf("expected operator image override, got %q", cfg.OperatorImage)
	}
	if cfg.GatewayProxyImage != "example/mcp-gateway:latest" {
		t.Fatalf("expected gateway proxy image override, got %q", cfg.GatewayProxyImage)
	}
	if cfg.ImagePlatform != "linux/amd64" {
		t.Fatalf("expected image platform override, got %q", cfg.ImagePlatform)
	}
	if cfg.GatewayOTLPEndpoint != "http://custom-otel:4318" {
		t.Fatalf("expected gateway OTLP endpoint override, got %q", cfg.GatewayOTLPEndpoint)
	}
	if cfg.AnalyticsIngestURL != "http://mcp-ingest.mcp-observability.svc.cluster.local:8081/events" {
		t.Fatalf("expected analytics ingest url override, got %q", cfg.AnalyticsIngestURL)
	}
	if cfg.IngressReadinessMode != "permissive" {
		t.Fatalf("expected ingress readiness mode override, got %q", cfg.IngressReadinessMode)
	}
	if cfg.DefaultServerPort != 9000 {
		t.Fatalf("expected default server port 9000, got %d", cfg.DefaultServerPort)
	}
	if cfg.ProvisionedRegistryURL != "registry.mcpruntime.com" {
		t.Fatalf("expected registry url, got %q", cfg.ProvisionedRegistryURL)
	}
	if cfg.ProvisionedRegistryUsername != "user" || cfg.ProvisionedRegistryPassword != "pass" {
		t.Fatalf("expected registry credentials, got %q/%q", cfg.ProvisionedRegistryUsername, cfg.ProvisionedRegistryPassword)
	}
}

func TestLoadCLIConfigPlatformDomain(t *testing.T) {
	for _, k := range []string{
		"MCP_REGISTRY_ENDPOINT", "MCP_REGISTRY_HOST", "MCP_REGISTRY_INGRESS_HOST", "MCP_MCP_INGRESS_HOST",
	} {
		t.Setenv(k, "")
	}
	t.Setenv("MCP_PLATFORM_DOMAIN", "mcpruntime.com")
	cfg := LoadCLIConfig()
	if cfg.RegistryEndpoint != "registry.mcpruntime.com" {
		t.Fatalf("expected registry endpoint from platform, got %q", cfg.RegistryEndpoint)
	}
	if cfg.RegistryIngressHost != "registry.mcpruntime.com" {
		t.Fatalf("expected registry ingress from platform, got %q", cfg.RegistryIngressHost)
	}
	if cfg.McpIngressHost != "mcp.mcpruntime.com" {
		t.Fatalf("expected mcp host from platform, got %q", cfg.McpIngressHost)
	}
}

func TestLoadCLIConfigUsesLegacyAnalyticsEnv(t *testing.T) {
	t.Setenv("MCP_PLATFORM_DOMAIN", "")
	t.Setenv("MCP_SENTINEL_INGEST_URL", "")
	t.Setenv("MCP_ANALYTICS_INGEST_URL", "http://legacy-ingest")

	cfg := LoadCLIConfig()
	if cfg.AnalyticsIngestURL != "http://legacy-ingest" {
		t.Fatalf("expected legacy analytics ingest url override, got %q", cfg.AnalyticsIngestURL)
	}
}

func TestConfigAccessors(t *testing.T) {
	orig := DefaultCLIConfig
	t.Cleanup(func() { DefaultCLIConfig = orig })

	DefaultCLIConfig = &CLIConfig{
		DeploymentTimeout:   10 * time.Second,
		CertTimeout:         15 * time.Second,
		RegistryPort:        7000,
		RegistryEndpoint:    "10.43.39.164:5000",
		RegistryIngressHost: "registry.prod.mcpruntime.com",
		McpIngressHost:      "mcp.prod.mcpruntime.com",
		SkopeoImage:         "skopeo:test",
		OperatorImage:       "operator:test",
		GatewayProxyImage:   "proxy:test",
		GatewayOTLPEndpoint: "http://otel:test",
		AnalyticsIngestURL:  "http://analytics-ingest",
		DefaultServerPort:   7070,
	}

	if GetDeploymentTimeout() != 10*time.Second {
		t.Fatalf("GetDeploymentTimeout mismatch")
	}
	if GetCertTimeout() != 15*time.Second {
		t.Fatalf("GetCertTimeout mismatch")
	}
	if GetRegistryPort() != 7000 {
		t.Fatalf("GetRegistryPort mismatch")
	}
	if GetRegistryEndpoint() != "10.43.39.164:5000" {
		t.Fatalf("GetRegistryEndpoint mismatch")
	}
	if GetRegistryIngressHost() != "registry.prod.mcpruntime.com" {
		t.Fatalf("GetRegistryIngressHost mismatch")
	}
	if GetMcpIngressHost() != "mcp.prod.mcpruntime.com" {
		t.Fatalf("GetMcpIngressHost mismatch")
	}
	if GetSkopeoImage() != "skopeo:test" {
		t.Fatalf("GetSkopeoImage mismatch")
	}
	if GetOperatorImageOverride() != "operator:test" {
		t.Fatalf("GetOperatorImageOverride mismatch")
	}
	if GetGatewayProxyImageOverride() != "proxy:test" {
		t.Fatalf("GetGatewayProxyImageOverride mismatch")
	}
	if GetGatewayOTLPEndpointOverride() != "http://otel:test" {
		t.Fatalf("GetGatewayOTLPEndpointOverride mismatch")
	}
	if GetAnalyticsIngestURLOverride() != "http://analytics-ingest" {
		t.Fatalf("GetAnalyticsIngestURLOverride mismatch")
	}
	if GetDefaultServerPort() != 7070 {
		t.Fatalf("GetDefaultServerPort mismatch")
	}
}
