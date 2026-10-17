package policy

import (
	"testing"
	"time"
)

func TestMonitorCannotSupplyEnforcingPermissions(t *testing.T) {
	for _, reason := range []string{"trust_too_low", "side_effect_not_allowed", "tool_not_granted", "tool_denied"} {
		for _, monitorFirst := range []bool{false, true} {
			t.Run(reason+map[bool]string{false: "/enforce-first", true: "/monitor-first"}[monitorFirst], func(t *testing.T) {
				doc := testPolicyWithGrant()
				enforce := doc.Grants[0]
				enforce.Name = "a-enforce"
				monitor := enforce
				monitor.Mode = "monitor"
				monitor.Name = "z-monitor"
				if monitorFirst {
					enforce.Name, monitor.Name = "z-enforce", "a-monitor"
				}
				switch reason {
				case "trust_too_low":
					enforce.MaxTrust = "low"
				case "side_effect_not_allowed":
					enforce.AllowedSideEffects = []string{"write"}
				case "tool_not_granted":
					enforce.ToolRules = []ToolAccess{{Name: "other", Decision: "allow"}}
				case "tool_denied":
					enforce.ToolRules = []ToolAccess{{Name: "upper", Decision: "deny"}}
				}
				doc.Grants = []Grant{enforce, monitor}
				decision := Authorize(doc, Request{Identity: Identity{HumanID: "human-1", AgentID: "agent-1"}, RPCMethod: "tools/call", ToolName: "upper"}, time.Time{})
				if decision.Allowed || decision.WouldDeny || decision.Reason != reason {
					t.Fatalf("decision = %#v, want enforcing %s", decision, reason)
				}
			})
		}
	}
}

func TestMonitorObservesRestrictionsIndependentlyOfEnforcingAllow(t *testing.T) {
	for _, reason := range []string{"trust_too_low", "side_effect_not_allowed", "tool_not_granted", "tool_denied"} {
		t.Run(reason, func(t *testing.T) {
			doc := testPolicyWithGrant()
			monitor := doc.Grants[0]
			monitor.Name, monitor.Mode = "monitor", "monitor"
			switch reason {
			case "trust_too_low":
				monitor.MaxTrust = "low"
			case "side_effect_not_allowed":
				monitor.AllowedSideEffects = []string{"write"}
			case "tool_not_granted":
				monitor.ToolRules = []ToolAccess{{Name: "other", Decision: "allow"}}
			case "tool_denied":
				monitor.ToolRules = []ToolAccess{{Name: "upper", Decision: "deny"}}
			}
			doc.Grants = append(doc.Grants, monitor)
			decision := Authorize(doc, Request{Identity: Identity{HumanID: "human-1", AgentID: "agent-1"}, RPCMethod: "tools/call", ToolName: "upper"}, time.Time{})
			if !decision.Allowed || !decision.WouldDeny || decision.WouldDenyReason != reason || decision.MatchedGrant != "grant-1" {
				t.Fatalf("decision = %#v, want enforcing allow and monitor %s", decision, reason)
			}
		})
	}
}

func TestMonitorPreservesFailClosedGates(t *testing.T) {
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		modify func(*Document, *Request)
		want   string
	}{
		{"missing identity", func(d *Document, r *Request) { r.Identity = Identity{} }, "missing_identity"},
		{"wrong team", func(d *Document, r *Request) { d.Grants[0].TeamID = "other" }, "no_matching_grant"},
		{"disabled", func(d *Document, r *Request) { d.Grants[0].Disabled = true }, "tool_not_granted"},
		{"expired", func(d *Document, r *Request) { d.Grants[0].ExpiresAt = now.Add(-time.Minute).Format(time.RFC3339) }, "grant_expired"},
		{"missing session", func(d *Document, r *Request) { d.Session.Required = true }, "missing_session"},
		{"session not found", func(d *Document, r *Request) { d.Session.Required = true; r.Identity.SessionID = "unknown" }, "session_not_found"},
		{"revoked session", func(d *Document, r *Request) {
			d.Session.Required = true
			r.Identity.SessionID = "s"
			d.Sessions = []Binding{{Name: "s", HumanID: "human-1", Revoked: true}}
		}, "session_revoked"},
		{"expired session", func(d *Document, r *Request) {
			d.Session.Required = true
			r.Identity.SessionID = "s"
			d.Sessions = []Binding{{Name: "s", HumanID: "human-1", ExpiresAt: now.Add(-time.Minute).Format(time.RFC3339)}}
		}, "session_expired"},
		{"unknown metadata", func(d *Document, r *Request) { d.Tools[0].SideEffect = "" }, "tool_side_effect_unknown"},
		{"unknown metadata with tool deny", func(d *Document, r *Request) {
			d.Tools[0].SideEffect = ""
			d.Grants[0].ToolRules = []ToolAccess{{Name: "upper", Decision: "deny"}}
		}, "tool_denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := testPolicyWithGrant()
			doc.Grants[0].Mode = "monitor"
			req := Request{Identity: Identity{HumanID: "human-1", AgentID: "agent-1"}, RPCMethod: "tools/call", ToolName: "upper"}
			tc.modify(doc, &req)
			decision := Authorize(doc, req, now)
			if decision.Allowed || decision.WouldDeny || decision.Reason != tc.want {
				t.Fatalf("decision = %#v, want fail-closed %s", decision, tc.want)
			}
		})
	}
}
