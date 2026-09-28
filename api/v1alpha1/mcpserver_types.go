package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"mcp-runtime/pkg/mcpdefaults"
)

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

// +kubebuilder:validation:Enum=low;medium;high
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

// MCPServerSpec defines the desired state of MCPServer.
// +kubebuilder:object:generate=true
type MCPServerSpec struct {
	// TeamID is the stable platform team identifier that owns this server.
	// The operator renders it into gateway policy and analytics events.
	TeamID string `json:"teamID,omitempty"`

	// Description is a human-readable summary of what the MCP server provides.
	Description string `json:"description,omitempty"`

	// Image is the container image for the MCP server.
	Image string `json:"image"`

	// ImageTag is the tag of the container image (defaults to "latest").
	ImageTag string `json:"imageTag,omitempty"`

	// RegistryOverride, if set, overrides the registry portion of the image (e.g., registry.mcpruntime.com).
	RegistryOverride string `json:"registryOverride,omitempty"`

	// UseProvisionedRegistry tells the controller to use the provisioned registry (from operator env) for this server.
	UseProvisionedRegistry bool `json:"useProvisionedRegistry,omitempty"`

	// ImagePullSecrets are secrets to use for pulling the image.
	ImagePullSecrets []string `json:"imagePullSecrets,omitempty"`

	// Replicas is the number of desired replicas (defaults to 1).
	Replicas *int32 `json:"replicas,omitempty"`

	// Port is the port the container listens on (defaults to 8088).
	Port int32 `json:"port,omitempty"`

	// ServicePort is the port exposed by the service (defaults to 80).
	ServicePort int32 `json:"servicePort,omitempty"`

	// IngressPath is the path for the ingress route (defaults to /{name}/mcp).
	IngressPath string `json:"ingressPath,omitempty"`

	// IngressHost is the hostname for the ingress (required unless publicPathPrefix is set; defaults from MCP_DEFAULT_INGRESS_HOST env var if set on the operator).
	IngressHost string `json:"ingressHost,omitempty"`

	// PublicPathPrefix enables path-based public routing and is used to compute /<publicPathPrefix>/mcp.
	// When ingressHost is also set, the route is path-based under that host; otherwise it is hostless.
	PublicPathPrefix string `json:"publicPathPrefix,omitempty"`

	// IngressClass is the ingress class to use (e.g., "traefik", "nginx", "istio"). Defaults to "traefik".
	IngressClass string `json:"ingressClass,omitempty"`

	// IngressAnnotations are additional annotations for the ingress controller.
	IngressAnnotations map[string]string `json:"ingressAnnotations,omitempty"`

	// Resources defines resource limits and requests.
	Resources ResourceRequirements `json:"resources,omitempty"`

	// EnvVars are literal environment variables to pass to the container.
	EnvVars []EnvVar `json:"envVars,omitempty"`

	// SecretEnvVars are secret-backed environment variables to pass to the container.
	SecretEnvVars []SecretEnvVar `json:"secretEnvVars,omitempty"`

	// Tools describes the MCP tool inventory exposed by the server.
	Tools []ToolConfig `json:"tools,omitempty"`

	// Prompts describes the MCP prompt inventory exposed by the server.
	Prompts []InventoryItem `json:"prompts,omitempty"`

	// MCPResources describes the MCP resource inventory exposed by the server.
	MCPResources []InventoryItem `json:"mcpResources,omitempty"`

	// Tasks describes task templates or workflows exposed by the server.
	Tasks []InventoryItem `json:"tasks,omitempty"`

	// Auth enables optional OAuth authentication at the gateway when present.
	Auth *AuthConfig `json:"auth,omitempty"`

	// Policy configures gateway-side authorization behavior.
	Policy *PolicyConfig `json:"policy,omitempty"`

	// Session configures server-side agent session behavior.
	Session *SessionConfig `json:"session,omitempty"`

	// Gateway configures an optional MCP proxy sidecar in front of the server container.
	Gateway *GatewayConfig `json:"gateway,omitempty"`

	// Analytics configures audit/analytics emission for the gateway sidecar.
	// Analytics is only applied when Gateway is enabled and this field is set.
	// If set without an ingest URL, the operator may supply its configured
	// default ingest URL.
	Analytics *AnalyticsConfig `json:"analytics,omitempty"`

	// Rollout configures deployment rollout behavior for this server.
	Rollout *RolloutConfig `json:"rollout,omitempty"`
}

