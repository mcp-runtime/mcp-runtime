// Package policy provides contract tests ensuring compatibility between
// operator-rendered policy and proxy-consumed policy.
package policy

import (
	"encoding/json"
	"testing"
	"time"
)

// TestPolicyDocumentRoundTrip verifies that a policy document can be serialized
// and deserialized correctly, ensuring the contract between operator and proxy.
func TestPolicyDocumentRoundTrip(t *testing.T) {
	// Create a representative policy document as the operator would render it
	original := &Document{
		Server: Server{
			Name:      "test-server",
			Namespace: "mcp-servers",
			TeamID:    "team-acme",
			Cluster:   "test-cluster",
		},
		Auth: &Auth{
			TokenHeader: "Authorization",
			IssuerURL:   "https://auth.example.com",
			Audience:    "mcp-runtime",
			Scopes:      []string{"tools:read", "tools:write"},
		},
		Policy: &Config{
			Mode:            "allow-list",
			DefaultDecision: "deny",
			EnforceOn:       "call_tool",
			PolicyVersion:   "v1",
		},
		Session: &Session{
			Required:            true,
			Store:               "kubernetes",
			MaxLifetime:         "24h",
			IdleTimeout:         "1h",
			UpstreamTokenHeader: "X-Upstream-Token",
		},
		Tools: []Tool{
			{
				Name:          "read-file",
				Description:   "Read a file from the filesystem",
				RequiredTrust: "low",
				SideEffect:    "read",
				Labels: map[string]string{
					"category": "filesystem",
				},
			},
			{
				Name:          "write-file",
				Description:   "Write a file to the filesystem",
				RequiredTrust: "high",
				SideEffect:    "write",
				Labels: map[string]string{
					"category": "filesystem",
					"risk":     "destructive",
				},
			},
		},
		Grants: []Grant{
			{
				Name:          "developer-grant",
				HumanID:       "user@example.com",
				TeamID:        "team-acme",
				MaxTrust:      "high",
				PolicyVersion: "v1",
				Disabled:      false,
				AllowedSideEffects: []string{
					"read",
					"write",
				},
				ToolRules: []ToolAccess{
					{
						Name:          "read-file",
						Decision:      "allow",
						RequiredTrust: "low",
					},
					{
						Name:          "write-file",
						Decision:      "allow",
						RequiredTrust: "high",
					},
				},
			},
			{
				Name:          "agent-grant",
				AgentID:       "agent-123",
				MaxTrust:      "medium",
				PolicyVersion: "v1",
				Disabled:      false,
				AllowedSideEffects: []string{
					"read",
				},
				ToolRules: []ToolAccess{}, // No tool rules = all read tools allowed
			},
		},
		Sessions: []Binding{
			{
				Name:             "session-1",
				HumanID:          "user@example.com",
				AgentID:          "agent-123",
				TeamID:           "team-acme",
				ConsentedTrust:   "high",
				Revoked:          false,
				ExpiresAt:        time.Now().Add(24 * time.Hour).Format(time.RFC3339),
				PolicyVersion:    "v1",
				UpstreamTokenRef: "upstream-token/key",
			},
		},
	}

	// Serialize to JSON (as the operator would write to a ConfigMap)
	data, err := json.MarshalIndent(original, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal policy document: %v", err)
	}

	// Deserialize (as the proxy would read from the ConfigMap)
	var decoded Document
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal policy document: %v", err)
	}

	// Verify all fields are preserved
	verifyServer(t, original.Server, decoded.Server)
	verifyAuth(t, original.Auth, decoded.Auth)
	verifyPolicy(t, original.Policy, decoded.Policy)
	verifySession(t, original.Session, decoded.Session)
	verifyTools(t, original.Tools, decoded.Tools)
	verifyGrants(t, original.Grants, decoded.Grants)
	verifySessions(t, original.Sessions, decoded.Sessions)
}

func verifyServer(t *testing.T, expected, actual Server) {
	if expected.Name != actual.Name {
		t.Errorf("Server.Name mismatch: expected %q, got %q", expected.Name, actual.Name)
	}
	if expected.Namespace != actual.Namespace {
		t.Errorf("Server.Namespace mismatch: expected %q, got %q", expected.Namespace, actual.Namespace)
	}
	if expected.TeamID != actual.TeamID {
		t.Errorf("Server.TeamID mismatch: expected %q, got %q", expected.TeamID, actual.TeamID)
	}
	if expected.Cluster != actual.Cluster {
		t.Errorf("Server.Cluster mismatch: expected %q, got %q", expected.Cluster, actual.Cluster)
	}
}

