package runtimeapi

import (
	"context"
	"errors"
	"testing"
	"time"

	mcpaccess "mcp-runtime/pkg/access"
)

func TestCrossTeamGrantExpiryValid(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	maxTTL := 7 * 24 * time.Hour
	for _, tc := range []struct {
		name string
		at   time.Time
		want bool
	}{
		{name: "within cap", at: now.Add(6 * 24 * time.Hour), want: true},
		{name: "at cap", at: now.Add(maxTTL), want: true},
		{name: "past cap", at: now.Add(maxTTL + time.Second)},
		{name: "expired", at: now.Add(-time.Second)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := crossTeamGrantExpiryValid(tc.at, now, maxTTL); got != tc.want {
				t.Fatalf("crossTeamGrantExpiryValid() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestBindAccessSubjectPreservesExplicitForeignTeam(t *testing.T) {
	subject := mcpaccess.SubjectRef{HumanID: "user-b", TeamID: "team-b"}
	if err := (&AccessService{}).bindAccessSubjectTeamID(context.Background(), "mcp-team-a", "team-a", &subject); err != nil {
		t.Fatalf("bindAccessSubjectTeamID() error = %v", err)
	}
	if subject.TeamID != "team-b" {
		t.Fatalf("subject team = %q, want explicit grantee team team-b", subject.TeamID)
	}
}

func TestCrossTeamGrantMaxTTLRejectsInvalidConfiguration(t *testing.T) {
	t.Setenv("MCP_CROSS_TEAM_GRANT_MAX_TTL", "not-a-duration")
	if _, err := crossTeamGrantMaxTTL(); !errors.Is(err, errInvalidCrossTeamGrantMaxTTL) {
		t.Fatalf("crossTeamGrantMaxTTL() error = %v, want invalid configuration error", err)
	}
	t.Setenv("MCP_CROSS_TEAM_GRANT_MAX_TTL", "0s")
	if _, err := crossTeamGrantMaxTTL(); !errors.Is(err, errInvalidCrossTeamGrantMaxTTL) {
		t.Fatalf("crossTeamGrantMaxTTL() error = %v, want invalid configuration error", err)
	}
}
