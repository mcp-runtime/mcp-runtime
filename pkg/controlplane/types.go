package controlplane

import mcpv1alpha1 "mcp-runtime/api/v1alpha1"

// ServerInfo is the control-plane projection of an MCPServer and its backing
// workload status.
type ServerInfo struct {
	Name           string                      `json:"name"`
	Namespace      string                      `json:"namespace"`
	UID            string                      `json:"uid,omitempty"`
	TeamID         string                      `json:"team_id,omitempty"`
	Image          string                      `json:"image,omitempty"`
	ImageTag       string                      `json:"imageTag,omitempty"`
	Description    string                      `json:"description,omitempty"`
	Ready          string                      `json:"ready"`
	Status         string                      `json:"status"`
	Labels         map[string]string           `json:"labels,omitempty"`
	Age            string                      `json:"age"`
	Endpoint       string                      `json:"endpoint,omitempty"`
	AuthMode       mcpv1alpha1.AuthMode        `json:"authMode,omitempty"`
	GatewayEnabled bool                        `json:"-"`
	HumanIDHeader  string                      `json:"-"`
	AgentIDHeader  string                      `json:"-"`
	ServicePort    int32                       `json:"servicePort,omitempty"`
	Generation     int64                       `json:"generation,omitempty"`
	Tools          []mcpv1alpha1.ToolConfig    `json:"tools,omitempty"`
	Prompts        []mcpv1alpha1.InventoryItem `json:"prompts"`
	Resources      []mcpv1alpha1.InventoryItem `json:"resources"`
	Tasks          []mcpv1alpha1.InventoryItem `json:"tasks"`
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
