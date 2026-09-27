package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"mcp-runtime-api/admin"
	"mcp-runtime-api/deployments"
	"mcp-runtime-api/internal/platformclient"
	"mcp-runtime-api/internal/platforminternal"
	"mcp-runtime-api/internal/runtimeapi"
	runtimehandlers "mcp-runtime-api/runtime"
	"mcp-runtime/pkg/apihttp"
	"mcp-runtime/pkg/openapi"
	"mcp-runtime/pkg/platformauth"
)

func (s *server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/ready", s.handleReady)

	platforminternal.Handler{
		Runtime:  s.runtime,
		Platform: s.platform,
		Token:    strings.TrimSpace(os.Getenv("INTERNAL_AUTH_TOKEN")),
	}.Register(mux)

	auth := s.authentic.Middleware
	adminOnly := func(h http.Handler) http.Handler {
		return auth(s.authentic.RequireRole(platformauth.RoleAdmin, h))
	}

	register := func(pattern string, handler http.Handler) {
		mux.Handle("/api/v1"+pattern, handler)
	}

	register("/openapi.yaml", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		openapi.ServeYAML(w, openAPISpec)
	}))

	if s.runtime == nil {
		return
	}
	routes := runtimeRoutes{
		runtime:   s.runtime,
		platform:  s.platform,
		auth:      auth,
		adminOnly: adminOnly,
		mount:     register,
	}
	routes.registerRoutes(mux)
}

type runtimeRoutes struct {
	runtime   *runtimeapi.RuntimeServer
	platform  *platformclient.Client
	auth      func(http.Handler) http.Handler
	adminOnly func(http.Handler) http.Handler
	mount     func(string, http.Handler)
}

func (rr runtimeRoutes) registerRoutes(mux *http.ServeMux) {
	runtimeServer := rr.runtime
	runtimeServer.SetAuditWriter(rr.platform)
	deploymentService := runtimeServer.Deployments()
	accessService := runtimeServer.Access()
	inventoryService := runtimeServer.Inventory()
	registryPushService := runtimeServer.RegistryPush()

	rr.mount("/dashboard/summary", rr.adminOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleDashboardSummary(runtimeServer, w, r)
	})))
	rr.mount("/runtime/servers", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleRuntimeServers(runtimeServer, w, r)
	})))
	rr.mount("/runtime/tools", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleRuntimeTools(inventoryService, w, r)
	})))
	rr.mount("/runtime/servers/", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleRuntimeServerItem(runtimeServer, w, r)
	})))
	rr.mount("/runtime/server-events", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleRuntimeServerEvents(runtimeServer, w, r)
	})))
	rr.mount("/runtime/observability/links", rr.auth(http.HandlerFunc(runtimeServer.HandleRuntimeObservabilityLinks)))
	rr.mount("/runtime/observability/prometheus/query", rr.auth(http.HandlerFunc(runtimeServer.HandleRuntimeObservabilityPrometheusQuery)))
	rr.mount("/runtime/teams", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleRuntimeTeams(runtimeServer, w, r)
	})))
	rr.mount("/runtime/teams/", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleRuntimeTeamItemPath(runtimeServer, w, r)
	})))
	rr.mount("/runtime/agents/", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimeServer.HandleRuntimeAgentPath(w, r)
	})))
	rr.mount("/runtime/namespaces", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleRuntimeNamespaces(runtimeServer, w, r)
	})))
	rr.mount("/runtime/namespaces/", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleRuntimeNamespaceItem(runtimeServer, w, r)
	})))
	rr.mount("/deployments", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deployments.HandleDeployments(deploymentService, w, r)
	})))
	rr.mount("/deployments/", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deployments.HandleDeploymentItem(deploymentService, w, r)
	})))
	rr.mount("/admin/operations", rr.adminOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		admin.HandleOperations(w, r, admin.Dependencies{
			Platform:  rr.platform,
			Runtime:   runtimeServer,
			WriteJSON: apihttp.WriteJSON,
		})
	})))
	rr.mount("/admin/deployments", rr.adminOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deployments.HandleAdminDeployments(deploymentService, w, r)
	})))
	rr.mount("/runtime/grants", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleRuntimeGrants(accessService, w, r)
	})))
	rr.mount("/runtime/sessions", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleRuntimeSessions(accessService, w, r)
	})))
	rr.mount("/runtime/adapter/sessions", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleAdapterSession(accessService, w, r)
	})))
	rr.mount("/runtime/adapter/certificates", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleAdapterCertificate(accessService, w, r)
	})))
	rr.mount("/runtime/registry/push", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleRuntimeRegistryPush(registryPushService, w, r)
	})))
	mux.HandleFunc("/internal/registry-push/tar", registryPushService.HandleRegistryPushTransfer)
	go deploymentService.ReconcileTeamNamespaceNetworkPolicies(context.Background())
	rr.mount("/runtime/components", rr.adminOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleRuntimeComponents(runtimeServer, w, r)
	})))
	rr.mount("/runtime/policy", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleRuntimePolicy(accessService, w, r)
	})))
	rr.mount("/runtime/actions/restart", rr.adminOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleActionRestart(runtimeServer, w, r)
	})))
	rr.mount("/runtime/grants/", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleGrantItemPath(accessService, w, r)
	})))
	rr.mount("/runtime/sessions/", rr.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimehandlers.HandleSessionItemPath(accessService, w, r)
	})))
	rr.mount("/user/api-keys", rr.auth(http.HandlerFunc(rr.handleUserAPIKeys)))
	rr.mount("/user/api-keys/", rr.auth(http.HandlerFunc(rr.handleUserAPIKeyItem)))
}

