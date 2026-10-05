package runtimeapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtimeaccess "mcp-runtime-api/internal/runtimeapi/access"
	mcpaccess "mcp-runtime/pkg/access"
	"mcp-runtime/pkg/k8sclient"
	"mcp-runtime/pkg/serviceutil"
)

type accessSessionRequest struct {
	Name           string                    `json:"name"`
	Namespace      string                    `json:"namespace"`
	ServerRef      mcpaccess.ServerReference `json:"serverRef"`
	Subject        mcpaccess.SubjectRef      `json:"subject"`
	ConsentedTrust mcpaccess.TrustLevel      `json:"consentedTrust"`
	ExpiresAt      *metav1.Time              `json:"expiresAt"`
	Revoked        *bool                     `json:"revoked,omitempty"`
	PolicyVersion  string                    `json:"policyVersion"`
	GrantName      string                    `json:"grantName,omitempty"`
}

type accessSessionPatchRequest struct {
	Revoked *bool `json:"revoked,omitempty"`
}

// HandleRuntimeSessions lists and applies MCPAgentSession resources within the caller's allowed namespace scope.
func (s *AccessService) HandleRuntimeSessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleRuntimeSessionList(w, r)
	case http.MethodPost:
		s.handleRuntimeSessionApply(w, r)
	default:
		w.Header().Set("allow", "GET, POST")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}

// HandleSessionItemPath handles GET, DELETE, PATCH, and legacy revoke or unrevoke actions for one MCPAgentSession.
func (s *AccessService) HandleSessionItemPath(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		ns, name, err := runtimeaccess.ExtractNamespacedPath(r.URL.Path, "/api/runtime/sessions/", 2)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.handleSessionGet(w, r, ns, name)
		return
	case http.MethodDelete:
		ns, name, err := serviceutil.ExtractNamespacedResourceDelete(r, "/api/runtime/sessions/")
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.handleSessionDelete(w, r, ns, name)
		return
	case http.MethodPatch:
		ns, name, err := runtimeaccess.ExtractNamespacedPath(r.URL.Path, "/api/runtime/sessions/", 2)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.handleSessionPatch(w, r, ns, name)
		return
	case http.MethodPost:
		s.handleSessionPostTogglePath(w, r)
		return
	default:
		w.Header().Set("allow", "GET, POST, PATCH, DELETE")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}

func (s *AccessService) handleSessionGet(w http.ResponseWriter, r *http.Request, namespace, name string) {
	if s.accessMgr == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "kubernetes not available")
		return
	}
	namespace, err := s.scopedNamespaceForPrincipal(r.Context(), namespace)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	session, err := s.accessMgr.GetSession(ctx, name, namespace)
	if err != nil {
		code, msg := k8sclient.HTTPStatusFromK8sError(err)
		writeAPIError(w, code, msg)
		return
	}
	if !s.sessionVisibleToPrincipal(ctx, *session) {
		writeAPIError(w, http.StatusForbidden, "forbidden server")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": mcpaccess.ToSessionSummary(*session)})
}

func (s *AccessService) handleSessionDelete(w http.ResponseWriter, r *http.Request, namespace, name string) {
	if s.accessMgr == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "kubernetes not available")
		return
	}
	namespace, err := s.scopedAccessWriteNamespaceForPrincipal(r.Context(), namespace)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	session, err := s.accessMgr.GetSession(ctx, name, namespace)
	if err != nil {
		code, msg := k8sclient.HTTPStatusFromK8sError(err)
		log.Printf("delete session %s/%s failed before authz (status=%d): %v", namespace, name, code, err)
		writeAPIError(w, code, fmt.Sprintf("failed to delete session: %s", msg))
		return
	}
	allowed, err := s.canAdministerAccessServerRef(ctx, namespace, session.Spec.ServerRef)
	if err != nil {
		log.Printf("delete session %s/%s authz lookup failed: %v", namespace, name, err)
		writeAPIError(w, http.StatusInternalServerError, "failed to verify server reference")
		return
	}
	if !allowed {
		writeAPIError(w, http.StatusForbidden, "forbidden server")
		return
	}
	if err := s.accessMgr.DeleteSession(ctx, name, namespace); err != nil {
		code, msg := k8sclient.HTTPStatusFromK8sError(err)
		log.Printf("delete session %s/%s failed (status=%d): %v", namespace, name, code, err)
		writeAPIError(w, code, fmt.Sprintf("failed to delete session: %s", msg))
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":   true,
		"name":      name,
		"namespace": namespace,
	})
}

