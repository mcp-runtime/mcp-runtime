package policy

import (
	"net/http"
	"testing"
	"time"
)

func TestAuthorizeAllowsNonToolCalls(t *testing.T) {
	t.Parallel()

	decision := Authorize(&Document{
		Policy: &Config{
			Mode:            "allow-list",
			DefaultDecision: "deny",
			PolicyVersion:   "test-policy",
		},
	}, Request{RPCMethod: "initialize"}, time.Time{})

	if !decision.Allowed || decision.Status != http.StatusOK {
		t.Fatalf("decision = %#v, want allowed non-tool call", decision)
	}
}

func TestAuthorizeObserveModeAllowsWithoutIdentity(t *testing.T) {
	t.Parallel()

	decision := Authorize(&Document{
		Policy: &Config{
			Mode:            "observe",
			DefaultDecision: "deny",
			PolicyVersion:   "test-policy",
		},
	}, Request{RPCMethod: "tools/call", ToolName: "upper"}, time.Time{})

	if !decision.Allowed {
		t.Fatalf("decision = %#v, want observe-mode allow", decision)
	}
}

func TestAuthorizeDefaultDecision(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		policy    *Document
		wantAllow bool
		wantCode  int
	}{
		{
			name: "default deny",
			policy: &Document{
				Policy: &Config{
					Mode:            "allow-list",
					DefaultDecision: "deny",
					PolicyVersion:   "test-policy",
				},
			},
			wantCode: http.StatusForbidden,
		},
		{
			name: "default allow",
			policy: &Document{
				Policy: &Config{
					Mode:            "allow-list",
					DefaultDecision: "allow",
					PolicyVersion:   "test-policy",
				},
			},
			wantAllow: true,
			wantCode:  http.StatusOK,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			decision := Authorize(tc.policy, Request{
				Identity:  Identity{HumanID: "human-1"},
				RPCMethod: "tools/call",
				ToolName:  "upper",
			}, time.Time{})

			if decision.Allowed != tc.wantAllow || decision.Status != tc.wantCode || decision.Reason != "no_matching_grant" {
				t.Fatalf("decision = %#v, want allowed=%v status=%d reason=no_matching_grant", decision, tc.wantAllow, tc.wantCode)
			}
		})
	}
}

func TestAuthorizeExpiredGrantIsIgnored(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	policy := testPolicyWithGrant()
	policy.Grants[0].ExpiresAt = now.Format(time.RFC3339)
	decision := Authorize(policy, Request{
		Identity:  Identity{HumanID: "human-1", AgentID: "agent-1"},
		RPCMethod: "tools/call",
		ToolName:  "upper",
	}, now)
	if decision.Allowed || decision.Reason != "grant_expired" {
		t.Fatalf("decision = %#v, want expired grant denied as grant_expired", decision)
	}
}

func TestAuthorizeMonitorGrantAuditsWouldDenyButAllows(t *testing.T) {
	policy := testPolicyWithGrant()
	policy.Grants[0].Mode = "monitor"
	policy.Grants[0].MaxTrust = "low"
	decision := Authorize(policy, Request{
		Identity:  Identity{HumanID: "human-1", AgentID: "agent-1"},
		RPCMethod: "tools/call",
		ToolName:  "upper",
	}, time.Time{})
	if !decision.Allowed || !decision.WouldDeny || decision.WouldDenyReason != "trust_too_low" {
		t.Fatalf("decision = %#v, want allow with trust_too_low shadow denial", decision)
	}
}

