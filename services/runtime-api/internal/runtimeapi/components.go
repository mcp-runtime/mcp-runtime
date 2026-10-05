package runtimeapi

import (
	"context"
	"net/http"
	"time"
)

// HandleDashboardSummary returns analytics and live control-plane counters for the platform dashboard.
func (s *RuntimeServer) HandleDashboardSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("allow", http.MethodGet)
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// Get analytics data from ClickHouse
	summary, err := s.db.QueryDashboardSummary(ctx)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "failed to query dashboard summary")
		return
	}
	if control := s.controlPlane(); control != nil {
		if result, err := control.ListServers(ctx, ""); err == nil {
			summary.ActiveServers = len(result.Servers)
		}
	}

	// Get grants and sessions counts from Kubernetes if available
	if s.accessMgr != nil {
		grants, err := s.accessMgr.ListGrants(ctx, "")
		if err == nil {
			activeGrants := 0
			for _, g := range grants.Items {
				if !g.Spec.Disabled {
					activeGrants++
				}
			}
			summary.ActiveGrants = activeGrants
		}

		sessions, err := s.accessMgr.ListSessions(ctx, "")
		if err == nil {
			activeSessions := 0
			for _, sess := range sessions.Items {
				if !sess.Spec.Revoked {
					activeSessions++
				}
			}
			summary.ActiveSessions = activeSessions
		}
	}

	writeJSON(w, http.StatusOK, summary)
}

// HandleRuntimeComponents returns admin-only health details for platform components.
func (s *RuntimeServer) HandleRuntimeComponents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("allow", http.MethodGet)
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if p, ok := principalFromContext(r.Context()); !ok || p.Role != roleAdmin {
		writeAPIError(w, http.StatusForbidden, "forbidden")
		return
	}
	if s.stackMgr == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "kubernetes not available")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	statuses, err := s.stackMgr.GetAllComponentStatuses(ctx)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "failed to get component statuses")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"components": statuses})
}