func verifyAuth(t *testing.T, expected, actual *Auth) {
	if expected == nil && actual == nil {
		return
	}
	if expected == nil || actual == nil {
		t.Fatalf("Auth nil mismatch: expected %v, got %v", expected == nil, actual == nil)
		return
	}
	if expected.TokenHeader != actual.TokenHeader {
		t.Errorf("Auth.TokenHeader mismatch: expected %q, got %q", expected.TokenHeader, actual.TokenHeader)
	}
	if expected.IssuerURL != actual.IssuerURL {
		t.Errorf("Auth.IssuerURL mismatch: expected %q, got %q", expected.IssuerURL, actual.IssuerURL)
	}
	if expected.Audience != actual.Audience {
		t.Errorf("Auth.Audience mismatch: expected %q, got %q", expected.Audience, actual.Audience)
	}
	if len(expected.Scopes) != len(actual.Scopes) {
		t.Errorf("Auth.Scopes mismatch: expected %v, got %v", expected.Scopes, actual.Scopes)
	} else {
		for i := range expected.Scopes {
			if expected.Scopes[i] != actual.Scopes[i] {
				t.Errorf("Auth.Scopes mismatch: expected %v, got %v", expected.Scopes, actual.Scopes)
				break
			}
		}
	}
}

func verifyPolicy(t *testing.T, expected, actual *Config) {
	if expected == nil && actual == nil {
		return
	}
	if expected == nil || actual == nil {
		t.Fatalf("Policy nil mismatch: expected %v, got %v", expected == nil, actual == nil)
		return
	}
	if expected.Mode != actual.Mode {
		t.Errorf("Policy.Mode mismatch: expected %q, got %q", expected.Mode, actual.Mode)
	}
	if expected.DefaultDecision != actual.DefaultDecision {
		t.Errorf("Policy.DefaultDecision mismatch: expected %q, got %q", expected.DefaultDecision, actual.DefaultDecision)
	}
	if expected.EnforceOn != actual.EnforceOn {
		t.Errorf("Policy.EnforceOn mismatch: expected %q, got %q", expected.EnforceOn, actual.EnforceOn)
	}
	if expected.PolicyVersion != actual.PolicyVersion {
		t.Errorf("Policy.PolicyVersion mismatch: expected %q, got %q", expected.PolicyVersion, actual.PolicyVersion)
	}
}

func verifySession(t *testing.T, expected, actual *Session) {
	if expected == nil && actual == nil {
		return
	}
	if expected == nil || actual == nil {
		t.Fatalf("Session nil mismatch: expected %v, got %v", expected == nil, actual == nil)
		return
	}
	if expected.Required != actual.Required {
		t.Errorf("Session.Required mismatch: expected %v, got %v", expected.Required, actual.Required)
	}
	if expected.Store != actual.Store {
		t.Errorf("Session.Store mismatch: expected %q, got %q", expected.Store, actual.Store)
	}
	if expected.MaxLifetime != actual.MaxLifetime {
		t.Errorf("Session.MaxLifetime mismatch: expected %q, got %q", expected.MaxLifetime, actual.MaxLifetime)
	}
	if expected.IdleTimeout != actual.IdleTimeout {
		t.Errorf("Session.IdleTimeout mismatch: expected %q, got %q", expected.IdleTimeout, actual.IdleTimeout)
	}
	if expected.UpstreamTokenHeader != actual.UpstreamTokenHeader {
		t.Errorf("Session.UpstreamTokenHeader mismatch: expected %q, got %q", expected.UpstreamTokenHeader, actual.UpstreamTokenHeader)
	}
}

func verifyTools(t *testing.T, expected, actual []Tool) {
	if len(expected) != len(actual) {
		t.Fatalf("Tools length mismatch: expected %d, got %d", len(expected), len(actual))
		return
	}
	for i, exp := range expected {
		act := actual[i]
		if exp.Name != act.Name {
			t.Errorf("Tool[%d].Name mismatch: expected %q, got %q", i, exp.Name, act.Name)
		}
		if exp.Description != act.Description {
			t.Errorf("Tool[%d].Description mismatch: expected %q, got %q", i, exp.Description, act.Description)
		}
		if exp.RequiredTrust != act.RequiredTrust {
			t.Errorf("Tool[%d].RequiredTrust mismatch: expected %q, got %q", i, exp.RequiredTrust, act.RequiredTrust)
		}
		if exp.SideEffect != act.SideEffect {
			t.Errorf("Tool[%d].SideEffect mismatch: expected %q, got %q", i, exp.SideEffect, act.SideEffect)
		}
		if len(exp.Labels) != len(act.Labels) {
			t.Errorf("Tool[%d].Labels length mismatch: expected %d, got %d", i, len(exp.Labels), len(act.Labels))
		}
		for k, v := range exp.Labels {
			if act.Labels[k] != v {
				t.Errorf("Tool[%d].Labels[%q] mismatch: expected %q, got %q", i, k, v, act.Labels[k])
			}
		}
	}
}