func TestAuthorizeMonitorGrantPreservesEnforceDenyAndSessionGates(t *testing.T) {
	t.Run("enforce grant deny takes precedence", func(t *testing.T) {
		policy := testPolicyWithGrant()
		policy.Grants[0].Name = "a-monitor"
		policy.Grants[0].Mode = "monitor"
		policy.Grants[0].ToolRules = []ToolAccess{{Name: "upper", Decision: "deny"}}
		enforceGrant := policy.Grants[0]
		enforceGrant.Name = "z-enforce"
		enforceGrant.Mode = "enforce"
		policy.Grants = append(policy.Grants, enforceGrant)
		decision := Authorize(policy, Request{Identity: Identity{HumanID: "human-1", AgentID: "agent-1"}, RPCMethod: "tools/call", ToolName: "upper"}, time.Time{})
		if decision.Allowed || decision.WouldDeny || decision.Reason != "tool_denied" {
			t.Fatalf("decision = %#v, want enforcing tool denial", decision)
		}
	})

	t.Run("enforce grant allow passes while monitor deny is audited", func(t *testing.T) {
		policy := testPolicyWithGrant()
		policy.Grants[0].Name = "a-monitor"
		policy.Grants[0].Mode = "monitor"
		policy.Grants[0].ToolRules = []ToolAccess{{Name: "upper", Decision: "deny"}}
		enforceGrant := testPolicyWithGrant().Grants[0]
		enforceGrant.Name = "z-enforce"
		policy.Grants = append(policy.Grants, enforceGrant)
		decision := Authorize(policy, Request{Identity: Identity{HumanID: "human-1", AgentID: "agent-1"}, RPCMethod: "tools/call", ToolName: "upper"}, time.Time{})
		if !decision.Allowed || !decision.WouldDeny || decision.WouldDenyReason != "tool_denied" {
			t.Fatalf("decision = %#v, want enforce allow with monitor would-deny audit", decision)
		}
	})

	t.Run("required session denial remains fail closed", func(t *testing.T) {
		policy := testPolicyWithGrant()
		policy.Grants[0].Mode = "monitor"
		policy.Session.Required = true
		decision := Authorize(policy, Request{Identity: Identity{HumanID: "human-1", AgentID: "agent-1"}, RPCMethod: "tools/call", ToolName: "upper"}, time.Time{})
		if decision.Allowed || decision.WouldDeny || decision.Reason != "missing_session" {
			t.Fatalf("decision = %#v, want missing-session denial", decision)
		}
	})
}

func TestAuthorizeEnforceModeKeepsDenialAndMonitorModeRecordsDefaultAllow(t *testing.T) {
	t.Run("default enforce keeps trust denial", func(t *testing.T) {
		policy := testPolicyWithGrant()
		policy.Grants[0].MaxTrust = "low"
		decision := Authorize(policy, Request{Identity: Identity{HumanID: "human-1", AgentID: "agent-1"}, RPCMethod: "tools/call", ToolName: "upper"}, time.Time{})
		if decision.Allowed || decision.WouldDeny || decision.Reason != "trust_too_low" {
			t.Fatalf("decision = %#v, want unchanged enforce denial", decision)
		}
	})

	t.Run("default allow still records monitor denial", func(t *testing.T) {
		policy := testPolicyWithGrant()
		policy.Policy.DefaultDecision = "allow"
		policy.Grants[0].Mode = "monitor"
		policy.Grants[0].ToolRules = []ToolAccess{{Name: "other", Decision: "allow"}}
		decision := Authorize(policy, Request{Identity: Identity{HumanID: "human-1", AgentID: "agent-1"}, RPCMethod: "tools/call", ToolName: "upper"}, time.Time{})
		if !decision.Allowed || !decision.WouldDeny || decision.WouldDenyReason != "tool_not_granted" {
			t.Fatalf("decision = %#v, want default allow with monitor would-deny audit", decision)
		}
	})
}

func TestAuthorizeExpiredGrantNextToActiveGrantReportsToolNotGranted(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	policy := testPolicyWithGrant()
	policy.Grants[0].ExpiresAt = now.Add(-time.Minute).Format(time.RFC3339)
	// An active grant for the same subject that does not cover the tool.
	policy.Grants = append(policy.Grants, Grant{
		Name:               "grant-2",
		HumanID:            "human-1",
		AgentID:            "agent-1",
		MaxTrust:           "high",
		AllowedSideEffects: []string{"read"},
		ToolRules:          []ToolAccess{{Name: "other", Decision: "allow"}},
	})
	decision := Authorize(policy, Request{
		Identity:  Identity{HumanID: "human-1", AgentID: "agent-1"},
		RPCMethod: "tools/call",
		ToolName:  "upper",
	}, now)
	if decision.Allowed || decision.Reason != "tool_not_granted" {
		t.Fatalf("decision = %#v, want tool_not_granted when an active grant still matches", decision)
	}
}

