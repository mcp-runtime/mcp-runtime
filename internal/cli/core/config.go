package core

// This file defines CLI configuration loading from environment variables.
// CLIConfig holds all CLI settings including timeouts, registry settings, and server defaults.

import (
	"os"
	"strconv"
	"time"

	"mcp-runtime/pkg/mcpdefaults"
	"mcp-runtime/pkg/metadata"
	"mcp-runtime/pkg/publicroutes"
)

// CLIConfig holds all CLI configuration loaded from environment variables.
// Use LoadCLIConfig() to create an instance with values from the environment.
type CLIConfig struct {
	// Timeouts
	DeploymentTimeout time.Duration
	CertTimeout       time.Duration
	HelperPodTimeout  time.Duration

	// Registry settings
	RegistryPort int
	// KubernetesAPIPort is the host-facing Kubernetes API port used by the
	// runtime API NetworkPolicy on distributions such as k3s.
	KubernetesAPIPort   int
	RegistryEndpoint    string
	RegistryIngressHost string
	// McpIngressHost is the public gateway / MCP host (e.g. mcp.mcpruntime.com), from
	// MCP_MCP_INGRESS_HOST or mcp.<MCP_PLATFORM_DOMAIN>. Empty if unset.
	McpIngressHost string
	// PlatformIngressHost is the public dashboard UI host (e.g. platform.mcpruntime.com), from
	// MCP_PLATFORM_INGRESS_HOST or platform.<MCP_PLATFORM_DOMAIN>. Empty falls back to path-based dev routing.
	PlatformIngressHost string
	PublicRoutes        publicroutes.Routes
	// RegistryClusterIssuerName is the cert-manager ClusterIssuer selected by
	// setup --with-tls for TLS-rendered resources (e.g. platform UI ingress).
	// The registry Secret itself is owned by an explicit registry-cert Certificate.
	RegistryClusterIssuerName string
	// ProvidedTLSSecrets selects operator-managed TLS Secrets in place of
	// cert-manager-issued Certificates.
	ProvidedTLSSecrets   bool
	SkopeoImage          string
	OperatorImage        string // Override for operator image
	GatewayProxyImage    string // Optional default image for the MCP gateway sidecar
	ImagePlatform        string // Optional Docker image platform for setup-built images, e.g. linux/amd64
	GatewayOTLPEndpoint  string // Optional OTLP/HTTP endpoint for MCP gateway sidecar tracing
	AnalyticsIngestURL   string // Optional analytics ingest URL override for the MCP gateway sidecar
	IngressReadinessMode string // Optional operator ingress readiness mode: strict or permissive
	ClusterName          string // Optional cluster label attached to analytics/audit events

	// Server defaults
	DefaultServerPort int

	// External/Provisioned registry credentials
	ProvisionedRegistryURL      string
	ProvisionedRegistryUsername string
	ProvisionedRegistryPassword string
}

// Default values
const (
	defaultDeploymentTimeout   = 5 * time.Minute
	defaultCertTimeout         = 60 * time.Second
	defaultHelperPodTimeout    = 3 * time.Minute
	defaultRegistryPort        = 5000
	defaultKubernetesAPIPort   = 6443
	defaultRegistryEndpoint    = "registry.local" // used by build paths; same default as metadata.DefaultRegistryHost
	defaultRegistryIngressHost = "registry.local"
	// Exported aliases for tests and subpackages (same values as above).
	DefaultRegistryEndpoint    = defaultRegistryEndpoint
	DefaultRegistryIngressHost = defaultRegistryIngressHost
	defaultSkopeoImage         = "quay.io/skopeo/stable:v1.14"
	defaultServerPort          = mcpdefaults.MCPServerPort
)

// DefaultCLIConfig is the global CLI configuration loaded at startup.
var DefaultCLIConfig = LoadCLIConfig()

// LoadCLIConfig loads CLI configuration from environment variables.
func LoadCLIConfig() *CLIConfig {
	registryEndpoint := metadata.ResolveRegistryEndpoint()
	registryIngressHost := metadata.ResolveRegistryHost()
	mcpIngressHost := metadata.ResolveMcpIngressHost()
	platformIngressHost := metadata.ResolvePlatformIngressHost()
	return &CLIConfig{
		// Applies to core deployment waits and platform rollouts (ingest, Kafka, etc.).
		DeploymentTimeout:           parseDurationEnv("MCP_DEPLOYMENT_TIMEOUT", defaultDeploymentTimeout),
		CertTimeout:                 parseDurationEnv("MCP_CERT_TIMEOUT", defaultCertTimeout),
		HelperPodTimeout:            parseDurationEnv("MCP_HELPER_POD_TIMEOUT", defaultHelperPodTimeout),
		RegistryPort:                parseIntEnv("MCP_REGISTRY_PORT", defaultRegistryPort),
		KubernetesAPIPort:           parseIntEnv("MCP_KUBERNETES_API_PORT", defaultKubernetesAPIPort),
		RegistryEndpoint:            registryEndpoint,
		RegistryIngressHost:         registryIngressHost,
		McpIngressHost:              mcpIngressHost,
		PlatformIngressHost:         platformIngressHost,
		PublicRoutes:                publicroutes.Routes{Platform: os.Getenv("MCP_PLATFORM_PATH_PREFIX"), Grafana: os.Getenv("MCP_GRAFANA_PATH_PREFIX"), Docs: os.Getenv("MCP_DOCS_PATH"), DocsURL: os.Getenv("MCP_DOCS_URL"), Registry: os.Getenv("MCP_REGISTRY_PATH")}.WithDefaults(),
		SkopeoImage:                 getEnvOrDefault("MCP_SKOPEO_IMAGE", defaultSkopeoImage),
		OperatorImage:               os.Getenv("MCP_OPERATOR_IMAGE"), // No default, empty means auto
		GatewayProxyImage:           os.Getenv("MCP_GATEWAY_PROXY_IMAGE"),
		ImagePlatform:               os.Getenv("MCP_IMAGE_PLATFORM"),
		GatewayOTLPEndpoint:         os.Getenv("MCP_GATEWAY_OTEL_EXPORTER_OTLP_ENDPOINT"),
		AnalyticsIngestURL:          os.Getenv("MCP_ANALYTICS_INGEST_URL"),
		IngressReadinessMode:        os.Getenv("MCP_INGRESS_READINESS_MODE"),
		ClusterName:                 getEnvOrDefault("MCP_CLUSTER_NAME", "local"),
		DefaultServerPort:           parseIntEnv("MCP_DEFAULT_SERVER_PORT", defaultServerPort),
		ProvisionedRegistryURL:      os.Getenv("PROVISIONED_REGISTRY_URL"),
		ProvisionedRegistryUsername: os.Getenv("PROVISIONED_REGISTRY_USERNAME"),
		ProvisionedRegistryPassword: os.Getenv("PROVISIONED_REGISTRY_PASSWORD"),
	}
}