func verifyGrants(t *testing.T, expected, actual []Grant) {
	if len(expected) != len(actual) {
		t.Fatalf("Grants length mismatch: expected %d, got %d", len(expected), len(actual))
		return
	}
	for i, exp := range expected {
		act := actual[i]
		if exp.Name != act.Name {
			t.Errorf("Grant[%d].Name mismatch: expected %q, got %q", i, exp.Name, act.Name)
		}
		if exp.HumanID != act.HumanID {
			t.Errorf("Grant[%d].HumanID mismatch: expected %q, got %q", i, exp.HumanID, act.HumanID)
		}
		if exp.AgentID != act.AgentID {
			t.Errorf("Grant[%d].AgentID mismatch: expected %q, got %q", i, exp.AgentID, act.AgentID)
		}
		if exp.TeamID != act.TeamID {
			t.Errorf("Grant[%d].TeamID mismatch: expected %q, got %q", i, exp.TeamID, act.TeamID)
		}
		if exp.MaxTrust != act.MaxTrust {
			t.Errorf("Grant[%d].MaxTrust mismatch: expected %q, got %q", i, exp.MaxTrust, act.MaxTrust)
		}
		if exp.PolicyVersion != act.PolicyVersion {
			t.Errorf("Grant[%d].PolicyVersion mismatch: expected %q, got %q", i, exp.PolicyVersion, act.PolicyVersion)
		}
		if exp.Disabled != act.Disabled {
			t.Errorf("Grant[%d].Disabled mismatch: expected %v, got %v", i, exp.Disabled, act.Disabled)
		}
		if len(exp.AllowedSideEffects) != len(act.AllowedSideEffects) {
			t.Fatalf("Grant[%d].AllowedSideEffects length mismatch: expected %d, got %d", i, len(exp.AllowedSideEffects), len(act.AllowedSideEffects))
			return
		}
		for j, sideEffect := range exp.AllowedSideEffects {
			if act.AllowedSideEffects[j] != sideEffect {
				t.Errorf("Grant[%d].AllowedSideEffects[%d] mismatch: expected %q, got %q", i, j, sideEffect, act.AllowedSideEffects[j])
			}
		}
		if len(exp.ToolRules) != len(act.ToolRules) {
			t.Fatalf("Grant[%d].ToolRules length mismatch: expected %d, got %d", i, len(exp.ToolRules), len(act.ToolRules))
			return
		}
		for j, rule := range exp.ToolRules {
			if act.ToolRules[j].Name != rule.Name {
				t.Errorf("Grant[%d].ToolRules[%d].Name mismatch: expected %q, got %q", i, j, rule.Name, act.ToolRules[j].Name)
			}
			if act.ToolRules[j].Decision != rule.Decision {
				t.Errorf("Grant[%d].ToolRules[%d].Decision mismatch: expected %q, got %q", i, j, rule.Decision, act.ToolRules[j].Decision)
			}
			if act.ToolRules[j].RequiredTrust != rule.RequiredTrust {
				t.Errorf("Grant[%d].ToolRules[%d].RequiredTrust mismatch: expected %q, got %q", i, j, rule.RequiredTrust, act.ToolRules[j].RequiredTrust)
			}
		}
	}
}