func TestAuthorizeGrantBeforeExpiryIsAllowed(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	policy := testPolicyWithGrant()
	policy.Grants[0].ExpiresAt = now.Add(time.Second).Format(time.RFC3339)
	decision := Authorize(policy, Request{
		Identity:  Identity{HumanID: "human-1", AgentID: "agent-1"},
		RPCMethod: "tools/call",
		ToolName:  "upper",
	}, now)
	if !decision.Allowed {
		t.Fatalf("decision = %#v, want allowed before expiry", decision)
	}
}

func TestAuthorizeOptionalSessionDoesNotApplyWithoutSessionHeader(t *testing.T) {
	t.Parallel()

	policy := testPolicyWithGrant()
	policy.Sessions = []Binding{
		{
			Name:           "session-1",
			HumanID:        "human-1",
			AgentID:        "agent-1",
			ConsentedTrust: "low",
		},
	}

	decision := Authorize(policy, Request{
		Identity:  Identity{HumanID: "human-1", AgentID: "agent-1"},
		RPCMethod: "tools/call",
		ToolName:  "upper",
	}, time.Time{})

	if !decision.Allowed {
		t.Fatalf("decision = %#v, want allowed request", decision)
	}
	if decision.ConsentedTrust != "high" || decision.EffectiveTrust != "high" {
		t.Fatalf("decision = %#v, want optional session ignored without header", decision)
	}
}

func TestAuthorizeOptionalSessionRequiresLiveSessionHeader(t *testing.T) {
	t.Parallel()

	liveSessionPolicy := testPolicyWithGrant()
	liveSessionPolicy.Sessions = []Binding{
		{
			Name:           "session-1",
			HumanID:        "human-1",
			AgentID:        "agent-1",
			ConsentedTrust: "low",
		},
	}

	denyDecision := Authorize(liveSessionPolicy, Request{
		Identity:  Identity{HumanID: "human-1", AgentID: "agent-1", SessionID: "session-1"},
		RPCMethod: "tools/call",
		ToolName:  "upper",
	}, time.Time{})

	if denyDecision.Reason != "trust_too_low" {
		t.Fatalf("deny decision = %#v, want trust_too_low", denyDecision)
	}

	revokedSessionPolicy := testPolicyWithGrant()
	revokedSessionPolicy.Sessions = []Binding{
		{
			Name:           "session-1",
			HumanID:        "human-1",
			AgentID:        "agent-1",
			ConsentedTrust: "low",
			Revoked:        true,
		},
	}

	allowDecision := Authorize(revokedSessionPolicy, Request{
		Identity:  Identity{HumanID: "human-1", AgentID: "agent-1", SessionID: "session-1"},
		RPCMethod: "tools/call",
		ToolName:  "upper",
	}, time.Time{})

	if !allowDecision.Allowed {
		t.Fatalf("allow decision = %#v, want revoked optional session ignored", allowDecision)
	}
	if allowDecision.ConsentedTrust != "high" || allowDecision.EffectiveTrust != "high" {
		t.Fatalf("allow decision = %#v, want admin trust when optional session is revoked", allowDecision)
	}
}

func TestAuthorizeRequiredSessionFailures(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		identity Identity
		sessions []Binding
		reason   string
		status   int
	}{
		{
			name:     "missing session header",
			identity: Identity{HumanID: "human-1", AgentID: "agent-1"},
			reason:   "missing_session",
			status:   http.StatusUnauthorized,
		},
		{
			name:     "session not found",
			identity: Identity{HumanID: "human-1", AgentID: "agent-1", SessionID: "missing"},
			sessions: []Binding{{Name: "session-1", HumanID: "human-1", AgentID: "agent-1", ConsentedTrust: "high"}},
			reason:   "session_not_found",
			status:   http.StatusUnauthorized,
		},
		{
			name:     "revoked session",
			identity: Identity{HumanID: "human-1", AgentID: "agent-1", SessionID: "session-1"},
			sessions: []Binding{{Name: "session-1", HumanID: "human-1", AgentID: "agent-1", ConsentedTrust: "high", Revoked: true}},
			reason:   "session_revoked",
			status:   http.StatusUnauthorized,
		},
		{
			name:     "expired session",
			identity: Identity{HumanID: "human-1", AgentID: "agent-1", SessionID: "session-1"},
			sessions: []Binding{{Name: "session-1", HumanID: "human-1", AgentID: "agent-1", ConsentedTrust: "high", ExpiresAt: now.Add(-time.Minute).Format(time.RFC3339)}},
			reason:   "session_expired",
			status:   http.StatusUnauthorized,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			policy := testPolicyWithGrant()
			policy.Session.Required = true
			policy.Sessions = tc.sessions

			decision := Authorize(policy, Request{
				Identity:  tc.identity,
				RPCMethod: "tools/call",
				ToolName:  "upper",
			}, now)

			if decision.Allowed || decision.Status != tc.status || decision.Reason != tc.reason {
				t.Fatalf("decision = %#v, want deny status=%d reason=%s", decision, tc.status, tc.reason)
			}
		})
	}
}