// parseDurationEnv parses a duration from an environment variable, returning the default if not set or invalid.
func parseDurationEnv(key string, defaultVal time.Duration) time.Duration {
	if val := os.Getenv(key); val != "" {
		if d, err := time.ParseDuration(val); err == nil {
			return d
		}
	}
	return defaultVal
}

// parseIntEnv parses an integer from an environment variable, returning the default if not set or invalid.
func parseIntEnv(key string, defaultVal int) int {
	if val := os.Getenv(key); val != "" {
		if i, err := strconv.Atoi(val); err == nil && i > 0 {
			return i
		}
	}
	return defaultVal
}

// getEnvOrDefault returns the environment variable value or the default if not set.
func getEnvOrDefault(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

// --- Convenience accessors using DefaultCLIConfig ---

// GetDeploymentTimeout returns the deployment wait timeout.
func GetDeploymentTimeout() time.Duration {
	return DefaultCLIConfig.DeploymentTimeout
}

// GetCertTimeout returns the certificate issuance timeout.
func GetCertTimeout() time.Duration {
	return DefaultCLIConfig.CertTimeout
}

// GetHelperPodTimeout returns the helper pod ready timeout (e.g. registry pusher pod).
func GetHelperPodTimeout() time.Duration {
	return DefaultCLIConfig.HelperPodTimeout
}

// GetRegistryPort returns the registry port.
func GetRegistryPort() int {
	return DefaultCLIConfig.RegistryPort
}

// GetRegistryEndpoint returns the configured registry endpoint for image refs and pushes.
func GetRegistryEndpoint() string {
	return DefaultCLIConfig.RegistryEndpoint
}

// GetRegistryIngressHost returns the configured registry ingress host.
func GetRegistryIngressHost() string {
	return DefaultCLIConfig.RegistryIngressHost
}

// GetMcpIngressHost returns the public MCP / gateway host (mcp.<domain> when
// MCP_PLATFORM_DOMAIN is set), or empty if not configured.
func GetMcpIngressHost() string {
	return DefaultCLIConfig.McpIngressHost
}

// GetPlatformIngressHost returns the public dashboard UI host (platform.<domain>
// when MCP_PLATFORM_DOMAIN is set), or empty if not configured. When empty the
// dev path-based routing on the gateway ingress is used.
func GetPlatformIngressHost() string {
	return DefaultCLIConfig.PlatformIngressHost
}

// GetRegistryClusterIssuerName returns the setup-selected cert-manager ClusterIssuer name (empty if unset).
func GetRegistryClusterIssuerName() string {
	return DefaultCLIConfig.RegistryClusterIssuerName
}

// GetProvidedTLSSecrets reports whether setup references operator-provided TLS
// Secrets rather than cert-manager-issued Certificates.
func GetProvidedTLSSecrets() bool {
	return DefaultCLIConfig.ProvidedTLSSecrets
}

// GetSkopeoImage returns the skopeo image for in-cluster operations.
func GetSkopeoImage() string {
	return DefaultCLIConfig.SkopeoImage
}

// GetOperatorImageOverride returns the operator image override, empty if not set.
func GetOperatorImageOverride() string {
	return DefaultCLIConfig.OperatorImage
}

// GetGatewayProxyImageOverride returns the gateway proxy image override, empty if not set.
func GetGatewayProxyImageOverride() string {
	return DefaultCLIConfig.GatewayProxyImage
}

// GetGatewayOTLPEndpointOverride returns the gateway OTLP endpoint override, empty if not set.
func GetGatewayOTLPEndpointOverride() string {
	return DefaultCLIConfig.GatewayOTLPEndpoint
}

// GetAnalyticsIngestURLOverride returns the analytics ingest URL override, empty if not set.
func GetAnalyticsIngestURLOverride() string {
	return DefaultCLIConfig.AnalyticsIngestURL
}

// GetClusterName returns the cluster label attached to analytics/audit events.
func GetClusterName() string {
	return DefaultCLIConfig.ClusterName
}

// GetDefaultServerPort returns the default MCP server port.
func GetDefaultServerPort() int {
	return DefaultCLIConfig.DefaultServerPort
}
