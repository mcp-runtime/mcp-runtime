package runtimeapi

import (
	"context"
	"net/http"
	"sort"
	"time"

	mcpaccess "mcp-runtime/pkg/access"
	"mcp-runtime/pkg/internalapi"
)

// Personal workspace membership comes from the resolved principal, including
// for admins. ListTeams intentionally has different, administrative semantics.
func (s *RuntimeServer) HandleWorkspaceTeams(w http.ResponseWriter, r *http.Request) {
	p, ok := workspacePrincipal(w, r, false)
	if !ok {
		return
	}
	teams := append([]principalTeam{}, p.Teams...)
	writeJSON(w, http.StatusOK, map[string]any{"teams": teams})
}

type workspaceServer struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Status    string `json:"status"`
	CanManage bool   `json:"can_manage"`
}

func (s *RuntimeServer) HandleWorkspaceServers(w http.ResponseWriter, r *http.Request) {
	p, ok := workspacePrincipal(w, r, false)
	if !ok {
		return
	}
	control := s.controlPlane()
	if control == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "kubernetes not available")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	servers, err := s.Inventory().visibleServers(ctx, control, p, "")
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "workspace servers unavailable")
		return
	}
	out := make([]workspaceServer, 0, len(servers))
	for _, server := range servers {
		out = append(out, workspaceServer{Name: server.Name, Namespace: server.Namespace, Status: server.Status,
			CanManage: principalCanAdministerServerLabels(p, server.Namespace, server.Labels)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	// Ownership labels are mutable authorization metadata, not immutable deployment
	// history. They cannot be presented as a deployment actor.
	writeJSON(w, http.StatusOK, map[string]any{"servers": out, "deployment_attribution": "unavailable"})
}

func (s *RuntimeServer) HandleWorkspaceAccess(w http.ResponseWriter, r *http.Request) {
	p, ok := workspacePrincipal(w, r, false)
	if !ok {
		return
	}
	if s.accessMgr == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "access records unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	grants, err := s.accessMgr.ListGrants(ctx, "")
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "grants unavailable")
		return
	}
	sessions, err := s.accessMgr.ListSessions(ctx, "")
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "sessions unavailable")
		return
	}
	gs := make([]mcpaccess.GrantSummary, 0)
	ss := make([]mcpaccess.SessionSummary, 0)
	for _, grant := range grants.Items {
		if !principalCanReadNamespace(p, grant.Namespace) && p.Role != roleAdmin {
			continue
		}
		subject := grant.Spec.Subject
		// Agent-specific grants belong in the agent detail view. A team-only
		// grant can be applicable to a member without implying personal ownership.
		if subject.AgentID != "" {
			continue
		}
		personal := subject.HumanID != "" && string(subject.HumanID) == p.UserID()
		team := false
		for _, membership := range p.Teams {
			if membership.ID == string(subject.TeamID) {
				team = true
				break
			}
		}
		if (personal && (subject.TeamID == "" || team)) || (subject.HumanID == "" && team) {
			gs = append(gs, mcpaccess.ToGrantSummary(grant))
		}
	}
	for _, session := range sessions.Items {
		if !principalCanReadNamespace(p, session.Namespace) && p.Role != roleAdmin {
			continue
		}
		if p.UserID() != "" && string(session.Spec.Subject.HumanID) == p.UserID() {
			ss = append(ss, mcpaccess.ToSessionSummary(session))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"grants": gs, "sessions": ss})
}

func (s *RuntimeServer) HandleOverviewDirectory(w http.ResponseWriter, r *http.Request) {
	if _, ok := workspacePrincipal(w, r, true); !ok {
		return
	}
	store, ok := s.identity.(interface {
		OverviewCounts(context.Context) (internalapi.OverviewCounts, error)
	})
	if !ok {
		writeAPIError(w, http.StatusServiceUnavailable, "directory counts unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	out, err := store.OverviewCounts(ctx)
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "directory counts unavailable")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *RuntimeServer) HandleOverviewServers(w http.ResponseWriter, r *http.Request) {
	if _, ok := workspacePrincipal(w, r, true); !ok {
		return
	}
	control := s.controlPlane()
	if control == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "kubernetes not available")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	result, err := control.ListServers(ctx, "")
	if err != nil || result.CRDError != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "server counts unavailable")
		return
	}
	failures := make([]workspaceServer, 0)
	for _, server := range result.Servers {
		if server.Status == "Error" || server.Status == "Failed" || server.Status == "Degraded" {
			failures = append(failures, workspaceServer{Name: server.Name, Namespace: server.Namespace, Status: server.Status, CanManage: true})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"servers": len(result.Servers), "failures": failures})
}

func workspacePrincipal(w http.ResponseWriter, r *http.Request, admin bool) (principal, bool) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return principal{}, false
	}
	p, ok := principalFromContext(r.Context())
	if !ok {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized")
		return p, false
	}
	if admin && p.Role != roleAdmin {
		writeAPIError(w, http.StatusForbidden, "forbidden")
		return p, false
	}
	return p, true
}