func TestAuthorizeTrustTooLow(t *testing.T) {
	t.Parallel()

	policy := testPolicyWithGrant()
	policy.Grants[0].MaxTrust = "low"

	decision := Authorize(policy, Request{
		Identity:  Identity{HumanID: "human-1", AgentID: "agent-1"},
		RPCMethod: "tools/call",
		ToolName:  "upper",
	}, time.Time{})

	if decision.Allowed || decision.Reason != "trust_too_low" {
		t.Fatalf("decision = %#v, want trust_too_low denial", decision)
	}
	if decision.RequiredTrust != "medium" || decision.AdminTrust != "low" || decision.EffectiveTrust != "low" {
		t.Fatalf("decision = %#v, want trust context included", decision)
	}
}

func TestAuthorizeMatchesTeamScopedGrant(t *testing.T) {
	t.Parallel()

	policy := testPolicyWithGrant()
	policy.Grants = []Grant{
		{
			Name:               "team-grant",
			TeamID:             "team-acme",
			MaxTrust:           "high",
			AllowedSideEffects: []string{"read"},
			ToolRules:          []ToolAccess{{Name: "upper", Decision: "allow"}},
		},
	}

	allowed := Authorize(policy, Request{
		Identity:  Identity{TeamID: "team-acme"},
		RPCMethod: "tools/call",
		ToolName:  "upper",
	}, time.Time{})
	if !allowed.Allowed {
		t.Fatalf("decision = %#v, want team scoped grant allowed", allowed)
	}

	denied := Authorize(policy, Request{
		Identity:  Identity{TeamID: "team-other"},
		RPCMethod: "tools/call",
		ToolName:  "upper",
	}, time.Time{})
	if denied.Allowed || denied.Reason != "no_matching_grant" {
		t.Fatalf("decision = %#v, want no_matching_grant", denied)
	}
}

func TestAuthorizeRequiresSessionTeamMatch(t *testing.T) {
	t.Parallel()

	policy := testPolicyWithGrant()
	policy.Session.Required = true
	policy.Grants[0].TeamID = "team-acme"
	policy.Sessions = []Binding{
		{
			Name:           "session-1",
			HumanID:        "human-1",
			AgentID:        "agent-1",
			TeamID:         "team-acme",
			ConsentedTrust: "high",
		},
	}

	decision := Authorize(policy, Request{
		Identity:  Identity{HumanID: "human-1", AgentID: "agent-1", TeamID: "team-other", SessionID: "session-1"},
		RPCMethod: "tools/call",
		ToolName:  "upper",
	}, time.Time{})
	if decision.Allowed || decision.Reason != "session_not_found" {
		t.Fatalf("decision = %#v, want team-mismatched session_not_found", decision)
	}
}

func TestAuthorizeAllowsGrantBySideEffectWithoutToolRules(t *testing.T) {
	t.Parallel()

	policy := testPolicyWithGrant()
	policy.Grants[0].ToolRules = nil
	policy.Grants[0].AllowedSideEffects = []string{"read"}

	decision := Authorize(policy, Request{
		Identity:  Identity{HumanID: "human-1", AgentID: "agent-1"},
		RPCMethod: "tools/call",
		ToolName:  "upper",
	}, time.Time{})

	if !decision.Allowed {
		t.Fatalf("decision = %#v, want read side-effect grant to allow tool", decision)
	}
	if decision.RequiredSideEffect != "read" {
		t.Fatalf("decision = %#v, want required side effect read", decision)
	}
}

