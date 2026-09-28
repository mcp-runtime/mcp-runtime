package metadata

import "mcp-runtime/pkg/mcpdefaults"

// +kubebuilder:validation:Enum=oauth
// +kubebuilder:validation:Enum=allow-list;observe
type PolicyMode string

const (
	PolicyModeAllowList PolicyMode = mcpdefaults.PolicyModeAllowList
	PolicyModeObserve   PolicyMode = "observe"
)

// +kubebuilder:validation:Enum=allow;deny
type PolicyDecision string

const (
	PolicyDecisionAllow PolicyDecision = mcpdefaults.PolicyDecisionAllow
	PolicyDecisionDeny  PolicyDecision = mcpdefaults.PolicyDecisionDeny
)

// +kubebuilder:validation:Enum=low;medium;high
type TrustLevel string

const (
	TrustLevelLow    TrustLevel = "low"
	TrustLevelMedium TrustLevel = "medium"
	TrustLevelHigh   TrustLevel = "high"
)

// +kubebuilder:validation:Enum=read;write;destructive
type ToolSideEffect string

const (
	ToolSideEffectRead        ToolSideEffect = "read"
	ToolSideEffectWrite       ToolSideEffect = "write"
	ToolSideEffectDestructive ToolSideEffect = "destructive"
)

type ToolRiskLevel string

const (
	ToolRiskLevelLow    ToolRiskLevel = "low"
	ToolRiskLevelMedium ToolRiskLevel = "medium"
	ToolRiskLevelHigh   ToolRiskLevel = "high"
)

// +kubebuilder:validation:Enum=RollingUpdate;Recreate;Canary
type RolloutStrategy string

const (
	RolloutStrategyRollingUpdate RolloutStrategy = "RollingUpdate"
	RolloutStrategyRecreate      RolloutStrategy = "Recreate"
	RolloutStrategyCanary        RolloutStrategy = "Canary"
)

// PublishScope selects the platform catalog or tenant boundary for publishing.
type PublishScope string

const (
	PublishScopeTenant PublishScope = "tenant"
	PublishScopeOrg    PublishScope = "org"
	PublishScopePublic PublishScope = "public"
)

