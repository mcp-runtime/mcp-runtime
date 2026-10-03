package main

import (
	"net/http"

	"mcp-platform-api/admin"
	"mcp-platform-api/identity"
	"mcp-platform-api/internal/platforminternal"
	"mcp-platform-api/registry"
	"mcp-runtime/pkg/apihttp"
	"mcp-runtime/pkg/openapi"
	"mcp-runtime/pkg/platformauth"
)

func (s *apiServer) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/ready", s.handleReady)

	platforminternal.Handler{
		Store:               s.platform,
		Token:               s.internalAuthToken,
		AuthenticateRequest: s.authenticateRequest,
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

	register("/registry/authz", http.HandlerFunc(s.handleRegistryAuthz))
	register("/registry/token", http.HandlerFunc(s.handleRegistryToken))
	register("/registry/pull-credentials", adminOnly(http.HandlerFunc(s.handleRegistryPullCredentials)))
	register("/auth/login", http.HandlerFunc(s.handleLogin))
	register("/auth/oidc", http.HandlerFunc(s.handleOIDCLogin))
	routes := platformRoutes{
		platform:            s.platform,
		authenticateRequest: s.authenticateRequest,
		auth:                auth,
		adminOnly:           adminOnly,
		mount:               register,
	}
	routes.register()
}

type platformRoutes struct {
	platform            *platformStore
	authenticateRequest func(*http.Request) (principal, bool, error)
	auth                func(http.Handler) http.Handler
	adminOnly           func(http.Handler) http.Handler
	mount               func(string, http.Handler)
}

func (pr platformRoutes) register() {
	pr.mount("/auth/signup", http.HandlerFunc(pr.handleSignup))
	pr.mount("/users", pr.adminOnly(http.HandlerFunc(pr.handleUsers)))
	pr.mount("/auth/me", pr.auth(http.HandlerFunc(pr.handleAuthMe)))
	pr.mount("/user/registry-credentials", pr.auth(http.HandlerFunc(pr.handleRegistryCredentials)))
	pr.mount("/user/registry-credentials/", pr.auth(http.HandlerFunc(pr.handleRegistryCredentialItem)))
	pr.mount("/user/activity/image-publish", pr.auth(http.HandlerFunc(pr.handleUserImagePublishActivity)))
	pr.mount("/admin/namespaces", pr.adminOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		admin.HandleNamespaces(w, r, admin.Dependencies{Platform: pr.platform, WriteJSON: writeJSON})
	})))
	pr.mount("/admin/audit", pr.adminOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		admin.HandleAudit(w, r, admin.Dependencies{Platform: pr.platform, WriteJSON: writeJSON})
	})))
}

func (s *apiServer) handleHealth(w http.ResponseWriter, _ *http.Request) {
	apihttp.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *apiServer) handleReady(w http.ResponseWriter, r *http.Request) {
	if s.platform == nil {
		apihttp.WriteEnvelope(w, http.StatusServiceUnavailable, apihttp.CodeServiceUnavailable, "postgres not configured")
		return
	}
	if err := s.platform.Ping(r.Context()); err != nil {
		apihttp.WriteEnvelope(w, http.StatusServiceUnavailable, apihttp.CodeServiceUnavailable, "postgres unavailable")
		return
	}
	apihttp.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (pr platformRoutes) handleRegistryCredentials(w http.ResponseWriter, r *http.Request) {
	registry.HandleRegistryCredentials(w, r, registry.CredentialDependencies{
		Platform:             pr.platform,
		PrincipalFromContext: principalFromContext,
		WriteJSON:            writeJSON,
		WriteBodyDecodeError: writeBodyDecodeError,
		RequestIP:            requestIP,
		AuditSource:          auditSource,
		AuditIdentityLabel:   auditIdentityLabel,
	})
}

func (pr platformRoutes) handleRegistryCredentialItem(w http.ResponseWriter, r *http.Request) {
	registry.HandleRegistryCredentialItem(w, r, registry.CredentialDependencies{
		Platform:             pr.platform,
		PrincipalFromContext: principalFromContext,
		WriteJSON:            writeJSON,
		RequestIP:            requestIP,
		AuditSource:          auditSource,
		AuditIdentityLabel:   auditIdentityLabel,
	})
}

func (pr platformRoutes) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	identity.HandleAuthMe(w, r, identity.Dependencies{
		PrincipalFromContext: principalFromContext,
		WriteJSON:            writeJSON,
	})
}

func (pr platformRoutes) handleSignup(w http.ResponseWriter, r *http.Request) {
	identity.HandleSignup(w, r, identity.Dependencies{
		Platform:             pr.platform,
		AuthenticateRequest:  pr.authenticateRequest,
		WriteJSON:            writeJSON,
		WriteBodyDecodeError: writeBodyDecodeError,
		RequestIP:            requestIP,
		RequestSource:        requestSource,
	})
}

func (pr platformRoutes) handleUsers(w http.ResponseWriter, r *http.Request) {
	identity.HandleUsers(w, r, identity.Dependencies{
		Platform:             pr.platform,
		AuthenticateRequest:  pr.authenticateRequest,
		WriteJSON:            writeJSON,
		WriteBodyDecodeError: writeBodyDecodeError,
		RequestIP:            requestIP,
		RequestSource:        requestSource,
		AuditIdentityLabel:   auditIdentityLabel,
	})
}
