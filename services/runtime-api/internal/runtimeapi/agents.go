package runtimeapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"mcp-runtime-api/internal/platformclient"
	"mcp-runtime/pkg/serviceutil"
)

type agentCreateRequest struct {
	Name string `json:"name"`
}
type agentRenameRequest struct {
	Name string `json:"name"`
}

// HandleRuntimeTeamAgents lists and creates governance agents under a team.
func (s *RuntimeServer) HandleRuntimeTeamAgents(w http.ResponseWriter, r *http.Request, p principal, teamSlug string) {
	if r.Method == http.MethodGet {
		if p.Role != roleAdmin && p.TeamRole(teamSlug) == "" {
			writeAPIError(w, http.StatusForbidden, "forbidden")
			return
		}
		status := strings.TrimSpace(r.URL.Query().Get("status"))
		if status != "" && status != "active" && status != "inactive" {
			writeAPIError(w, http.StatusBadRequest, "status must be active or inactive")
			return
		}
		if len([]rune(r.URL.Query().Get("q"))) > 128 {
			writeAPIError(w, http.StatusBadRequest, "q must be 128 characters or fewer")
			return
		}
		limit := 50
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > 200 {
				writeAPIError(w, http.StatusBadRequest, "limit must be between 1 and 200")
				return
			}
			limit = parsed
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		page, err := s.identity.ListAgents(ctx, teamSlug, status, r.URL.Query().Get("q"), r.URL.Query().Get("cursor"), limit)
		if errors.Is(err, platformclient.ErrTeamNotFound) || errors.Is(err, sql.ErrNoRows) {
			writeAPIError(w, http.StatusNotFound, "team not found")
			return
		}
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "failed to list agents")
			return
		}
		writeJSON(w, http.StatusOK, page)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("allow", "GET, POST")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if p.Role != roleAdmin && p.TeamRole(teamSlug) != teamRoleOwner {
		writeAPIError(w, http.StatusForbidden, "forbidden")
		return
	}
	var req agentCreateRequest
	r.Body = http.MaxBytesReader(w, r.Body, teamApplyMaxBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBodyDecodeError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	item, err := s.identity.CreateAgent(ctx, teamSlug, req.Name, p.UserID())
	if err != nil {
		writeAgentError(w, err)
		return
	}
	s.auditAgent(r, p, item, "agent.created")
	writeJSON(w, http.StatusCreated, map[string]any{"agent": item})
}

// HandleRuntimeAgentPath reads, renames, deactivates, or reactivates one agent.
func (s *RuntimeServer) HandleRuntimeAgentPath(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(serviceutil.NormalizePublicAPIPath(r.URL.Path), "/runtime/agents/"), "/")
	if !s.identityConfigured() {
		writeAPIError(w, http.StatusServiceUnavailable, "platform identity database not configured")
		return
	}
	p, ok := principalFromContext(r.Context())
	if !ok {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	parts := strings.Split(path, "/")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		writeAPIError(w, http.StatusBadRequest, "invalid agent ID")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	item, found, err := s.identity.GetAgent(ctx, parts[0])
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "failed to get agent")
		return
	}
	if !found {
		writeAPIError(w, http.StatusNotFound, "agent not found")
		return
	}
	teamSlug := NormalizeTeamSlug(item.TeamSlug)
	if p.Role != roleAdmin && p.TeamRole(teamSlug) == "" {
		writeAPIError(w, http.StatusForbidden, "forbidden")
		return
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{"agent": item})
		return
	}
	if p.Role != roleAdmin && p.TeamRole(teamSlug) != teamRoleOwner {
		writeAPIError(w, http.StatusForbidden, "forbidden")
		return
	}
	if len(parts) == 1 && r.Method == http.MethodPatch {
		var req agentRenameRequest
		r.Body = http.MaxBytesReader(w, r.Body, teamApplyMaxBytes)
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeBodyDecodeError(w, err)
			return
		}
		updated, err := s.identity.RenameAgent(ctx, item.ID, req.Name)
		if err != nil {
			writeAgentError(w, err)
			return
		}
		updated.TeamSlug = item.TeamSlug
		s.auditAgent(r, p, updated, "agent.renamed")
		writeJSON(w, http.StatusOK, map[string]any{"agent": updated})
		return
	}
	if len(parts) == 2 && r.Method == http.MethodPost && (parts[1] == "deactivate" || parts[1] == "reactivate") {
		status := "active"
		action := "agent.reactivated"
		if parts[1] == "deactivate" {
			status, action = "inactive", "agent.deactivated"
		}
		if parts[1] == "deactivate" {
			updated, err := s.identity.SetAgentStatus(ctx, item.ID, status, p.UserID())
			if err != nil {
				writeAgentError(w, err)
				return
			}
			if err := s.revokeAgentSessions(ctx, r, p, item); err != nil {
				writeAPIError(w, http.StatusInternalServerError, "agent is inactive but session revocation is incomplete; retry deactivation")
				return
			}
			updated.TeamSlug = item.TeamSlug
			s.auditAgent(r, p, updated, action)
			writeJSON(w, http.StatusOK, map[string]any{"agent": updated})
			return
		}
		updated, err := s.identity.SetAgentStatus(ctx, item.ID, status, p.UserID())
		if err != nil {
			writeAgentError(w, err)
			return
		}
		updated.TeamSlug = item.TeamSlug
		s.auditAgent(r, p, updated, action)
		writeJSON(w, http.StatusOK, map[string]any{"agent": updated})
		return
	}
	w.Header().Set("allow", "GET, PATCH, POST")
	writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed")
}

