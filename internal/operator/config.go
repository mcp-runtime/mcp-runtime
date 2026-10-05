package operator

import (
	"os"
	"strconv"
	"strings"

	"mcp-runtime/pkg/metadata"
)

// OperatorConfig holds configuration for the operator loaded from environment variables.
type OperatorConfig struct {
	// DefaultIngressHost is the default host for ingress resources.
	DefaultIngressHost string

	// DefaultIngressClass is the ingress class to use.
	DefaultIngressClass string

	// DefaultIngressEntryPoints is the default Traefik entrypoint annotation for MCP server ingresses.
	DefaultIngressEntryPoints string

	// DefaultIngressTLS enables Traefik TLS routing for MCP server ingresses by default.
	DefaultIngressTLS bool

	// IngressReadinessMode controls how ingress readiness is evaluated.
	IngressReadinessMode string

	// ProvisionedRegistryURL is the URL of the provisioned registry.
	ProvisionedRegistryURL string

	// ProvisionedRegistryUsername is the username for the provisioned registry.
	ProvisionedRegistryUsername string

	// ProvisionedRegistryPassword is the password for the provisioned registry.
	ProvisionedRegistryPassword string

	// ProvisionedRegistrySecretName is the name of the secret for registry credentials.
	ProvisionedRegistrySecretName string

	// InternalRegistryEndpoint is the internal registry endpoint to use for image refs when not using a provisioned registry.
	InternalRegistryEndpoint string

	// RegistryPullHost is the pullable registry host used in image refs when the operator
	// needs to rewrite images to the platform-managed registry.
	RegistryPullHost string

	// RequeueDelaySeconds is the delay in seconds before requeueing when resources aren't ready.
	RequeueDelaySeconds int

	// GatewayProxyImage is the default image used for the optional MCP gateway sidecar.
	GatewayProxyImage string

	// GatewayOTLPEndpoint is the OTLP/HTTP endpoint injected into MCP gateway sidecars.
	GatewayOTLPEndpoint string

	// AnalyticsIngestURL is the default analytics ingest endpoint for gateway sidecars.
	AnalyticsIngestURL string

	// OAuthInternalIssuerURL is the in-cluster URL used by gateway sidecars for
	// OAuth metadata and JWKS discovery.
	OAuthInternalIssuerURL string

	// ClusterName is the cluster label attached to emitted audit events.
	ClusterName string
}

// LoadOperatorConfig loads operator configuration from environment variables.
func LoadOperatorConfig() *OperatorConfig {
	ingressReadinessMode, _ := NormalizeIngressReadinessMode(os.Getenv("MCP_INGRESS_READINESS_MODE"))
	mcpIngressHost := metadata.ResolveMcpIngressHost()
	if mcpIngressHost == "" {
		mcpIngressHost = getEnvCompat("MCP_DEFAULT_INGRESS_HOST", "DEFAULT_INGRESS_HOST")
	}
	cfg := &OperatorConfig{
		DefaultIngressHost:            mcpIngressHost,
		DefaultIngressClass:           getEnvOrDefault("DEFAULT_INGRESS_CLASS", DefaultIngressClass),
		DefaultIngressEntryPoints:     strings.TrimSpace(os.Getenv("MCP_DEFAULT_INGRESS_ENTRYPOINTS")),
		DefaultIngressTLS:             getEnvBool("MCP_DEFAULT_INGRESS_TLS"),
		IngressReadinessMode:          ingressReadinessMode,
		ProvisionedRegistryURL:        os.Getenv("PROVISIONED_REGISTRY_URL"),
		ProvisionedRegistryUsername:   os.Getenv("PROVISIONED_REGISTRY_USERNAME"),
		ProvisionedRegistryPassword:   os.Getenv("PROVISIONED_REGISTRY_PASSWORD"),
		ProvisionedRegistrySecretName: getEnvOrDefault("PROVISIONED_REGISTRY_SECRET_NAME", DefaultRegistrySecretName),
		InternalRegistryEndpoint:      getEnvOrDefault("MCP_REGISTRY_ENDPOINT", "registry.registry.svc.cluster.local:5000"),
		RegistryPullHost:              getEnvOrDefault("MCP_REGISTRY_PULL_HOST", getEnvOrDefault("MCP_REGISTRY_ENDPOINT", "registry.registry.svc.cluster.local:5000")),
		RequeueDelaySeconds:           getEnvIntOrDefault("REQUEUE_DELAY_SECONDS", RequeueDelayNotReady),
		GatewayProxyImage:             os.Getenv("MCP_GATEWAY_PROXY_IMAGE"),
		GatewayOTLPEndpoint:           os.Getenv("MCP_GATEWAY_OTEL_EXPORTER_OTLP_ENDPOINT"),
		AnalyticsIngestURL:            os.Getenv("MCP_ANALYTICS_INGEST_URL"),
		OAuthInternalIssuerURL:        strings.TrimSpace(os.Getenv("OAUTH_INTERNAL_ISSUER_URL")),
		ClusterName:                   getEnvOrDefault("MCP_CLUSTER_NAME", "local"),
	}
	return cfg
}

// HasProvisionedRegistry returns true if a provisioned registry is configured.
func (c *OperatorConfig) HasProvisionedRegistry() bool {
	return c.ProvisionedRegistryURL != ""
}

// ToRegistryConfig converts the config to a RegistryConfig if provisioned registry is enabled.
func (c *OperatorConfig) ToRegistryConfig() *RegistryConfig {
	if !c.HasProvisionedRegistry() {
		return nil
	}
	return &RegistryConfig{
		URL:        c.ProvisionedRegistryURL,
		Username:   c.ProvisionedRegistryUsername,
		Password:   c.ProvisionedRegistryPassword,
		SecretName: c.ProvisionedRegistrySecretName,
	}
}

func getEnvOrDefault(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

func getEnvIntOrDefault(key string, defaultValue int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return defaultValue
}

func getEnvBool(key string) bool {
	v, err := strconv.ParseBool(strings.TrimSpace(os.Getenv(key)))
	return err == nil && v
}

func getEnvCompat(keys ...string) string {
	for _, key := range keys {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}
	return ""
}

// NormalizeIngressReadinessMode returns a supported ingress readiness mode.
// Empty or invalid values fall back to strict mode.
func NormalizeIngressReadinessMode(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", IngressReadinessModeStrict:
		return IngressReadinessModeStrict, true
	case IngressReadinessModePermissive:
		return IngressReadinessModePermissive, true
	default:
		return IngressReadinessModeStrict, false
	}
}

// DefaultOperatorConfig is the default configuration loaded at startup.
var DefaultOperatorConfig = LoadOperatorConfig()
