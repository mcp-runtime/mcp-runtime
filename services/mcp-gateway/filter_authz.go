package main

import (
	"net/http"
	"time"

	policypkg "mcp-runtime/pkg/policy"
)

// authzFilter is stage 4 of the gateway pipeline. It evaluates the policy
// decision for tool calls and indeterminate RPC attempts.
//
// Non-tool-call requests (e.g. tools/list, resources/list, ping, GET passthrough)
// are not subject to grant/session policy evaluation and always Continue.
//
// For tool calls, the authorization inputs are Exchange.Policy and
// Exchange.Identity. Both were set by earlier stages and must not be mutated
// after this filter sets Exchange.Decision.
//
// On any denial, authzFilter writes the denial response and returns Reject.
func (s *gatewayServer) authzFilter(ex *Exchange) Result {
	if !ex.Inspection.ToolCall && !ex.Inspection.Indeterminate {
		// Non-tool requests (tools/list, resources/*, ping, GET passthrough) are
		// intentionally not subject to grant/session policy. Record an explicit
		// allow so the deny default set in newExchange unambiguously means
		// "never evaluated"; without this the passthrough would rely on the
		// default decision and any future change to that default would silently
		// flip these requests.
		ex.Decision = policypkg.Allow(
			"allowed",
			policypkg.ChoosePolicyVersion(policypkg.PolicyVersion(ex.Policy), s.defaultPolicyVersion),
		)
		return Continue
	}

	switch {
	case ex.Policy != nil && ex.Policy.Session != nil && ex.Policy.Session.Store == "platform" &&
		ex.Policy.Policy != nil && ex.Policy.Policy.Mode != "observe" && !s.sessionSync.fresh():
		ex.Decision = policypkg.Deny(http.StatusServiceUnavailable, "policy_unavailable", policypkg.PolicyVersion(ex.Policy))
	case ex.PolicyErr != nil:
		ex.Decision = policypkg.Deny(
			http.StatusServiceUnavailable,
			"policy_unavailable",
			policypkg.ChoosePolicyVersion(policypkg.PolicyVersion(ex.Policy), s.defaultPolicyVersion),
		)
	case ex.Inspection.Indeterminate:
		ex.Decision = policypkg.Deny(
			http.StatusForbidden,
			policypkg.FirstNonEmpty(ex.Inspection.FailureReason, "rpc_inspection_failed"),
			policypkg.ChoosePolicyVersion(policypkg.PolicyVersion(ex.Policy), s.defaultPolicyVersion),
		)
	default:
		ex.Decision = policypkg.Authorize(ex.Policy, policypkg.Request{
			Identity:  policyIdentity(ex.Identity),
			RPCMethod: ex.Inspection.Method,
			ToolName:  policypkg.ToolName(ex.Inspection.ToolName),
			Session:   ex.Identity.Session,
		}, time.Now())
	}

	if !ex.Decision.Allowed {
		s.writeDeniedResponse(ex)
		return Reject
	}
	return Continue
}