// ResourceRequirements defines resource limits and requests.
// +kubebuilder:object:generate=true
type ResourceRequirements struct {
	Limits   *ResourceList `json:"limits,omitempty"`
	Requests *ResourceList `json:"requests,omitempty"`
}

// ResourceList defines CPU and memory resources.
// +kubebuilder:object:generate=true
type ResourceList struct {
	CPU    string `json:"cpu,omitempty"`
	Memory string `json:"memory,omitempty"`
}

// EnvVar represents a literal environment variable.
// +kubebuilder:object:generate=true
type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// SecretEnvVar represents a secret-backed environment variable.
// +kubebuilder:object:generate=true
type SecretEnvVar struct {
	Name         string        `json:"name"`
	SecretKeyRef *SecretKeyRef `json:"secretKeyRef,omitempty"`
}

// ToolConfig describes one MCP tool exposed by a server.
// +kubebuilder:object:generate=true
type ToolConfig struct {
	Name          string            `json:"name"`
	Description   string            `json:"description,omitempty"`
	RequiredTrust TrustLevel        `json:"requiredTrust,omitempty"`
	SideEffect    ToolSideEffect    `json:"sideEffect"`
	RiskLevel     ToolRiskLevel     `json:"riskLevel,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
}

// InventoryItem describes a named MCP prompt, resource, or task.
// +kubebuilder:object:generate=true
type InventoryItem struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
}

// AuthConfig configures OAuth authentication at the gateway.
// +kubebuilder:object:generate=true
type AuthConfig struct {
	TokenHeader string `json:"tokenHeader,omitempty"`
	IssuerURL   string `json:"issuerURL,omitempty"`
	// Audience is the OAuth resource identifier tokens must be issued for and
	// that protected-resource metadata advertises. When
	// this is unset, it defaults to the public MCP URL built from the ingress
	// host (or MCP_DEFAULT_INGRESS_HOST on the operator), TLS setting, and path.
	Audience string `json:"audience,omitempty"`
}

// PolicyConfig configures authorization behavior at the gateway.
// +kubebuilder:object:generate=true
type PolicyConfig struct {
	Mode            PolicyMode     `json:"mode,omitempty"`
	DefaultDecision PolicyDecision `json:"defaultDecision,omitempty"`
	EnforceOn       string         `json:"enforceOn,omitempty"`
	PolicyVersion   string         `json:"policyVersion,omitempty"`
}

// SessionConfig configures server-side agent session behavior.
// +kubebuilder:object:generate=true
type SessionConfig struct {
	Required            bool   `json:"required,omitempty"`
	Store               string `json:"store,omitempty"`
	MaxLifetime         string `json:"maxLifetime,omitempty"`
	IdleTimeout         string `json:"idleTimeout,omitempty"`
	UpstreamTokenHeader string `json:"upstreamTokenHeader,omitempty"`
}

// GatewayConfig configures an optional MCP proxy sidecar for a server.
// +kubebuilder:object:generate=true
type GatewayConfig struct {
	// Enabled turns on the gateway sidecar for this server.
	Enabled bool `json:"enabled,omitempty"`

	// Image overrides the proxy container image for this server.
	Image string `json:"image,omitempty"`

	// Port is the port the gateway listens on inside the pod (defaults to 8091).
	Port int32 `json:"port,omitempty"`

	// UpstreamURL is the upstream URL the gateway proxies to.
	// Defaults to http://127.0.0.1:<spec.port>.
	UpstreamURL string `json:"upstreamURL,omitempty"`

	// StripPrefix removes a path prefix before forwarding to the upstream server.
	StripPrefix string `json:"stripPrefix,omitempty"`

	// Resources defines resource limits and requests for the gateway sidecar.
	Resources *ResourceRequirements `json:"resources,omitempty"`
}

// AnalyticsConfig configures analytics emission from the gateway sidecar.
// +kubebuilder:object:generate=true
type AnalyticsConfig struct {
	// Disabled suppresses analytics emission from the gateway sidecar for this
	// server.
	Disabled bool `json:"disabled,omitempty"`

	// IngestURL is the analytics ingest endpoint. If empty, the operator may
	// supply its configured default ingest URL.
	IngestURL string `json:"ingestURL,omitempty"`

	// Source is the event source label attached to emitted analytics events.
	Source string `json:"source,omitempty"`

	// EventType is the event type label attached to emitted analytics events.
	EventType string `json:"eventType,omitempty"`

	// APIKeySecretRef points to a secret key containing the analytics API key.
	APIKeySecretRef *SecretKeyRef `json:"apiKeySecretRef,omitempty"`
}

// RolloutConfig configures deployment rollout behavior.
// +kubebuilder:object:generate=true
type RolloutConfig struct {
	Strategy       RolloutStrategy `json:"strategy,omitempty"`
	MaxUnavailable string          `json:"maxUnavailable,omitempty"`
	MaxSurge       string          `json:"maxSurge,omitempty"`
	CanaryReplicas *int32          `json:"canaryReplicas,omitempty"`
}

// SecretKeyRef points to a single key in a Kubernetes Secret.
// +kubebuilder:object:generate=true
type SecretKeyRef struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

// MCPServerStatus defines the observed state of MCPServer.
// +kubebuilder:object:generate=true
type MCPServerStatus struct {
	// Phase represents the current phase of the MCPServer.
	Phase string `json:"phase,omitempty"`

	// Message provides additional information about the status.
	Message string `json:"message,omitempty"`

	// Conditions represent the latest available observations.
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// DeploymentReady indicates if the deployment is ready.
	DeploymentReady bool `json:"deploymentReady,omitempty"`

	// ServiceReady indicates if the service is ready.
	ServiceReady bool `json:"serviceReady,omitempty"`

	// IngressReady indicates if the ingress is ready.
	IngressReady bool `json:"ingressReady,omitempty"`

	// GatewayReady indicates if the gateway configuration and sidecar are ready.
	GatewayReady bool `json:"gatewayReady,omitempty"`

	// PolicyReady indicates if policy data for the gateway has been generated.
	PolicyReady bool `json:"policyReady,omitempty"`

	// CanaryReady indicates if the canary deployment, when configured, is ready.
	CanaryReady bool `json:"canaryReady,omitempty"`

	// URL is the public MCP endpoint the operator derived from the ingress
	// host, TLS setting, and public path. The platform API, CLI, and UI show
	// this value instead of rebuilding it, so every surface agrees with what
	// the ingress and gateway actually serve. Empty when no host is known.
	URL string `json:"url,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Team",type="string",JSONPath=".spec.teamID"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Policy",type="boolean",JSONPath=".status.policyReady"
// +kubebuilder:printcolumn:name="Gateway",type="boolean",JSONPath=".status.gatewayReady"
// +kubebuilder:printcolumn:name="Ready",type="boolean",JSONPath=".status.deploymentReady"
// +kubebuilder:printcolumn:name="URL",type="string",JSONPath=".status.url",priority=1
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:webhook:path=/mutate-mcpruntime-org-v1alpha1-mcpserver,mutating=true,failurePolicy=ignore,sideEffects=None,groups=mcpruntime.org,resources=mcpservers,verbs=create;update,versions=v1alpha1,name=mmcpserver.kb.io,admissionReviewVersions=v1,serviceName=mcp-runtime-operator-webhook-service,serviceNamespace=mcp-runtime,servicePort=443
// +kubebuilder:webhook:path=/validate-mcpruntime-org-v1alpha1-mcpserver,mutating=false,failurePolicy=fail,sideEffects=None,groups=mcpruntime.org,resources=mcpservers,verbs=create;update,versions=v1alpha1,name=vmcpserver.kb.io,admissionReviewVersions=v1,serviceName=mcp-runtime-operator-webhook-service,serviceNamespace=mcp-runtime,servicePort=443

// MCPServer is the Schema for the mcpservers API.
type MCPServer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MCPServerSpec   `json:"spec,omitempty"`
	Status MCPServerStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MCPServerList contains a list of MCPServer.
type MCPServerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPServer `json:"items"`
}
