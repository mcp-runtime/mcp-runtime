package runtimeapi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	mcpaccess "mcp-runtime/pkg/access"
)

const defaultCrossTeamGrantMaxTTL = 7 * 24 * time.Hour

var errInvalidCrossTeamGrantMaxTTL = errors.New("invalid MCP_CROSS_TEAM_GRANT_MAX_TTL")

func crossTeamGrantMaxTTL() (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv("MCP_CROSS_TEAM_GRANT_MAX_TTL"))
	if raw == "" {
		return defaultCrossTeamGrantMaxTTL, nil
	}
	ttl, err := time.ParseDuration(raw)
	if err != nil || ttl <= 0 {
		return 0, fmt.Errorf("%w: must be a positive duration", errInvalidCrossTeamGrantMaxTTL)
	}
	return ttl, nil
}

func crossTeamGrantExpiryValid(expiresAt time.Time, now time.Time, maxTTL time.Duration) bool {
	return maxTTL > 0 && expiresAt.After(now) && !expiresAt.After(now.Add(maxTTL))
}

// validateCrossTeamSubject confirms each explicitly named principal belongs
// to the subject team. Agent identity comes only from the platform directory.
func (s *AccessService) validateCrossTeamSubject(ctx context.Context, subject mcpaccess.SubjectRef) error {
	if s == nil || s.identity == nil || !s.identity.Configured() {
		return errors.New("platform identity is unavailable for cross-team subject validation")
	}
	teams, err := s.identity.ListTeams(ctx)
	if err != nil {
		return errors.New("failed to validate cross-team subject membership")
	}
	teamID := strings.TrimSpace(string(subject.TeamID))
	teamSlug := ""
	for _, team := range teams {
		if strings.TrimSpace(team.ID) == teamID {
			teamSlug = strings.TrimSpace(team.Slug)
			break
		}
	}
	if teamSlug == "" {
		return errors.New("subject.teamID does not identify an existing team")
	}
	if subject.HumanID == "" && subject.AgentID == "" {
		return errors.New("cross-team grant must identify a human or agent subject")
	}
	if subject.HumanID != "" {
		members, err := s.identity.ListTeamMemberships(ctx, teamSlug)
		if err != nil {
			return errors.New("failed to validate cross-team subject membership")
		}
		memberFound := false
		for _, member := range members {
			if strings.EqualFold(strings.TrimSpace(member.UserID), strings.TrimSpace(string(subject.HumanID))) ||
				strings.EqualFold(strings.TrimSpace(member.Email), strings.TrimSpace(string(subject.HumanID))) {
				memberFound = true
				break
			}
		}
		if !memberFound {
			return errors.New("subject human is not a member of subject.teamID")
		}
	}
	if subject.AgentID != "" {
		agent, found, err := s.identity.GetAgent(ctx, strings.TrimSpace(string(subject.AgentID)))
		if err != nil {
			return errors.New("failed to validate cross-team agent subject")
		}
		if !found || strings.TrimSpace(agent.ID) != strings.TrimSpace(string(subject.AgentID)) ||
			strings.TrimSpace(agent.TeamID) != teamID || strings.TrimSpace(agent.Status) != "active" {
			return errors.New("subject agent must be active and belong to subject.teamID")
		}
	}
	return nil
}
