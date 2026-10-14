package main

import (
	"github.com/golang-jwt/jwt/v4"
	"testing"
)

func TestCertificateOAuthSelectsAssertedSubjectTeam(t *testing.T) {
	policy := oauthPolicy("https://issuer.example.com")
	policy.Server.TeamID = "resource-a"
	for _, tc := range []struct {
		name         string
		claims       jwt.MapClaims
		wantTeam     string
		wantAllowed  bool
		wantMismatch string
	}{
		{"both memberships", jwt.MapClaims{"team_ids": []any{"resource-a", "subject-b"}}, "subject-b", true, ""},
		{"missing subject membership", jwt.MapClaims{"team_ids": []any{"resource-a", "other-c"}}, "", false, ""},
		{"no team claim", jwt.MapClaims{}, "", true, ""},
		{"explicit subject", jwt.MapClaims{"team_id": "subject-b"}, "subject-b", true, ""},
		{"explicit conflicting team", jwt.MapClaims{"team_id": "resource-a", "team_ids": []any{"subject-b"}}, "resource-a", true, "certificate_oauth_team_mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			team, allowed := oauthTeamIDForSubject(tc.claims, policy, "subject-b")
			if team != tc.wantTeam || allowed != tc.wantAllowed {
				t.Fatalf("team=%q allowed=%t", team, allowed)
			}
			if allowed {
				reason := certificateOAuthMismatch(identityContext{HumanID: "human", TeamID: "subject-b"}, identityContext{HumanID: "human", TeamID: team})
				if reason != tc.wantMismatch {
					t.Fatalf("reason=%q", reason)
				}
			}
		})
	}
	if team, allowed := oauthTeamIDForSubject(jwt.MapClaims{"team_ids": []any{"resource-a", "subject-b"}}, policy, ""); !allowed || team != "resource-a" {
		t.Fatalf("direct OAuth changed: team=%q allowed=%t", team, allowed)
	}
}