func (s *RuntimeServer) revokeAgentSessions(ctx context.Context, r *http.Request, p principal, item platformclient.Agent) error {
	if s.accessMgr == nil {
		return errors.New("kubernetes unavailable")
	}
	sessions, err := s.accessMgr.ListSessions(ctx, "")
	if err != nil {
		return err
	}
	for _, session := range sessions.Items {
		if string(session.Spec.Subject.AgentID) != item.ID || string(session.Spec.Subject.TeamID) != item.TeamID || session.Spec.Revoked {
			continue
		}
		if err := s.accessMgr.RevokeSession(ctx, session.Name, session.Namespace); err != nil {
			return err
		}
		message, _ := json.Marshal(map[string]string{"actor_id": p.UserID(), "agent_id": item.ID, "team_id": item.TeamID, "session_id": session.Name, "namespace": session.Namespace, "action": "agent.session_revoked"})
		if s.audit != nil {
			s.audit.WriteAudit(ctx, auditEvent{UserID: p.UserID(), Action: "agent.session_revoked", Resource: "session", Namespace: session.Namespace, Status: "success", Message: string(message), AgentID: item.ID, TeamID: item.TeamID, ActorIP: requestIP(r), RequestID: strings.TrimSpace(r.Header.Get("X-Request-ID")), Source: auditSource(r, p), AuthIdentity: auditIdentityLabel(p)})
		}
	}
	return nil
}

func (s *RuntimeServer) auditAgent(r *http.Request, p principal, item platformclient.Agent, action string) {
	message, _ := json.Marshal(map[string]string{"actor_id": p.UserID(), "agent_id": item.ID, "team_id": item.TeamID, "action": action})
	s.writeAudit(r.Context(), auditEvent{UserID: p.UserID(), Action: action, Resource: "agent", Status: "success",
		Message: string(message), AgentID: item.ID, TeamID: item.TeamID, ActorIP: requestIP(r),
		RequestID: strings.TrimSpace(r.Header.Get("X-Request-ID")), Source: auditSource(r, p), AuthIdentity: auditIdentityLabel(p)})
}

func writeAgentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, platformclient.ErrAgentNotFound):
		writeAPIError(w, http.StatusNotFound, "agent not found")
	case errors.Is(err, platformclient.ErrTeamNotFound):
		writeAPIError(w, http.StatusNotFound, "team not found")
	case errors.Is(err, platformclient.ErrAgentNameTaken):
		writeAPIError(w, http.StatusConflict, "an agent with this name already exists in the team")
	case errors.Is(err, platformclient.ErrInvalidAgentName):
		writeAPIError(w, http.StatusBadRequest, "agent name must contain 1 to 64 characters")
	default:
		writeAPIError(w, http.StatusInternalServerError, "agent operation failed")
	}
}
