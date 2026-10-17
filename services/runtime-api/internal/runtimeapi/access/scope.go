package access

import (
	"strings"

	mcpaccess "mcp-runtime/pkg/access"
	"mcp-runtime/pkg/access/specvalidation"
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

func ValidTrust(trust mcpaccess.TrustLevel) bool { return specvalidation.ValidTrust(string(trust)) }

func ValidSideEffect(sideEffect mcpaccess.ToolSideEffect) bool {
	return specvalidation.ValidSideEffect(string(sideEffect))
}

func ValidDecision(decision mcpaccess.PolicyDecision) bool {
	return specvalidation.ValidDecision(string(decision))
}
