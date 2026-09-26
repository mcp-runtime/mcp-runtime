package runtimeapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

var errAgentDirectoryUnavailable = errors.New("agent directory is unavailable")
var errAgentNotActive = errors.New("agent is unknown, inactive, or belongs to another team")

var platformAgentIDPattern = regexp.MustCompile(`^agt_[0-7][0-9a-hjkmnp-tv-z]{25}$`)

// requireActiveAgent makes the platform directory authoritative anywhere a
// runtime identity containing an agent ID is accepted. Only generated,
// directory-owned IDs are valid.
func requireActiveAgent(ctx context.Context, store identityStore, agentID, teamID string) error {
	agentID, teamID = strings.TrimSpace(agentID), strings.TrimSpace(teamID)
	if agentID == "" {
		return nil
	}
	if !platformAgentIDPattern.MatchString(agentID) || teamID == "" {
		return errAgentNotActive
	}
	if store == nil || !store.Configured() {
		return errAgentDirectoryUnavailable
	}
	agent, found, err := store.GetAgent(ctx, agentID)
	if err != nil {
		return fmt.Errorf("%w: %v", errAgentDirectoryUnavailable, err)
	}
	if !found {
		return errAgentNotActive
	}
	if agent.ID != agentID || agent.Status != "active" || agent.TeamID != teamID {
		return errAgentNotActive
	}
	return nil
}

func writeAgentDirectoryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errAgentDirectoryUnavailable):
		writeAPIError(w, http.StatusServiceUnavailable, "agent directory is unavailable")
	case errors.Is(err, errAgentNotActive):
		writeAPIError(w, http.StatusUnprocessableEntity, "agent is unknown, inactive, or outside the subject team")
	default:
		writeAPIError(w, http.StatusInternalServerError, "agent directory check failed")
	}
}
