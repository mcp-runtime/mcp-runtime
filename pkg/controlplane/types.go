package controlplane

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
)

// ServerInfo is the control-plane projection of an MCPServer and its backing
// workload status.
type ServerInfo struct {
	Name        string             `json:"name"`
	Namespace   string             `json:"namespace"`
	UID         string             `json:"uid,omitempty"`
	TeamID      string             `json:"team_id,omitempty"`
	Image       string             `json:"image,omitempty"`
	ImageTag    string             `json:"imageTag,omitempty"`
	Description string             `json:"description,omitempty"`
	Ready       string             `json:"ready"`
	Status      string             `json:"status"`
	Message     string             `json:"message,omitempty"`
	Conditions  []metav1.Condition `json:"conditions,omitempty"`
	Labels      map[string]string  `json:"labels,omitempty"`
	Age         string             `json:"age"`
	Endpoint    string             `json:"endpoint,omitempty"`
	// AuthMode is "header" when the server delegates credential checks to
	// upstream headers, or "oauth" when OAuth support is configured.
	// Empty means authentication support is not reported.
	AuthMode string `json:"authMode,omitempty"`
	// AuthHeaders are credential header names for header auth. Values are
	// never copied onto this summary.
	AuthHeaders []string `json:"authHeaders,omitempty"`
	// CredentialPresence is "any" or "all" for header auth.
	CredentialPresence string                      `json:"credentialPresence,omitempty"`
	GatewayEnabled     bool                        `json:"gatewayEnabled"`
	ServicePort        int32                       `json:"servicePort,omitempty"`
	Generation         int64                       `json:"generation,omitempty"`
	Tools              []mcpv1alpha1.ToolConfig    `json:"tools,omitempty"`
	Prompts            []mcpv1alpha1.InventoryItem `json:"prompts"`
	Resources          []mcpv1alpha1.InventoryItem `json:"resources"`
	Tasks              []mcpv1alpha1.InventoryItem `json:"tasks"`
	RuntimeStatus      ServerRuntimeStatus         `json:"runtimeStatus"`
}

// ServerRuntimeStatus combines operator-owned rollout state with observations
// available from the runtime API. Session details are populated only for
// callers allowed to inspect server activity.
type ServerRuntimeStatus struct {
	Transport        string                           `json:"transport,omitempty"`
	ConnectionState  string                           `json:"connectionState,omitempty"`
	AuthSource       string                           `json:"authSource,omitempty"`
	LastError        *mcpv1alpha1.MCPServerErrorEvent `json:"lastError,omitempty"`
	LastProbeTime    string                           `json:"lastProbeTime,omitempty"`
	LastSession      *ServerLastSession               `json:"lastSession,omitempty"`
	LastSessionState string                           `json:"lastSessionState,omitempty"`
}

// ServerLastSession identifies the latest observed gateway call using an MCP
// session. It deliberately omits caller identity and request payloads.
type ServerLastSession struct {
	ID        string `json:"id"`
	Namespace string `json:"namespace,omitempty"`
	UsedAt    string `json:"usedAt"`
	ToolName  string `json:"toolName,omitempty"`
}

// ServerDeploymentStatus summarizes readiness for the Deployment that backs an
// MCPServer.
type ServerDeploymentStatus struct {
	Ready  string
	Status string
}

// ListServersResult contains MCPServer summaries and metadata about fallback
// behavior used while reading them.
type ListServersResult struct {
	Servers                []ServerInfo
	CRDError               error
	UsedDeploymentFallback bool
}