func TestAuthorizeDeniesDisallowedSideEffect(t *testing.T) {
	t.Parallel()

	policy := testPolicyWithGrant()
	policy.Tools = append(policy.Tools, Tool{Name: "delete_row", RequiredTrust: "high", SideEffect: "destructive"})
	policy.Grants[0].AllowedSideEffects = []string{"read", "write"}
	policy.Grants[0].ToolRules = []ToolAccess{{Name: "delete_row", Decision: "allow"}}

	decision := Authorize(policy, Request{
		Identity:  Identity{HumanID: "human-1", AgentID: "agent-1"},
		RPCMethod: "tools/call",
		ToolName:  "delete_row",
	}, time.Time{})

	if decision.Allowed || decision.Reason != "side_effect_not_allowed" {
		t.Fatalf("decision = %#v, want side_effect_not_allowed", decision)
	}
	if decision.RequiredSideEffect != "destructive" {
		t.Fatalf("decision = %#v, want destructive side effect context", decision)
	}
}

func TestAuthorizeDeniesUnknownToolSideEffect(t *testing.T) {
	t.Parallel()

	policy := testPolicyWithGrant()
	policy.Tools[0].SideEffect = ""

	decision := Authorize(policy, Request{
		Identity:  Identity{HumanID: "human-1", AgentID: "agent-1"},
		RPCMethod: "tools/call",
		ToolName:  "upper",
	}, time.Time{})

	if decision.Allowed || decision.Reason != "tool_side_effect_unknown" {
		t.Fatalf("decision = %#v, want tool_side_effect_unknown", decision)
	}
}

func TestAuthorizeIgnoresRuleTrustFromGrantWithoutSideEffect(t *testing.T) {
	t.Parallel()

	policy := testPolicyWithGrant()
	policy.Grants = []Grant{
		{
			Name:               "read-grant",
			HumanID:            "human-1",
			AgentID:            "agent-1",
			MaxTrust:           "medium",
			AllowedSideEffects: []string{"read"},
			ToolRules:          []ToolAccess{{Name: "upper", Decision: "allow"}},
		},
		{
			Name:               "wrong-side-effect-grant",
			HumanID:            "human-1",
			AgentID:            "agent-1",
			MaxTrust:           "high",
			AllowedSideEffects: []string{"write"},
			ToolRules:          []ToolAccess{{Name: "upper", Decision: "allow", RequiredTrust: "high"}},
		},
	}

	decision := Authorize(policy, Request{
		Identity:  Identity{HumanID: "human-1", AgentID: "agent-1"},
		RPCMethod: "tools/call",
		ToolName:  "upper",
	}, time.Time{})

	if !decision.Allowed {
		t.Fatalf("decision = %#v, want side-effect-allowed grant to control trust", decision)
	}
	if decision.RequiredTrust != "medium" || decision.AdminTrust != "medium" {
		t.Fatalf("decision = %#v, want read grant trust only", decision)
	}
}

func TestAuthorizeReportsMatchedGrantOnAllow(t *testing.T) {
	t.Parallel()

	decision := Authorize(testPolicyWithGrant(), Request{
		Identity:  Identity{HumanID: "human-1", AgentID: "agent-1"},
		RPCMethod: "tools/call",
		ToolName:  "upper",
	}, time.Time{})

	if !decision.Allowed {
		t.Fatalf("decision = %#v, want allowed", decision)
	}
	if decision.MatchedGrant != "grant-1" {
		t.Fatalf("decision = %#v, want matched grant grant-1", decision)
	}
	if decision.MatchedSession != "" {
		t.Fatalf("decision = %#v, want no matched session without session header", decision)
	}
}

func TestAuthorizeReportsMatchedGrantOnToolDenied(t *testing.T) {
	t.Parallel()

	policy := testPolicyWithGrant()
	policy.Grants[0].ToolRules = []ToolAccess{{Name: "upper", Decision: "deny"}}

	decision := Authorize(policy, Request{
		Identity:  Identity{HumanID: "human-1", AgentID: "agent-1"},
		RPCMethod: "tools/call",
		ToolName:  "upper",
	}, time.Time{})

	if decision.Allowed || decision.Reason != "tool_denied" {
		t.Fatalf("decision = %#v, want tool_denied", decision)
	}
	if decision.MatchedGrant != "grant-1" {
		t.Fatalf("decision = %#v, want denying grant attributed", decision)
	}
}

