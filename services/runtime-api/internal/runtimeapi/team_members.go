package runtimeapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"mcp-runtime/pkg/apihttp"
	"mcp-runtime/pkg/serviceutil"
)

// HandleRuntimeTeamItemPath routes team detail and membership operations after role checks.
func (s *RuntimeServer) HandleRuntimeTeamItemPath(w http.ResponseWriter, r *http.Request) {
	if !s.identityConfigured() {
		writeAPIError(w, http.StatusServiceUnavailable, "platform identity database not configured")
		return
	}
	p, ok := principalFromContext(r.Context())
	if !ok {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	path := strings.Trim(strings.TrimPrefix(serviceutil.NormalizePublicAPIPath(r.URL.Path), "/runtime/teams/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		writeAPIError(w, http.StatusBadRequest, "invalid path")
		return
	}
	teamSlug := NormalizeTeamSlug(parts[0])

	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		s.handleRuntimeTeamGet(w, r, p, teamSlug)
		return
	case len(parts) == 1 && r.Method == http.MethodDelete:
		s.handleRuntimeTeamDelete(w, r, p, teamSlug)
		return
	case len(parts) == 2 && parts[1] == "members" && r.Method == http.MethodGet:
		s.handleRuntimeTeamMemberList(w, r, p, teamSlug)
		return
	case len(parts) == 2 && parts[1] == "members" && r.Method == http.MethodPost:
		s.handleRuntimeTeamMemberUpsertLegacy(w, r, p, teamSlug)
		return
	case len(parts) == 2 && parts[1] == "users" && r.Method == http.MethodPost:
		s.handleRuntimeTeamUserCreate(w, r, p, teamSlug)
		return
	case len(parts) == 2 && parts[1] == "agents":
		s.HandleRuntimeTeamAgents(w, r, p, teamSlug)
		return
	case len(parts) == 3 && parts[1] == "members" && r.Method == http.MethodPut:
		s.handleRuntimeTeamMemberUpsert(w, r, p, teamSlug, strings.TrimSpace(parts[2]))
		return
	case len(parts) == 3 && parts[1] == "members" && r.Method == http.MethodDelete:
		s.handleRuntimeTeamMemberDelete(w, r, p, teamSlug, strings.TrimSpace(parts[2]))
		return
	default:
		w.Header().Set("allow", "GET, POST, PUT, DELETE")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}

func (s *RuntimeServer) handleRuntimeTeamDelete(w http.ResponseWriter, r *http.Request, p principal, teamSlug string) {
	if p.Role != roleAdmin {
		writeAPIError(w, http.StatusForbidden, "forbidden")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	team, ok, err := s.identity.GetTeamBySlug(ctx, teamSlug)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "failed to look up team")
		return
	}
	if !ok {
		writeAPIError(w, http.StatusNotFound, "team not found")
		return
	}
	if err := s.identity.DeleteTeamBySlug(ctx, teamSlug); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAPIError(w, http.StatusNotFound, "team not found")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "failed to delete team")
		return
	}
	// Best-effort: drop the team namespace from Traefik watches so a later
	// namespace delete cannot stall the CRD provider (IngressRoute 404s).
	if deployments := s.Deployments(); deployments != nil && deployments.k8sClients != nil {
		cfg := platformTeamTraefikWatchConfig()
		if err := removeTraefikDeploymentWatchesNamespace(ctx, deployments.k8sClients.Clientset, team.Namespace, cfg); err != nil {
			log.Printf("remove Traefik watch for deleted team %s namespace %s: %v", teamSlug, team.Namespace, err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *RuntimeServer) handleRuntimeTeamMemberList(w http.ResponseWriter, r *http.Request, p principal, teamSlug string) {
	if p.Role != roleAdmin && p.TeamRole(teamSlug) == "" {
		writeAPIError(w, http.StatusForbidden, "forbidden")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	memberships, err := s.identity.ListTeamMemberships(ctx, teamSlug)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "failed to list team members")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": memberships})
}

type teamMemberUpsertRequest struct {
	UserID string `json:"userID"`
	Role   string `json:"role"`
}

func (s *RuntimeServer) handleRuntimeTeamMemberUpsert(w http.ResponseWriter, r *http.Request, p principal, teamSlug, userID string) {
	if p.Role != roleAdmin && p.TeamRole(teamSlug) != teamRoleOwner {
		writeAPIError(w, http.StatusForbidden, "forbidden")
		return
	}
	var req teamMemberUpsertRequest
	r.Body = http.MaxBytesReader(w, r.Body, teamApplyMaxBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBodyDecodeError(w, err)
		return
	}
	if strings.TrimSpace(req.UserID) != "" && strings.TrimSpace(userID) != "" && strings.TrimSpace(req.UserID) != strings.TrimSpace(userID) {
		writeAPIError(w, http.StatusBadRequest, "userID must match the path")
		return
	}
	if strings.TrimSpace(userID) == "" {
		userID = strings.TrimSpace(req.UserID)
	}
	s.handleRuntimeTeamMemberUpsertDecoded(w, r, teamSlug, userID, req.Role)
}

func (s *RuntimeServer) handleRuntimeTeamMemberUpsertDecoded(w http.ResponseWriter, r *http.Request, teamSlug, userID, role string) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	membership, err := s.identity.UpsertTeamMembership(ctx, teamSlug, userID, role)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAPIError(w, http.StatusNotFound, "team or user not found")
			return
		}
		writeIdentityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"membership": membership})
}

