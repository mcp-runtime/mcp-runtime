// Package policy provides helper functions for working with policy documents.
package policy

import (
	"strings"
)

// IsToolCallMethod returns true if the method is a tool invocation method.
func IsToolCallMethod(method string) bool {
	switch method {
	case "tools/call", "call_tool":
		return true
	default:
		return false
	}
}

// FirstNonEmpty returns the first non-empty string from the provided values.
func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// PolicyServerName returns the server name from a policy document.
func PolicyServerName(policy *Document) string {
	if policy == nil {
		return ""
	}
	return string(policy.Server.Name)
}

// PolicyServerNamespace returns the server namespace from a policy document.
func PolicyServerNamespace(policy *Document) string {
	if policy == nil {
		return ""
	}
	return string(policy.Server.Namespace)
}

// PolicyServerTeamID returns the owning team ID from a policy document.
func PolicyServerTeamID(policy *Document) string {
	if policy == nil {
		return ""
	}
	return string(policy.Server.TeamID)
}

// PolicyServerCluster returns the cluster name from a policy document.
func PolicyServerCluster(policy *Document) string {
	if policy == nil {
		return ""
	}
	return policy.Server.Cluster
}

// PolicyVersion returns the policy version from a policy document.
func PolicyVersion(policy *Document) string {
	if policy == nil || policy.Policy == nil {
		return ""
	}
	return policy.Policy.PolicyVersion
}

// PolicyUsesOAuth returns true if the policy uses OAuth authentication.
func PolicyUsesOAuth(policy *Document) bool {
	return policy != nil && policy.Auth != nil &&
		(strings.TrimSpace(policy.Auth.IssuerURL) != "" || strings.TrimSpace(policy.Auth.Audience) != "")
}

// ChoosePolicyVersion returns the first non-empty policy version from the provided values,
// or empty string if none found. Callers should pass their own default as the last value.
func ChoosePolicyVersion(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