func TestAuthorizeReportsMatchedSession(t *testing.T) {
	t.Parallel()

	policy := testPolicyWithGrant()
	policy.Session.Required = true
	policy.Sessions = []Binding{
		{
			Name:           "session-1",
			HumanID:        "human-1",
			AgentID:        "agent-1",
			ConsentedTrust: "high",
		},
	}

	decision := Authorize(policy, Request{
		Identity:  Identity{HumanID: "human-1", AgentID: "agent-1", SessionID: "session-1"},
		RPCMethod: "tools/call",
		ToolName:  "upper",
	}, time.Time{})

	if !decision.Allowed {
		t.Fatalf("decision = %#v, want allowed", decision)
	}
	if decision.MatchedSession != "session-1" || decision.MatchedGrant != "grant-1" {
		t.Fatalf("decision = %#v, want session and grant attribution", decision)
	}
}

func TestAuthorizeReportsMatchedSessionOnRevokedSession(t *testing.T) {
	t.Parallel()

	policy := testPolicyWithGrant()
	policy.Session.Required = true
	policy.Sessions = []Binding{
		{
			Name:           "session-1",
			HumanID:        "human-1",
			AgentID:        "agent-1",
			ConsentedTrust: "high",
			Revoked:        true,
		},
	}

	decision := Authorize(policy, Request{
		Identity:  Identity{HumanID: "human-1", AgentID: "agent-1", SessionID: "session-1"},
		RPCMethod: "tools/call",
		ToolName:  "upper",
	}, time.Time{})

	if decision.Allowed || decision.Reason != "session_revoked" {
		t.Fatalf("decision = %#v, want session_revoked", decision)
	}
	if decision.MatchedSession != "session-1" {
		t.Fatalf("decision = %#v, want revoked session attributed", decision)
	}
}

func TestAuthorizeAttributesHighestTrustGrant(t *testing.T) {
	t.Parallel()

	policy := testPolicyWithGrant()
	policy.Grants = []Grant{
		{
			Name:               "low-grant",
			HumanID:            "human-1",
			AgentID:            "agent-1",
			MaxTrust:           "medium",
			AllowedSideEffects: []string{"read"},
			ToolRules:          []ToolAccess{{Name: "upper", Decision: "allow"}},
		},
		{
			Name:               "high-grant",
			HumanID:            "human-1",
			AgentID:            "agent-1",
			MaxTrust:           "high",
			AllowedSideEffects: []string{"read"},
			ToolRules:          []ToolAccess{{Name: "upper", Decision: "allow"}},
		},
	}

	decision := Authorize(policy, Request{
		Identity:  Identity{HumanID: "human-1", AgentID: "agent-1"},
		RPCMethod: "tools/call",
		ToolName:  "upper",
	}, time.Time{})

	if !decision.Allowed {
		t.Fatalf("decision = %#v, want allowed", decision)
	}
	if decision.MatchedGrant != "high-grant" {
		t.Fatalf("decision = %#v, want highest-trust grant attributed", decision)
	}
}

func TestAuthorizeAttributesTrustRequirementGrantOnTrustTooLow(t *testing.T) {
	t.Parallel()

	policy := testPolicyWithGrant()
	policy.Tools = []Tool{{Name: "upper", RequiredTrust: "medium", SideEffect: "read"}}
	policy.Sessions = []Binding{{
		Name:           "session-1",
		HumanID:        "human-1",
		AgentID:        "agent-1",
		ConsentedTrust: "medium",
	}}
	policy.Grants = []Grant{
		{
			Name:               "high-cap-grant",
			HumanID:            "human-1",
			AgentID:            "agent-1",
			MaxTrust:           "high",
			AllowedSideEffects: []string{"read"},
			ToolRules:          []ToolAccess{{Name: "upper", Decision: "allow"}},
		},
		{
			Name:               "strict-rule-grant",
			HumanID:            "human-1",
			AgentID:            "agent-1",
			MaxTrust:           "medium",
			AllowedSideEffects: []string{"read"},
			ToolRules:          []ToolAccess{{Name: "upper", Decision: "allow", RequiredTrust: "high"}},
		},
	}

	decision := Authorize(policy, Request{
		Identity:  Identity{HumanID: "human-1", AgentID: "agent-1", SessionID: "session-1"},
		RPCMethod: "tools/call",
		ToolName:  "upper",
	}, time.Time{})

	if decision.Allowed || decision.Reason != "trust_too_low" {
		t.Fatalf("decision = %#v, want trust_too_low", decision)
	}
	if decision.MatchedGrant != "strict-rule-grant" {
		t.Fatalf("decision = %#v, want grant that imposed the failing trust requirement", decision)
	}
}

