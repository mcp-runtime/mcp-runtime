package main

import (
	"net/http"
	"testing"
	"time"

	"mcp-runtime/pkg/policy"
	"mcp-runtime/pkg/sessionstate"
)

func TestCertificateDenialPreservesOnlyVerifiedKnownActor(t *testing.T) {
	for _, tc := range []struct {
		name, spiffe                    string
		expired, unverified, wrongProxy bool
		wantReason                      string
		wantActor                       bool
	}{
		{name: "known revoked", wantReason: "session_revoked", wantActor: true},
		{name: "known expired", expired: true, wantReason: "session_expired", wantActor: true},
		{name: "unverified transport", unverified: true, wantReason: "missing_client_certificate"},
		{name: "wrong ingress peer", wrongProxy: true, wantReason: "untrusted_proxy"},
		{name: "wrong trust domain", spiffe: "spiffe://attacker.org/ns/authority/session/binding", wantReason: "invalid_spiffe_identity"},
		{name: "unknown binding", spiffe: "spiffe://example.org/ns/authority/session/missing", wantReason: "session_not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := minimalServer()
			s.trustedProxySPIFFE = "spiffe://example.org/ns/traefik/sa/traefik"
			binding := policy.Binding{Name: "binding", Namespace: "authority", HumanID: "human-b", AgentID: "agent-b", TeamID: "team-b", Revoked: !tc.expired}
			if tc.expired {
				binding.ExpiresAt = time.Now().Add(-time.Hour).Format(time.RFC3339)
			}
			s.sessionSync = &gatewaySessionSync{lastSync: time.Now(), staleWindow: time.Minute, sessions: map[string]sessionstate.Session{"authority/binding": {Binding: binding}}}
			spiffe := tc.spiffe
			if spiffe == "" {
				spiffe = "spiffe://example.org/ns/authority/session/binding"
			}
			ex := newTestExchange(http.MethodPost, "/mcp", `{}`, map[string]string{defaultVerifiedSPIFFEHeader: spiffe, "X-MCP-Human-ID": "forged-human", "X-MCP-Team-ID": "forged-team"})
			if !tc.unverified {
				peer := s.trustedProxySPIFFE
				if tc.wrongProxy {
					peer = "spiffe://example.org/ns/evil/sa/evil"
				}
				ex.R.TLS = verifiedProxyTLS(peer)
			}
			ex.Policy = &policy.Document{Server: policy.Server{Namespace: "authority", TeamID: "team-a"}, Auth: &policy.Auth{TrustDomain: "example.org"}, Session: &policy.Session{Store: "platform"}}
			// A valid SPIFFE URI without a known binding must not supply a human
			// or team identity for audit.
			if tc.wantReason == "session_not_found" {
				ex.Policy.Session = nil
			}
			if result := s.authFilter(ex); result != Reject || ex.Decision.Allowed || ex.Decision.Reason != tc.wantReason {
				t.Fatalf("result=%v decision=%+v", result, ex.Decision)
			}
			if tc.wantActor {
				if ex.Identity.TeamID != "team-b" || ex.Identity.HumanID != "human-b" || ex.Identity.SessionID != "binding" {
					t.Fatalf("actor=%+v", ex.Identity)
				}
				payload := s.auditPayload(ex.R, "/mcp", "tools/call", "aaa-ping", ex.Identity, ex.Policy, ex.Decision, http.StatusUnauthorized, 0, 0)
				if payload["subject_team_id"] != "team-b" || payload["resource_team_id"] != "team-a" {
					t.Fatalf("denial attribution=%+v", payload)
				}
			} else if ex.Identity != (identityContext{}) {
				t.Fatalf("unverified identity leaked into audit: %+v", ex.Identity)
			}
		})
	}
}
