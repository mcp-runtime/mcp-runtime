package policy

import (
	"testing"
	"time"
)

func delegatedDoc(rules ...DelegatedToolRule) *Document {
	doc := &Document{
		Server: Server{Name: "pilot", Namespace: "mcp-servers"},
		Auth: &Auth{
			Mode:               "header",
			Headers:            []string{"X-Example-Credential", "Private-Token"},
			CredentialPresence: "any",
		},
		Policy: &Config{
			Mode:               "allow-list",
			DefaultDecision:    "deny",
			DelegatedToolRules: rules,
			MaxSideEffect:      "read",
		},
		Tools: []Tool{{Name: "list_projects", SideEffect: "read"}, {Name: "delete_project", SideEffect: "destructive"}},
	}
	if err := Stamp(doc, ""); err != nil {
		panic(err)
	}
	return doc
}

func TestAuthorizeDelegatedAllowsListedReadAndDeniesOthers(t *testing.T) {
	doc := delegatedDoc(DelegatedToolRule{Name: "list_projects", Decision: "allow"}, DelegatedToolRule{Name: "delete_project", Decision: "deny"})
	if err := Validate(doc); err != nil {
		t.Fatal(err)
	}
	allowed := Authorize(doc, Request{RPCMethod: "tools/call", ToolName: "list_projects"}, time.Time{})
	if !allowed.Allowed || allowed.Reason != "delegated_tool_allowed" {
		t.Fatalf("allowed = %#v", allowed)
	}
	denied := Authorize(doc, Request{RPCMethod: "tools/call", ToolName: "delete_project"}, time.Time{})
	if denied.Allowed || denied.Reason != "tool_denied" {
		t.Fatalf("denied = %#v", denied)
	}
	unknown := Authorize(doc, Request{RPCMethod: "tools/call", ToolName: "other"}, time.Time{})
	if unknown.Allowed || unknown.Reason != "tool_side_effect_unknown" {
		t.Fatalf("unknown = %#v", unknown)
	}
	lifecycle := Authorize(doc, Request{RPCMethod: "initialize"}, time.Time{})
	if !lifecycle.Allowed || lifecycle.Reason != "delegated_lifecycle" {
		t.Fatalf("lifecycle = %#v", lifecycle)
	}
}

func TestAuthorizeDelegatedRejectsIdentityPolicy(t *testing.T) {
	doc := delegatedDoc(DelegatedToolRule{Name: "list_projects", Decision: "allow"})
	doc.Grants = []Grant{{Name: "g", HumanID: "user"}}
	_ = Stamp(doc, "")
	decision := Authorize(doc, Request{RPCMethod: "tools/call", ToolName: "list_projects"}, time.Time{})
	if decision.Allowed || decision.Reason != "delegated_identity_policy_unsupported" {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestValidateHeaderAuthRejectsOAuthFieldsAndObserve(t *testing.T) {
	doc := delegatedDoc()
	doc.Auth.IssuerURL = "https://issuer.example.com"
	_ = Stamp(doc, "")
	if err := Validate(doc); err == nil {
		t.Fatal("expected oauth fields to be rejected")
	}
	doc = delegatedDoc()
	doc.Policy.Mode = "observe"
	_ = Stamp(doc, "")
	if err := Validate(doc); err == nil {
		t.Fatal("expected observe mode to be rejected")
	}
	doc = delegatedDoc()
	doc.SchemaVersion = SchemaVersion
	if err := Validate(doc); err == nil {
		t.Fatal("expected v1 header document to be rejected")
	}
}
