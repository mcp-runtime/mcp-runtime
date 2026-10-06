package runtimeapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"mcp-runtime/pkg/platformstack"
)

const actionRestartMaxBytes = 4 * 1024

// HandleActionRestart restarts one or all platform runtime components.
func (s *RuntimeServer) HandleActionRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("allow", "POST")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if s.stackMgr == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "kubernetes not available")
		return
	}

	var req struct {
		Component string `json:"component"`
		All       bool   `json:"all"`
	}

	r.Body = http.MaxBytesReader(w, r.Body, actionRestartMaxBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBodyDecodeError(w, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	if req.All {
		errs := s.stackMgr.RestartAllComponents(ctx)
		if len(errs) > 0 {
			writeAPIError(w, http.StatusInternalServerError, "some components failed to restart", errors.Join(errs...))
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success":   true,
			"restarted": "all",
		})
		return
	}

	if req.Component == "" {
		writeAPIError(w, http.StatusBadRequest, "component required")
		return
	}

	// Validate component exists
	if _, err := platformstack.FindComponent(req.Component); err != nil {
		writeAPIError(w, http.StatusBadRequest, "unknown component")
		return
	}

	if err := s.stackMgr.RestartComponent(ctx, req.Component); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "failed to restart component")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":   true,
		"component": req.Component,
	})
}

// RuntimeServer is now fully wired up through individual handler functions