// ServerMetadata defines the metadata for an MCP server.
type ServerMetadata struct {
	// Name is the unique name of the MCP server.
	Name string `yaml:"name" json:"name"`

	// Description is a human-readable summary of what the MCP server provides.
	Description string `yaml:"description,omitempty" json:"description,omitempty"`

	// Image is the container image for the server.
	Image string `yaml:"image" json:"image"`

	// ImageTag is the tag of the container image (defaults to "latest").
	ImageTag string `yaml:"imageTag,omitempty" json:"imageTag,omitempty"`

	// Route is the route path for the server (defaults to name/mcp).
	Route string `yaml:"route,omitempty" json:"route,omitempty"`

	// IngressHost is the hostname for the server ingress route.
	IngressHost string `yaml:"ingressHost,omitempty" json:"ingressHost,omitempty"`

	// PublicPathPrefix enables hostless path-based routing and resolves to /<publicPathPrefix>/mcp.
	PublicPathPrefix string `yaml:"publicPathPrefix,omitempty" json:"publicPathPrefix,omitempty"`

	// Port is the port the container listens on (defaults to 8088).
	Port int32 `yaml:"port,omitempty" json:"port,omitempty"`

	// Replicas is the number of desired replicas (defaults to 1).
	Replicas *int32 `yaml:"replicas,omitempty" json:"replicas,omitempty"`

	// Resources defines resource limits and requests.
	Resources *ResourceRequirements `yaml:"resources,omitempty" json:"resources,omitempty"`

	// EnvVars are literal environment variables to pass to the container.
	EnvVars []EnvVar `yaml:"envVars,omitempty" json:"envVars,omitempty"`

	// SecretEnvVars are secret-backed environment variables to pass to the container.
	SecretEnvVars []SecretEnvVar `yaml:"secretEnvVars,omitempty" json:"secretEnvVars,omitempty"`

	// Namespace is the Kubernetes namespace (defaults to "mcp-servers").
	Namespace string `yaml:"namespace,omitempty" json:"namespace,omitempty"`

	// Scope selects a publish destination: tenant, org, or public.
	Scope PublishScope `yaml:"scope,omitempty" json:"scope,omitempty"`

	// TeamID is the stable platform team identifier that owns the server.
	TeamID string `yaml:"teamID,omitempty" json:"teamID,omitempty"`

	// Tools describes the MCP tool inventory exposed by the server.
	Tools []ToolConfig `yaml:"tools,omitempty" json:"tools,omitempty"`

	// Prompts describes the MCP prompt inventory exposed by the server.
	Prompts []InventoryItem `yaml:"prompts,omitempty" json:"prompts,omitempty"`

	// MCPResources describes the MCP resource inventory exposed by the server.
	MCPResources []InventoryItem `yaml:"mcpResources,omitempty" json:"mcpResources,omitempty"`

	// Tasks describes task templates or workflows exposed by the server.
	Tasks []InventoryItem `yaml:"tasks,omitempty" json:"tasks,omitempty"`

	// Auth configures how the gateway extracts human, agent, and session identity.
	Auth *AuthConfig `yaml:"auth,omitempty" json:"auth,omitempty"`

	// Policy configures gateway-side authorization behavior.
	Policy *PolicyConfig `yaml:"policy,omitempty" json:"policy,omitempty"`

	// Session configures server-side agent session behavior.
	Session *SessionConfig `yaml:"session,omitempty" json:"session,omitempty"`

	// Gateway configures an optional MCP proxy sidecar in front of the server container.
	Gateway *GatewayConfig `yaml:"gateway,omitempty" json:"gateway,omitempty"`

	// Analytics configures analytics emission for the gateway sidecar.
	Analytics *AnalyticsConfig `yaml:"analytics,omitempty" json:"analytics,omitempty"`

	// Rollout configures deployment rollout behavior.
	Rollout *RolloutConfig `yaml:"rollout,omitempty" json:"rollout,omitempty"`
}

// ResourceRequirements defines resource limits and requests.
type ResourceRequirements struct {
	Limits   *ResourceList `yaml:"limits,omitempty" json:"limits,omitempty"`
	Requests *ResourceList `yaml:"requests,omitempty" json:"requests,omitempty"`
}

// ResourceList defines CPU and memory resources.
type ResourceList struct {
	CPU    string `yaml:"cpu,omitempty" json:"cpu,omitempty"`
	Memory string `yaml:"memory,omitempty" json:"memory,omitempty"`
}

// EnvVar defines a literal environment variable.
type EnvVar struct {
	Name  string `yaml:"name" json:"name"`
	Value string `yaml:"value" json:"value"`
}

// SecretEnvVar defines a secret-backed environment variable.
type SecretEnvVar struct {
	Name         string        `yaml:"name" json:"name"`
	SecretKeyRef *SecretKeyRef `yaml:"secretKeyRef,omitempty" json:"secretKeyRef,omitempty"`
}

// ToolConfig describes one MCP tool exposed by a server.
type ToolConfig struct {
	Name          string            `yaml:"name" json:"name"`
	Description   string            `yaml:"description,omitempty" json:"description,omitempty"`
	RequiredTrust TrustLevel        `yaml:"requiredTrust,omitempty" json:"requiredTrust,omitempty"`
	SideEffect    ToolSideEffect    `yaml:"sideEffect" json:"sideEffect"`
	RiskLevel     ToolRiskLevel     `yaml:"riskLevel,omitempty" json:"riskLevel,omitempty"`
	Labels        map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`
}

// InventoryItem describes a named MCP prompt, resource, or task.
type InventoryItem struct {
	Name        string            `yaml:"name" json:"name"`
	Description string            `yaml:"description,omitempty" json:"description,omitempty"`
	Labels      map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`
}

