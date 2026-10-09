package policy

import (
	"net/http"
	"strings"
)

// authorizeDelegated enforces server and tool rules without a caller identity.
// Grants, sessions, and observation are rejected. A missing tool side effect
// fails closed. Credential authentication stays with the upstream server.
func authorizeDelegated(policy *Document, request Request) Decision {
	version := policyVersionOrDefault(policy, "")
	if policy == nil || policy.Policy == nil || policyModeObserve(policy) || !strings.EqualFold(policy.Policy.Mode, "allow-list") || !strings.EqualFold(policy.Policy.DefaultDecision, "deny") {
		return Deny(http.StatusForbidden, "delegated_policy_unsupported", version)
	}
	if policy.Session != nil || len(policy.Grants) > 0 || len(policy.Sessions) > 0 {
		return Deny(http.StatusForbidden, "delegated_identity_policy_unsupported", version)
	}
	if !IsToolCallMethod(request.RPCMethod) {
		return Allow("delegated_lifecycle", version)
	}
	requiredTrust, requiredSideEffect, riskLevel := resolveToolMetadata(policyTools(policy), request.ToolName)
	for _, rule := range policy.Policy.DelegatedToolRules {
		if !strings.EqualFold(string(rule.Name), string(request.ToolName)) {
			continue
		}
		if strings.EqualFold(rule.Decision, "deny") {
			return delegatedDeny(http.StatusForbidden, "tool_denied", version, requiredTrust, requiredSideEffect, riskLevel)
		}
	}
	if NormalizeSideEffect(requiredSideEffect) == "" {
		return delegatedDeny(http.StatusForbidden, "tool_side_effect_unknown", version, requiredTrust, requiredSideEffect, riskLevel)
	}
	if max := NormalizeSideEffect(policy.Policy.MaxSideEffect); max != "" && sideEffectRank(requiredSideEffect) > sideEffectRank(max) {
		return delegatedDeny(http.StatusForbidden, "side_effect_not_allowed", version, requiredTrust, requiredSideEffect, riskLevel)
	}
	for _, rule := range policy.Policy.DelegatedToolRules {
		if strings.EqualFold(string(rule.Name), string(request.ToolName)) && strings.EqualFold(rule.Decision, "allow") {
			allowed := Allow("delegated_tool_allowed", version)
			allowed.RequiredTrust = requiredTrust
			allowed.RequiredSideEffect = requiredSideEffect
			allowed.RiskLevel = riskLevel
			return allowed
		}
	}
	return delegatedDeny(http.StatusForbidden, "tool_not_allowed", version, requiredTrust, requiredSideEffect, riskLevel)
}

func delegatedDeny(status int, reason, version, requiredTrust, requiredSideEffect, riskLevel string) Decision {
	denied := Deny(status, reason, version)
	denied.RequiredTrust = requiredTrust
	denied.RequiredSideEffect = requiredSideEffect
	denied.RiskLevel = riskLevel
	return denied
}

func sideEffectRank(value string) int {
	switch NormalizeSideEffect(value) {
	case SideEffectRead:
		return 1
	case SideEffectWrite:
		return 2
	case SideEffectDestructive:
		return 3
	default:
		return 0
	}
}
