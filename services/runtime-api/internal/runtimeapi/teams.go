package runtimeapi

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"
)

// HandleRuntimeTeams lists visible teams and lets admins create team identity records.
func (s *RuntimeServer) HandleRuntimeTeams(w http.ResponseWriter, r *http.Request) {
	if !s.identityConfigured() {
		writeAPIError(w, http.StatusServiceUnavailable, "platform identity database not configured")
		return
	}
	p, ok := principalFromContext(r.Context())
	if !ok {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.handleRuntimeTeamList(w, r, p)
	case http.MethodPost:
		if p.Role != roleAdmin {
			writeAPIError(w, http.StatusForbidden, "forbidden")
			return
		}
		s.handleRuntimeTeamCreate(w, r, p)
	default:
		w.Header().Set("allow", "GET, POST")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}

func (s *RuntimeServer) handleRuntimeTeamList(w http.ResponseWriter, r *http.Request, p principal) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	if p.Role == roleAdmin {
		teams, err := s.identity.ListTeams(ctx)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "failed to list teams")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"teams": teams})
		return
	}
	out := make([]map[string]any, 0, len(p.Teams))
	for _, membership := range p.Teams {
		out = append(out, map[string]any{
			"id": membership.ID, "slug": membership.Slug,
			"name": membership.Name, "namespace": membership.Namespace,
			"role": membership.Role,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"teams": out})
}

func (s *RuntimeServer) handleRuntimeTeamGet(w http.ResponseWriter, r *http.Request, p principal, teamSlug string) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	team, ok, err := s.identity.GetTeamBySlug(ctx, teamSlug)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "failed to fetch team")
		return
	}
	if !ok {
		writeAPIError(w, http.StatusNotFound, "team not found")
		return
	}
	if p.Role != roleAdmin && p.TeamRole(teamSlug) == "" {
		writeAPIError(w, http.StatusForbidden, "forbidden")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"team": team})
}

func (s *RuntimeServer) handleRuntimeTeamCreate(w http.ResponseWriter, r *http.Request, p principal) {
	var req struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, teamApplyMaxBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBodyDecodeError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	team, err := s.identity.CreateTeam(ctx, req.Slug, req.Name, p.UserID())
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate") || strings.Contains(strings.ToLower(err.Error()), "unique") {
			writeAPIError(w, http.StatusConflict, "team already exists")
			return
		}
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Deployments().ensureTeamNamespace(ctx, team); err != nil {
		log.Printf("ensureTeamNamespace for team %s namespace %s failed: %v", team.Slug, team.Namespace, err)
		if cleanupErr := s.identity.DeleteTeamBySlug(ctx, team.Slug); cleanupErr != nil {
			log.Printf("failed to clean up team %s after namespace provisioning failure: %v", team.Slug, cleanupErr)
			writeAPIError(w, http.StatusInternalServerError, "failed to provision team namespace")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "failed to provision team namespace")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"team": team})
}