// AuthConfig enables optional OAuth authentication at the gateway.
type AuthConfig struct {
	TokenHeader string `yaml:"tokenHeader,omitempty" json:"tokenHeader,omitempty"`
	IssuerURL   string `yaml:"issuerURL,omitempty" json:"issuerURL,omitempty"`
	Audience    string `yaml:"audience,omitempty" json:"audience,omitempty"`
}

// PolicyConfig configures authorization behavior at the gateway.
type PolicyConfig struct {
	Mode            PolicyMode     `yaml:"mode,omitempty" json:"mode,omitempty"`
	DefaultDecision PolicyDecision `yaml:"defaultDecision,omitempty" json:"defaultDecision,omitempty"`
	EnforceOn       string         `yaml:"enforceOn,omitempty" json:"enforceOn,omitempty"`
	PolicyVersion   string         `yaml:"policyVersion,omitempty" json:"policyVersion,omitempty"`
}

// SessionConfig configures server-side agent session behavior.
type SessionConfig struct {
	Required            bool   `yaml:"required,omitempty" json:"required,omitempty"`
	Store               string `yaml:"store,omitempty" json:"store,omitempty"`
	MaxLifetime         string `yaml:"maxLifetime,omitempty" json:"maxLifetime,omitempty"`
	IdleTimeout         string `yaml:"idleTimeout,omitempty" json:"idleTimeout,omitempty"`
	UpstreamTokenHeader string `yaml:"upstreamTokenHeader,omitempty" json:"upstreamTokenHeader,omitempty"`
}

// GatewayConfig configures an optional MCP proxy sidecar for a server.
type GatewayConfig struct {
	Enabled     bool                  `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Image       string                `yaml:"image,omitempty" json:"image,omitempty"`
	Port        int32                 `yaml:"port,omitempty" json:"port,omitempty"`
	UpstreamURL string                `yaml:"upstreamURL,omitempty" json:"upstreamURL,omitempty"`
	StripPrefix string                `yaml:"stripPrefix,omitempty" json:"stripPrefix,omitempty"`
	Resources   *ResourceRequirements `yaml:"resources,omitempty" json:"resources,omitempty"`
}

// AnalyticsConfig configures analytics emission from the gateway sidecar.
// Emission is on by default whenever the operator has an analytics ingest URL
// configured; set Disabled to true to opt out per server.
type AnalyticsConfig struct {
	Disabled        bool          `yaml:"disabled,omitempty" json:"disabled,omitempty"`
	IngestURL       string        `yaml:"ingestURL,omitempty" json:"ingestURL,omitempty"`
	Source          string        `yaml:"source,omitempty" json:"source,omitempty"`
	EventType       string        `yaml:"eventType,omitempty" json:"eventType,omitempty"`
	APIKeySecretRef *SecretKeyRef `yaml:"apiKeySecretRef,omitempty" json:"apiKeySecretRef,omitempty"`
}

// RolloutConfig configures deployment rollout behavior.
type RolloutConfig struct {
	Strategy       RolloutStrategy `yaml:"strategy,omitempty" json:"strategy,omitempty"`
	MaxUnavailable string          `yaml:"maxUnavailable,omitempty" json:"maxUnavailable,omitempty"`
	MaxSurge       string          `yaml:"maxSurge,omitempty" json:"maxSurge,omitempty"`
	CanaryReplicas *int32          `yaml:"canaryReplicas,omitempty" json:"canaryReplicas,omitempty"`
}

// SecretKeyRef points to a single key in a Kubernetes Secret.
type SecretKeyRef struct {
	Name string `yaml:"name" json:"name"`
	Key  string `yaml:"key" json:"key"`
}

// RegistryFile represents the complete registry/metadata file.
type RegistryFile struct {
	// Version of the metadata format.
	Version string `yaml:"version" json:"version"`

	// Servers is a list of MCP server definitions.
	Servers []ServerMetadata `yaml:"servers" json:"servers"`
}
