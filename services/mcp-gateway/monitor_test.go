package main

import (
	"net/http"
	"testing"

	p "mcp-runtime/pkg/policy"
)

func TestAuthzMonitorMixedGrantPrecedenceAndAudit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enforce *p.Grant
		want    Result
		shadow  bool
	}{
		{name: "monitor only", want: Continue, shadow: true},
		{name: "enforcing trust denial", enforce: &p.Grant{Name: "enforce", HumanID: "human-1", MaxTrust: "low", AllowedSideEffects: []string{"write"}}, want: Reject},
		{name: "enforcing side effect denial", enforce: &p.Grant{Name: "enforce", HumanID: "human-1", MaxTrust: "high", AllowedSideEffects: []string{"read"}}, want: Reject},
		{name: "enforcing allow with monitor observation", enforce: &p.Grant{Name: "enforce", HumanID: "human-1", MaxTrust: "high", AllowedSideEffects: []string{"write"}}, want: Continue, shadow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := minimalServer()
			ex := newTestExchange(http.MethodPost, "/mcp", `{"method":"tools/call","params":{"name":"echo"}}`, map[string]string{"Content-Type": "application/json"})
			s.inspectFilter(ex)
			ex.Identity = identityContext{HumanID: "human-1"}
			monitor := p.Grant{Name: "monitor", Mode: "monitor", HumanID: "human-1", MaxTrust: "high", AllowedSideEffects: []string{"write"}, ToolRules: []p.ToolAccess{{Name: "echo", Decision: "deny"}}}
			// Use a permissive monitor for the enforcing-deny cases: it must not
			// supply the permissions missing from the enforcing grant.
			if !tc.shadow {
				monitor.ToolRules = nil
			}
			ex.Policy = &p.Document{Policy: &p.Config{Mode: "allow-list", DefaultDecision: "deny"}, Tools: []p.Tool{{Name: "echo", RequiredTrust: "high", SideEffect: "write"}}, Grants: []p.Grant{monitor}}
			if tc.enforce != nil {
				ex.Policy.Grants = append(ex.Policy.Grants, *tc.enforce)
			}
			if got := s.authzFilter(ex); got != tc.want || ex.Decision.WouldDeny != tc.shadow {
				t.Fatalf("result=%v decision=%#v", got, ex.Decision)
			}
			payload := s.auditPayload(ex.R, "/mcp", "tools/call", "echo", ex.Identity, ex.Policy, ex.Decision, ex.W.status, 0, 0)
			if tc.shadow && (payload["would_deny"] != true || payload["would_deny_reason"] != "tool_denied") {
				t.Fatalf("missing monitor audit: %#v", payload)
			}
			if !tc.shadow && payload["would_deny"] != nil {
				t.Fatalf("enforced denial mislabeled: %#v", payload)
			}
		})
	}
}
