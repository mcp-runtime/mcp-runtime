package access

import (
	"strings"

	mcpaccess "mcp-runtime/pkg/access"
	"mcp-runtime/pkg/mcpdefaults"
)

const DefaultPolicyVersionValue = mcpdefaults.PolicyVersion

func DefaultAccessNamespace(namespace string) string {
	if namespace = strings.TrimSpace(namespace); namespace != "" {
		return namespace
	}
	return mcpaccess.DefaultMCPResourceNamespace
}

func DefaultPolicyVersion(policyVersion string) string {
	if policyVersion = strings.TrimSpace(policyVersion); policyVersion != "" {
		return policyVersion
	}
	return DefaultPolicyVersionValue
}

func NormalizeTrust(trust mcpaccess.TrustLevel) mcpaccess.TrustLevel {
	return mcpaccess.TrustLevel(strings.TrimSpace(string(trust)))
}

func NormalizeSideEffect(sideEffect mcpaccess.ToolSideEffect) mcpaccess.ToolSideEffect {
	return mcpaccess.ToolSideEffect(strings.TrimSpace(string(sideEffect)))
}

func ValidTrust(trust mcpaccess.TrustLevel) bool {
	switch trust {
	case "low", "medium", "high":
		return true
	default:
		return false
	}
}

func ValidSideEffect(sideEffect mcpaccess.ToolSideEffect) bool {
	switch sideEffect {
	case "read", "write", "destructive":
		return true
	default:
		return false
	}
}

func ValidDecision(decision mcpaccess.PolicyDecision) bool {
	switch decision {
	case "allow", "deny":
		return true
	default:
		return false
	}
}
