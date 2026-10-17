package access_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "mcp-runtime/api/v1alpha1"
	"mcp-runtime/pkg/access"
)

// These cases pin the deliberate difference between persisted CRD state and
// platform apply. Consolidation must not silently broaden apply or forbid
// historical/wildcard resources already admitted by the Kubernetes contract.
func TestGrantValidationBoundaryContracts(t *testing.T) {
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name                       string
		change                     func(*v1.MCPAccessGrant)
		admissionField, applyError string
		wildcard                   bool
	}{
		{name: "explicit team grant"},
		{name: "legacy wildcard", change: func(g *v1.MCPAccessGrant) { g.Spec.Subject = v1.SubjectRef{} }, applyError: "one of subject.humanID, subject.agentID, or subject.teamID is required", wildcard: true},
		{name: "empty effects deny all in persisted policy", change: func(g *v1.MCPAccessGrant) { g.Spec.AllowedSideEffects = nil }, applyError: "at least one allowed side effect is required"},
		{name: "duplicate side effects", change: func(g *v1.MCPAccessGrant) { g.Spec.AllowedSideEffects = []v1.ToolSideEffect{"read", "read"} }, admissionField: "spec.allowedSideEffects[1]", applyError: "allowedSideEffects[1] is a duplicate"},
		{name: "unsupported effect", change: func(g *v1.MCPAccessGrant) { g.Spec.AllowedSideEffects = []v1.ToolSideEffect{"erase"} }, admissionField: "spec.allowedSideEffects[0]", applyError: "allowedSideEffects[0] must be read, write, or destructive"},
		{name: "empty tool name", change: func(g *v1.MCPAccessGrant) { g.Spec.ToolRules[0].Name = " " }, admissionField: "spec.toolRules[0].name", applyError: "toolRules[0].name is required"},
		{name: "tool decision required", change: func(g *v1.MCPAccessGrant) { g.Spec.ToolRules[0].Decision = "" }, admissionField: "spec.toolRules[0].decision", applyError: "toolRules[0].decision must be allow or deny"},
		{name: "API forbids audit decision", change: func(g *v1.MCPAccessGrant) { g.Spec.ToolRules[0].Decision = "audit" }, applyError: "toolRules[0].decision must be allow or deny"},
		{name: "expired state remains admissible", change: func(g *v1.MCPAccessGrant) { g.Spec.ExpiresAt = &metav1.Time{Time: now.Add(-time.Hour)} }, applyError: "expiresAt must be in the future"},
		{name: "expiry exactly now is not a new grant", change: func(g *v1.MCPAccessGrant) { g.Spec.ExpiresAt = &metav1.Time{Time: now} }, applyError: "expiresAt must be in the future"},
		{name: "outer whitespace normalized only at API boundary", change: func(g *v1.MCPAccessGrant) { g.Spec.Subject.TeamID = " team-a " }, admissionField: "spec.subject.teamID"},
		{name: "internal team whitespace", change: func(g *v1.MCPAccessGrant) { g.Spec.Subject.TeamID = "team a" }, admissionField: "spec.subject.teamID", applyError: "subject.teamID must not contain whitespace"},
		{name: "stable identifier bound", change: func(g *v1.MCPAccessGrant) { g.Spec.Subject.TeamID = strings.Repeat("x", 129) }, admissionField: "spec.subject.teamID", applyError: "subject.teamID must be at most 128 characters"},
		{name: "unknown mode", change: func(g *v1.MCPAccessGrant) { g.Spec.Mode = "shadow" }, admissionField: "spec.mode", applyError: "mode must be enforce or monitor"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := &v1.MCPAccessGrant{ObjectMeta: metav1.ObjectMeta{Name: "grant-a", Namespace: "mcp-team-a"}, Spec: v1.MCPAccessGrantSpec{ServerRef: v1.ServerReference{Name: "server-a"}, Subject: v1.SubjectRef{TeamID: "team-a"}, AllowedSideEffects: []v1.ToolSideEffect{"read"}, ToolRules: []v1.ToolRule{{Name: "ping", Decision: "allow"}}}}
			if tc.change != nil {
				tc.change(g)
			}
			warnings, err := g.ValidateCreate()
			if tc.admissionField == "" {
				if err != nil {
					t.Fatalf("admission: %v", err)
				}
			} else {
				status, ok := err.(apierrors.APIStatus)
				if !ok {
					t.Fatalf("expected structured admission field error, got %v", err)
				}
				found := false
				for _, cause := range status.Status().Details.Causes {
					if cause.Field == tc.admissionField {
						found = true
					}
				}
				if !found {
					t.Fatalf("admission lost field %q: %v", tc.admissionField, err)
				}
			}
			if tc.wildcard && (len(warnings) != 1 || !strings.Contains(warnings[0], "wildcard grant")) {
				t.Fatalf("wildcard warning: %v", warnings)
			}
			req := access.GrantApplyRequest{Name: g.Name, Namespace: g.Namespace, ServerRef: access.ServerReference{Name: access.ServerName(g.Spec.ServerRef.Name)}, Subject: access.SubjectRef{HumanID: access.HumanID(g.Spec.Subject.HumanID), AgentID: access.AgentID(g.Spec.Subject.AgentID), TeamID: access.TeamID(g.Spec.Subject.TeamID)}, ExpiresAt: g.Spec.ExpiresAt}
			if g.Spec.Mode != "" {
				mode := access.GrantMode(g.Spec.Mode)
				req.Mode = &mode
			}
			for _, effect := range g.Spec.AllowedSideEffects {
				req.AllowedSideEffects = append(req.AllowedSideEffects, access.ToolSideEffect(effect))
			}
			for _, rule := range g.Spec.ToolRules {
				req.ToolRules = append(req.ToolRules, access.ToolRule{Name: rule.Name, Decision: access.PolicyDecision(rule.Decision), RequiredTrust: access.TrustLevel(rule.RequiredTrust)})
			}
			err = access.ValidateGrantApplyRequest(&req, now)
			if tc.applyError == "" {
				if err != nil {
					t.Fatalf("apply: %v", err)
				}
			} else if err == nil || err.Error() != tc.applyError {
				t.Fatalf("apply error=%v,want %q", err, tc.applyError)
			}
		})
	}
}