func (s *AccessService) handleSessionPatch(w http.ResponseWriter, r *http.Request, namespace, name string) {
	if s.accessMgr == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "kubernetes not available")
		return
	}

	namespace, nsErr := s.scopedAccessWriteNamespaceForPrincipal(r.Context(), namespace)
	if nsErr != nil {
		writeAPIError(w, http.StatusForbidden, nsErr.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	session, err := s.accessMgr.GetSession(ctx, name, namespace)
	if err != nil {
		code, msg := k8sclient.HTTPStatusFromK8sError(err)
		writeAPIError(w, code, msg)
		return
	}
	allowed, err := s.canAdministerAccessServerRef(ctx, namespace, session.Spec.ServerRef)
	if err != nil {
		log.Printf("patch session %s/%s authz lookup failed: %v", namespace, name, err)
		writeAPIError(w, http.StatusInternalServerError, "failed to verify server reference")
		return
	}
	if !allowed {
		writeAPIError(w, http.StatusForbidden, "forbidden server")
		return
	}

	var req accessSessionPatchRequest
	r.Body = http.MaxBytesReader(w, r.Body, accessApplyMaxBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBodyDecodeError(w, err)
		return
	}
	if req.Revoked == nil {
		writeAPIError(w, http.StatusBadRequest, "revoked is required")
		return
	}
	s.handleSessionStateChange(w, ctx, namespace, name, *req.Revoked)
}

// handleSessionPostTogglePath handles legacy POST /api/runtime/sessions/{namespace}/{name}/revoke|unrevoke.
func (s *AccessService) handleSessionPostTogglePath(w http.ResponseWriter, r *http.Request) {
	params, err := serviceutil.ExtractSessionActionParams(r, "/api/runtime/sessions/")
	if err != nil {
		if errors.Is(err, serviceutil.ErrMethodNotAllowed) {
			writeAPIError(w, http.StatusMethodNotAllowed, err.Error())
		} else {
			writeAPIError(w, http.StatusBadRequest, err.Error())
		}
		return
	}

	revoke := !serviceutil.IsActionEnabled(params.Action)
	s.handleSessionToggle(w, r, params.Namespace, params.Name, revoke)
}

func (s *AccessService) handleSessionToggle(w http.ResponseWriter, r *http.Request, namespace, name string, revoke bool) {
	if s.accessMgr == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "kubernetes not available")
		return
	}

	namespace, nsErr := s.scopedAccessWriteNamespaceForPrincipal(r.Context(), namespace)
	if nsErr != nil {
		writeAPIError(w, http.StatusForbidden, nsErr.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	session, err := s.accessMgr.GetSession(ctx, name, namespace)
	if err != nil {
		code, msg := k8sclient.HTTPStatusFromK8sError(err)
		writeAPIError(w, code, msg)
		return
	}
	allowed, err := s.canAdministerAccessServerRef(ctx, namespace, session.Spec.ServerRef)
	if err != nil {
		log.Printf("toggle session %s/%s authz lookup failed: %v", namespace, name, err)
		writeAPIError(w, http.StatusInternalServerError, "failed to verify server reference")
		return
	}
	if !allowed {
		writeAPIError(w, http.StatusForbidden, "forbidden server")
		return
	}

	s.handleSessionStateChange(w, ctx, namespace, name, revoke)
}

func (s *AccessService) handleSessionStateChange(w http.ResponseWriter, ctx context.Context, namespace, name string, revoke bool) {
	var updateErr error
	if revoke {
		updateErr = s.accessMgr.RevokeSession(ctx, name, namespace)
	} else {
		updateErr = s.accessMgr.UnrevokeSession(ctx, name, namespace)
	}
	if updateErr != nil {
		writeAPIError(w, http.StatusInternalServerError, "failed to update session")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":   true,
		"name":      name,
		"namespace": namespace,
		"revoked":   revoke,
	})
}
