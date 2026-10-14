package policy

import (
	"testing"
	"time"
)

func TestExternalSessionPreservesTrustAndAttribution(t *testing.T) {
	binding := Binding{Name: "session", Namespace: "team", HumanID: "human", AgentID: "agent", TeamID: "team-id", ConsentedTrust: "low"}
	doc := &Document{Server: Server{Name: "server", Namespace: "team", UID: "uid"}, Session: &Session{Required: true, Store: "platform"}, Policy: &Config{Mode: "allow-list", DefaultDecision: "deny"}, Tools: []Tool{{Name: "read", RequiredTrust: "medium", SideEffect: "read"}}, Grants: []Grant{{Name: "grant", Namespace: "team", HumanID: "human", AgentID: "agent", TeamID: "team-id", MaxTrust: "high", AllowedSideEffects: []string{"read"}, ToolRules: []ToolAccess{{Name: "read", Decision: "allow"}}}}}
	req := Request{Identity: Identity{HumanID: "human", AgentID: "agent", TeamID: "team-id", SessionID: "session"}, RPCMethod: "tools/call", ToolName: "read", Session: &binding}
	decision := Authorize(doc, req, time.Now())
	if decision.Allowed || decision.Reason != "trust_too_low" || decision.MatchedSession != "session" || decision.MatchedGrant != "grant" {
		t.Fatalf("decision=%+v", decision)
	}
	binding.ConsentedTrust = "high"
	if decision := Authorize(doc, req, time.Now()); !decision.Allowed {
		t.Fatalf("decision=%+v", decision)
	}
	req.Session = nil
	// Embedded state cannot silently override the authoritative platform source.
	doc.Sessions = []Binding{binding}
	if decision := Authorize(doc, req, time.Now()); decision.Allowed || decision.Reason != "session_not_found" {
		t.Fatalf("decision=%+v", decision)
	}
}