func TestApplyValidationPreservesStateAndSessionImportContract(t *testing.T) {
	no := false
	expired := metav1.NewTime(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
	req := access.SessionApplyRequest{Name: " session-a ", ServerRef: access.ServerReference{Name: " server-a "}, Subject: access.SubjectRef{HumanID: " user-a "}, ConsentedTrust: " high ", ExpiresAt: &expired, Revoked: &no}
	if err := access.ValidateSessionApplyRequest(&req); err != nil {
		t.Fatal(err)
	}
	if req.Name != "session-a" || req.ServerRef.Name != "server-a" || req.Subject.HumanID != "user-a" || req.ConsentedTrust != "high" || req.Namespace != access.DefaultMCPResourceNamespace || req.PolicyVersion != "v1" {
		t.Fatalf("normalization changed: %#v", req)
	}
	if req.ExpiresAt != &expired || req.Revoked != &no {
		t.Fatal("validation replaced explicit persisted state")
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err = json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if value, present := body["revoked"]; !present || value != false {
		t.Fatalf("explicit false lost: %s", raw)
	}
	req.Revoked = nil
	raw, err = json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "revoked") {
		t.Fatalf("omitted state field became explicit: %s", raw)
	}
	mode := access.GrantMode("")
	grant := access.GrantApplyRequest{Mode: &mode}
	if err = access.ValidateGrantApplyRequest(&grant, time.Now()); err == nil || err.Error() != "mode must be enforce or monitor" {
		t.Fatalf("explicit empty mode must fail before other fields: %v", err)
	}
}
