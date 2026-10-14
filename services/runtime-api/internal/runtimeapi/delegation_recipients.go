package runtimeapi

import (
	"context"
	"encoding/base64"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"
)

type delegationTeam struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}
type delegationAgent struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	TeamID   string `json:"team_id"`
	TeamSlug string `json:"team_slug"`
}
type delegationMember struct {
	UserID string `json:"user_id"`
}
type delegationRecipients struct {
	Team             delegationTeam     `json:"team"`
	Agents           []delegationAgent  `json:"agents"`
	Members          []delegationMember `json:"members"`
	AgentNextCursor  string             `json:"agent_next_cursor,omitempty"`
	MemberNextCursor string             `json:"member_next_cursor,omitempty"`
}

// HandleDelegationRecipients resolves one explicitly named recipient team for
// a resource-namespace manager. It never grants access to global team directory
// listing, membership management, or private recipient account metadata.
func (s *RuntimeServer) HandleDelegationRecipients(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	p, ok := principalFromContext(r.Context())
	if !ok {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	q := r.URL.Query()
	namespace := strings.TrimSpace(q.Get("namespace"))
	if namespace == "" || len(validation.IsDNS1123Label(namespace)) != 0 {
		writeAPIError(w, http.StatusBadRequest, "valid resource namespace is required")
		return
	}
	if !principalCanAdministerServerLabels(p, namespace, nil) {
		writeAPIError(w, http.StatusForbidden, "resource namespace management is required")
		return
	}
	teamSlug := strings.TrimSpace(q.Get("team"))
	if teamSlug == "" || len(validation.IsDNS1123Label(teamSlug)) != 0 {
		writeAPIError(w, http.StatusBadRequest, "one exact recipient team slug is required")
		return
	}
	limit := 50
	if raw := q.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeAPIError(w, http.StatusBadRequest, "limit must be between 1 and 100")
			return
		}
		limit = parsed
	}
	agentCursor := q.Get("agent_cursor")
	if len(agentCursor) > 1024 || len(q.Get("member_cursor")) > 512 {
		writeAPIError(w, http.StatusBadRequest, "invalid recipient cursor")
		return
	}
	memberAfter := ""
	if cursor := q.Get("member_cursor"); cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || len(decoded) == 0 || len(decoded) > 256 {
			writeAPIError(w, http.StatusBadRequest, "invalid member cursor")
			return
		}
		memberAfter = string(decoded)
	}
	if s.identity == nil || !s.identity.Configured() {
		writeAPIError(w, http.StatusServiceUnavailable, "recipient directory is unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	team, found, err := s.identity.GetTeamBySlug(ctx, teamSlug)
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "recipient directory is unavailable")
		return
	}
	if !found {
		writeAPIError(w, http.StatusNotFound, "recipient team not found")
		return
	}
	page, err := s.identity.ListAgents(ctx, teamSlug, "active", "", agentCursor, limit)
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "recipient agent directory is unavailable")
		return
	}
	memberships, err := s.identity.ListTeamMemberships(ctx, teamSlug)
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "recipient membership directory is unavailable")
		return
	}
	result := delegationRecipients{Team: delegationTeam{ID: team.ID, Slug: team.Slug, Name: team.Name}, Agents: []delegationAgent{}, Members: []delegationMember{}, AgentNextCursor: page.NextCursor}
	for _, agent := range page.Agents {
		if agent.Status == "active" && agent.TeamID == team.ID && len(result.Agents) < limit {
			result.Agents = append(result.Agents, delegationAgent{ID: agent.ID, Name: agent.Name, Status: agent.Status, TeamID: agent.TeamID, TeamSlug: agent.TeamSlug})
		}
	}
	seen := map[string]bool{}
	for _, member := range memberships {
		if member.UserID == "" || seen[member.UserID] || (member.TeamID != "" && member.TeamID != team.ID) {
			continue
		}
		seen[member.UserID] = true
		result.Members = append(result.Members, delegationMember{UserID: member.UserID})
	}
	sort.Slice(result.Members, func(i, j int) bool { return result.Members[i].UserID < result.Members[j].UserID })
	start := sort.Search(len(result.Members), func(i int) bool { return result.Members[i].UserID > memberAfter })
	result.Members = result.Members[start:]
	if len(result.Members) > limit {
		result.Members = result.Members[:limit]
		result.MemberNextCursor = base64.RawURLEncoding.EncodeToString([]byte(result.Members[len(result.Members)-1].UserID))
	}
	writeJSON(w, http.StatusOK, result)
}