func verifySessions(t *testing.T, expected, actual []Binding) {
	if len(expected) != len(actual) {
		t.Fatalf("Sessions length mismatch: expected %d, got %d", len(expected), len(actual))
		return
	}
	for i, exp := range expected {
		act := actual[i]
		if exp.Name != act.Name {
			t.Errorf("Binding[%d].Name mismatch: expected %q, got %q", i, exp.Name, act.Name)
		}
		if exp.HumanID != act.HumanID {
			t.Errorf("Binding[%d].HumanID mismatch: expected %q, got %q", i, exp.HumanID, act.HumanID)
		}
		if exp.AgentID != act.AgentID {
			t.Errorf("Binding[%d].AgentID mismatch: expected %q, got %q", i, exp.AgentID, act.AgentID)
		}
		if exp.TeamID != act.TeamID {
			t.Errorf("Binding[%d].TeamID mismatch: expected %q, got %q", i, exp.TeamID, act.TeamID)
		}
		if exp.ConsentedTrust != act.ConsentedTrust {
			t.Errorf("Binding[%d].ConsentedTrust mismatch: expected %q, got %q", i, exp.ConsentedTrust, act.ConsentedTrust)
		}
		if exp.Revoked != act.Revoked {
			t.Errorf("Binding[%d].Revoked mismatch: expected %v, got %v", i, exp.Revoked, act.Revoked)
		}
		if exp.ExpiresAt != act.ExpiresAt {
			t.Errorf("Binding[%d].ExpiresAt mismatch: expected %q, got %q", i, exp.ExpiresAt, act.ExpiresAt)
		}
		if exp.PolicyVersion != act.PolicyVersion {
			t.Errorf("Binding[%d].PolicyVersion mismatch: expected %q, got %q", i, exp.PolicyVersion, act.PolicyVersion)
		}
		if exp.UpstreamTokenRef != act.UpstreamTokenRef {
			t.Errorf("Binding[%d].UpstreamTokenRef mismatch: expected %q, got %q", i, exp.UpstreamTokenRef, act.UpstreamTokenRef)
		}
	}
}

// TestTrustNormalization verifies trust level normalization is consistent.
func TestTrustNormalization(t *testing.T) {
	tests := []struct {
		input    string
		expected string
		rank     int
	}{
		{"low", "low", 1},
		{"LOW", "low", 1},
		{"Low", "low", 1},
		{"medium", "medium", 2},
		{"MEDIUM", "medium", 2},
		{"Medium", "medium", 2},
		{"high", "high", 3},
		{"HIGH", "high", 3},
		{"High", "high", 3},
		{"", "low", 1},
		{"unknown", "low", 1},
		{"invalid", "low", 1},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			normalized := NormalizeTrust(tc.input)
			if normalized != tc.expected {
				t.Errorf("NormalizeTrust(%q) = %q, expected %q", tc.input, normalized, tc.expected)
			}
			rank := TrustRank(tc.input)
			if rank != tc.rank {
				t.Errorf("TrustRank(%q) = %d, expected %d", tc.input, rank, tc.rank)
			}
			// Verify round-trip
			if RankToTrust(rank) != normalized {
				t.Errorf("RankToTrust(TrustRank(%q)) = %q, expected %q", tc.input, RankToTrust(rank), normalized)
			}
		})
	}
}

// TestPolicyHelperFunctions verifies helper function behavior.
func TestPolicyHelperFunctions(t *testing.T) {
	t.Run("FirstNonEmpty", func(t *testing.T) {
		if got := FirstNonEmpty("", "", "value"); got != "value" {
			t.Errorf("FirstNonEmpty('', '', 'value') = %q, expected 'value'", got)
		}
		if got := FirstNonEmpty("first", "second"); got != "first" {
			t.Errorf("FirstNonEmpty('first', 'second') = %q, expected 'first'", got)
		}
		if got := FirstNonEmpty("", "  "); got != "" {
			t.Errorf("FirstNonEmpty('', '  ') = %q, expected ''", got)
		}
	})

	t.Run("IsToolCallMethod", func(t *testing.T) {
		if !IsToolCallMethod("tools/call") {
			t.Error("IsToolCallMethod('tools/call') should be true")
		}
		if !IsToolCallMethod("call_tool") {
			t.Error("IsToolCallMethod('call_tool') should be true")
		}
		if IsToolCallMethod("other_method") {
			t.Error("IsToolCallMethod('other_method') should be false")
		}
	})

	t.Run("PolicyUsesOAuth", func(t *testing.T) {
		if !PolicyUsesOAuth(&Document{Auth: &Auth{IssuerURL: "https://issuer.example.com", Audience: "https://mcp.example.com/mcp"}}) {
			t.Error("PolicyUsesOAuth with issuer and audience should be true")
		}
		if PolicyUsesOAuth(&Document{Auth: &Auth{TrustDomain: "example.org"}}) {
			t.Error("PolicyUsesOAuth with adapter trust domain only should be false")
		}
		if PolicyUsesOAuth(nil) {
			t.Error("PolicyUsesOAuth with nil document should be false")
		}
	})
}
