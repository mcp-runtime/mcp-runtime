// Package mcpdefaults defines platform defaults shared across the API, CLI,
// operator, and runtime services.
package mcpdefaults

const (
	MCPServerPort         = 8088
	MCPGatewayPort        = 8091
	MCPServersNamespace   = "mcp-servers"
	LogCollectorNamespace = "mcp-log-collector"

	AuthTokenHeader = "Authorization"

	// Enum values. Types that mirror these enums reference the value
	// constants below, never the defaults, so changing a default can never
	// change what "deny" or "allow-list" means.
	PolicyModeAllowList = "allow-list"
	PolicyModeObserve   = "observe"
	PolicyDecisionDeny  = "deny"
	PolicyDecisionAllow = "allow"

	// Defaults for an explicitly configured policy block (server init / grants).
	PolicyMode      = PolicyModeAllowList
	PolicyDecision  = PolicyDecisionDeny
	PolicyEnforceOn = "call_tool"
	PolicyVersion   = "v1"

	// Defaults when the gateway is on but spec.policy is omitted: observe tool
	// calls so metrics/analytics work without adapter grants.
	ObservabilityPolicyMode     = PolicyModeObserve
	ObservabilityPolicyDecision = PolicyDecisionAllow

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