func (s *server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	if s.runtimeInit != "" {
		apihttp.WriteJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ok":                  false,
			"runtime_initialized": false,
			"runtime_error":       s.runtimeInit,
		})
		return
	}
	apihttp.WriteJSON(w, http.StatusOK, map[string]any{
		"ok":                  true,
		"runtime_initialized": true,
	})
}

func (s *server) handleReady(w http.ResponseWriter, _ *http.Request) {
	if s.runtimeInit != "" {
		apihttp.WriteEnvelope(w, http.StatusServiceUnavailable, apihttp.CodeServiceUnavailable, s.runtimeInit)
		return
	}
	if s.runtime == nil || !s.runtime.KubernetesAvailable() {
		apihttp.WriteEnvelope(w, http.StatusServiceUnavailable, apihttp.CodeServiceUnavailable, "kubernetes not available")
		return
	}
	apihttp.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (rr runtimeRoutes) handleUserAPIKeys(w http.ResponseWriter, r *http.Request) {
	p, ok := platformauth.FromContext(r.Context())
	if !ok || p.UserID() == "" {
		apihttp.WriteEnvelope(w, http.StatusUnauthorized, apihttp.CodeUnauthorized, "unauthorized")
		return
	}
	switch r.Method {
	case http.MethodGet:
		keys, err := rr.runtime.ListUserAPIKeys(r.Context(), p.UserID())
		if err != nil {
			apihttp.WriteEnvelope(w, http.StatusInternalServerError, apihttp.CodeQueryFailed, "failed to list user api keys")
			return
		}
		apihttp.WriteJSON(w, http.StatusOK, map[string]any{"keys": keys})
	case http.MethodPost:
		var req struct {
			Name string `json:"name"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 8*1024)
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			apihttp.WriteEnvelope(w, http.StatusBadRequest, apihttp.CodeInvalidRequestBody, "invalid request body")
			return
		}
		key, cleartext, err := rr.runtime.CreateUserAPIKey(r.Context(), p.UserID(), req.Name)
		if err != nil {
			rr.platform.WriteAudit(r.Context(), platformAuditEvent(p, "api_key_create", strings.TrimSpace(req.Name), "error", err.Error(), r))
			apihttp.WriteEnvelope(w, http.StatusBadRequest, apihttp.CodeInvalidRequestBody, err.Error())
			return
		}
		rr.platform.WriteAudit(r.Context(), platformAuditEvent(p, "api_key_create", key.ID, "success", "", r))
		apihttp.WriteJSON(w, http.StatusOK, map[string]any{"key": key, "api_key": cleartext, "one_time_key": cleartext})
	default:
		w.Header().Set("allow", "GET, POST")
		apihttp.WriteEnvelope(w, http.StatusMethodNotAllowed, apihttp.CodeMethodNotAllowed, "method not allowed")
	}
}

func (rr runtimeRoutes) handleUserAPIKeyItem(w http.ResponseWriter, r *http.Request) {
	p, ok := platformauth.FromContext(r.Context())
	if !ok || p.UserID() == "" {
		apihttp.WriteEnvelope(w, http.StatusUnauthorized, apihttp.CodeUnauthorized, "unauthorized")
		return
	}
	keyID, allowed, valid := parseUserAPIKeyItemPath(r.Method, r.URL.Path)
	if !allowed {
		w.Header().Set("allow", "DELETE, POST")
		apihttp.WriteEnvelope(w, http.StatusMethodNotAllowed, apihttp.CodeMethodNotAllowed, "method not allowed")
		return
	}
	if !valid {
		apihttp.WriteEnvelope(w, http.StatusBadRequest, apihttp.CodeInvalidRequestBody, "invalid key path")
		return
	}
	key, err := rr.runtime.RevokeUserAPIKey(r.Context(), p.UserID(), keyID)
	if err != nil {
		rr.platform.WriteAudit(r.Context(), platformAuditEvent(p, "api_key_revoke", keyID, "error", err.Error(), r))
		if apierrors.IsNotFound(err) {
			apihttp.WriteEnvelope(w, http.StatusNotFound, apihttp.CodeNotFound, "key not found")
			return
		}
		apihttp.WriteEnvelope(w, http.StatusInternalServerError, apihttp.CodeQueryFailed, "failed to revoke key")
		return
	}
	rr.platform.WriteAudit(r.Context(), platformAuditEvent(p, "api_key_revoke", key.ID, "success", "", r))
	apihttp.WriteJSON(w, http.StatusOK, map[string]any{"key": key})
}

func platformAuditEvent(p platformauth.Principal, action, resource, status, message string, r *http.Request) platformclient.AuditEvent {
	return platformclient.AuditEvent{
		UserID:       p.UserID(),
		Action:       action,
		Resource:     resource,
		Namespace:    p.Namespace,
		Status:       status,
		Message:      message,
		ActorIP:      platformauth.RequestIP(r),
		Source:       platformauth.AuditSource(r, p),
		AuthIdentity: platformauth.AuditIdentityLabel(p),
	}
}

func parseUserAPIKeyItemPath(method, path string) (keyID string, allowed bool, valid bool) {
	const prefix = "/api/v1/user/api-keys/"
	if strings.HasPrefix(path, prefix) {
		path = strings.TrimPrefix(path, prefix)
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch method {
	case http.MethodDelete:
		if len(parts) != 1 || parts[0] == "" {
			return "", true, false
		}
		return parts[0], true, true
	case http.MethodPost:
		if len(parts) != 2 || parts[0] == "" || parts[1] != "revoke" {
			return "", true, false
		}
		return parts[0], true, true
	default:
		return "", false, false
	}
}