func (s *RuntimeServer) handleRuntimeTeamMemberUpsertLegacy(w http.ResponseWriter, r *http.Request, p principal, teamSlug string) {
	var req teamMemberUpsertRequest
	r.Body = http.MaxBytesReader(w, r.Body, teamApplyMaxBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBodyDecodeError(w, err)
		return
	}
	if p.Role != roleAdmin && p.TeamRole(teamSlug) != teamRoleOwner {
		writeAPIError(w, http.StatusForbidden, "forbidden")
		return
	}
	s.handleRuntimeTeamMemberUpsertDecoded(w, r, teamSlug, req.UserID, req.Role)
}

func (s *RuntimeServer) handleRuntimeTeamMemberDelete(w http.ResponseWriter, r *http.Request, p principal, teamSlug, userID string) {
	if p.Role != roleAdmin && p.TeamRole(teamSlug) != teamRoleOwner {
		writeAPIError(w, http.StatusForbidden, "forbidden")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := s.identity.DeleteTeamMembership(ctx, teamSlug, userID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAPIError(w, http.StatusNotFound, "membership not found")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "failed to delete membership")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"team":    teamSlug,
		"userID":  userID,
	})
}

type teamUserCreateRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

// handleRuntimeTeamUserCreate creates a new platform user and immediately adds
// them to the team. Called by the UI "Add team owner/member" form which sends
// { email, password, role } to POST /api/runtime/teams/{slug}/users.
func (s *RuntimeServer) handleRuntimeTeamUserCreate(w http.ResponseWriter, r *http.Request, p principal, teamSlug string) {
	if p.Role != roleAdmin && p.TeamRole(teamSlug) != teamRoleOwner {
		writeAPIError(w, http.StatusForbidden, "forbidden")
		return
	}
	var req teamUserCreateRequest
	r.Body = http.MaxBytesReader(w, r.Body, teamApplyMaxBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBodyDecodeError(w, err)
		return
	}
	req.Email = strings.TrimSpace(req.Email)
	teamRole := strings.TrimSpace(req.Role)
	if req.Email == "" || req.Password == "" {
		writeAPIError(w, http.StatusBadRequest, "email and password are required")
		return
	}
	if teamRole == "" {
		teamRole = teamRoleMember
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	u, membership, err := s.identity.CreateTeamUser(ctx, teamSlug, req.Email, req.Password, teamRole)
	if err != nil {
		diagnostic := "platform identity request failed"
		var upstream *apihttp.Error
		if errors.As(err, &upstream) {
			diagnostic = fmt.Sprintf("status=%d code=%s", upstream.Status, upstream.Code)
			switch upstream.Message {
			case "valid email required", "password must be at least 8 characters", "password must be at most 72 bytes", "membership role must be owner or member", "a user with this email already exists; add them as a member instead", "team not found", "failed to create user":
				diagnostic += " message=" + upstream.Message
			}
		}
		log.Printf("team account creation failed: %s", diagnostic)
		s.writeAudit(r.Context(), auditEvent{UserID: p.Subject, Action: "team_user_create", Resource: teamSlug, Status: "error", Message: diagnostic})
		if errors.Is(err, sql.ErrNoRows) {
			writeAPIError(w, http.StatusNotFound, "team not found")
			return
		}
		writeIdentityError(w, err)
		return
	}
	s.writeAudit(r.Context(), auditEvent{UserID: p.Subject, Action: "team_user_create", Resource: teamSlug, Status: "success"})
	writeJSON(w, http.StatusCreated, map[string]any{"user": u, "membership": membership})
}

func writeIdentityError(w http.ResponseWriter, err error) {
	var upstream *apihttp.Error
	if errors.As(err, &upstream) {
		// Log only status/code: upstream messages may contain submitted user data.
		log.Printf("platform identity request failed: status=%d code=%s", upstream.Status, upstream.Code)
		apihttp.WriteError(w, nil, upstream)
		return
	}
	writeAPIError(w, http.StatusInternalServerError, "platform identity request failed")
}