func TestAuthorizeReportsMatchedGrantOnSideEffectDenial(t *testing.T) {
	t.Parallel()

	policy := testPolicyWithGrant()
	policy.Tools = append(policy.Tools, Tool{Name: "delete_row", RequiredTrust: "high", SideEffect: "destructive"})
	policy.Grants[0].AllowedSideEffects = []string{"read"}
	policy.Grants[0].ToolRules = []ToolAccess{{Name: "delete_row", Decision: "allow"}}

	decision := Authorize(policy, Request{
		Identity:  Identity{HumanID: "human-1", AgentID: "agent-1"},
		RPCMethod: "tools/call",
		ToolName:  "delete_row",
	}, time.Time{})

	if decision.Allowed || decision.Reason != "side_effect_not_allowed" {
		t.Fatalf("decision = %#v, want side_effect_not_allowed", decision)
	}
	if decision.MatchedGrant != "grant-1" {
		t.Fatalf("decision = %#v, want tool-matching grant attributed", decision)
	}
}

func TestAuthorizePolicyVersionFollowsMatchedGrant(t *testing.T) {
	t.Parallel()

	policy := testPolicyWithGrant()
	policy.Grants = []Grant{
		{
			Name:               "matched-grant",
			HumanID:            "human-1",
			AgentID:            "agent-1",
			MaxTrust:           "high",
			PolicyVersion:      "matched-v1",
			AllowedSideEffects: []string{"read"},
			ToolRules:          []ToolAccess{{Name: "upper", Decision: "allow"}},
		},
		{
			Name:               "unrelated-grant",
			HumanID:            "human-1",
			AgentID:            "agent-1",
			MaxTrust:           "high",
			PolicyVersion:      "unrelated-v9",
			AllowedSideEffects: []string{"write"},
			ToolRules:          []ToolAccess{{Name: "other_tool", Decision: "allow"}},
		},
	}

	decision := Authorize(policy, Request{
		Identity:  Identity{HumanID: "human-1", AgentID: "agent-1"},
		RPCMethod: "tools/call",
		ToolName:  "upper",
	}, time.Time{})

	if !decision.Allowed {
		t.Fatalf("decision = %#v, want allowed", decision)
	}
	if decision.MatchedGrant != "matched-grant" {
		t.Fatalf("decision = %#v, want matched-grant attributed", decision)
	}
	if decision.PolicyVersion != "matched-v1" {
		t.Fatalf("decision = %#v, want policy version of the matched grant, not an unrelated one", decision)
	}
}

func TestAuthorizeReportsMatchedNamespaces(t *testing.T) {
	t.Parallel()

	policy := testPolicyWithGrant()
	policy.Grants[0].Namespace = "team-a"
	policy.Session.Required = true
	policy.Sessions = []Binding{
		{
			Name:           "session-1",
			Namespace:      "team-b",
			HumanID:        "human-1",
			AgentID:        "agent-1",
			ConsentedTrust: "high",
		},
	}

	decision := Authorize(policy, Request{
		Identity:  Identity{HumanID: "human-1", AgentID: "agent-1", SessionID: "session-1"},
		RPCMethod: "tools/call",
		ToolName:  "upper",
	}, time.Time{})

	if !decision.Allowed {
		t.Fatalf("decision = %#v, want allowed", decision)
	}
	if decision.MatchedGrantNamespace != "team-a" || decision.MatchedSessionNamespace != "team-b" {
		t.Fatalf("decision = %#v, want grant namespace team-a and session namespace team-b", decision)
	}
}

func testPolicyWithGrant() *Document {
	return &Document{
		Policy: &Config{
			Mode:            "allow-list",
			DefaultDecision: "deny",
			PolicyVersion:   "test-policy",
		},
		Session: &Session{
			Required: false,
		},
		Tools: []Tool{
			{Name: "upper", RequiredTrust: "medium", SideEffect: "read"},
		},
		Grants: []Grant{
			{
				Name:               "grant-1",
				HumanID:            "human-1",
				AgentID:            "agent-1",
				MaxTrust:           "high",
				AllowedSideEffects: []string{"read"},
				ToolRules:          []ToolAccess{{Name: "upper", Decision: "allow"}},
			},
		},
	}
}
