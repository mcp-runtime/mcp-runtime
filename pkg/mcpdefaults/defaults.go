// Package mcpdefaults defines platform defaults shared across the API, CLI,
// operator, and runtime services.
package mcpdefaults

const (
	MCPServerPort       = 8088
	MCPGatewayPort      = 8091
	MCPServersNamespace = "mcp-servers"

	AuthTokenHeader = "Authorization"

	// Enum values. Types that mirror these enums reference the value
	// constants below, never the defaults, so changing a default can never
	// change what "deny" or "allow-list" means.
	PolicyModeAllowList = "allow-list"
	PolicyDecisionDeny  = "deny"
	PolicyDecisionAllow = "allow"

	// Defaults, expressed in terms of the enum values.
	PolicyMode      = PolicyModeAllowList
	PolicyDecision  = PolicyDecisionDeny
	PolicyEnforceOn = "call_tool"
	PolicyVersion   = "v1"

	SessionStore    = "kubernetes"
	SessionMaxLife  = "24h"
	SessionIdleTime = "1h"
	SessionUpstream = AuthTokenHeader
)

// GatewayPolicyConfigMapName returns the ConfigMap name used by a server's gateway.
func GatewayPolicyConfigMapName(serverName string) string {
	return serverName + "-gateway-policy"
}

// DefaultIngressPath returns the default public MCP path for a server name.
func DefaultIngressPath(serverName string) string {
	return "/" + serverName + "/mcp"
}
