package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	"mcp-platform-api/internal/apiauth"
	"mcp-platform-api/registry"
)

type registryCredentialAuthenticator interface {
	AuthenticateRegistryCredential(ctx context.Context, username, password string) (principal, bool, error)
}

func (s *apiServer) handleRegistryAuthz(w http.ResponseWriter, r *http.Request) {
	if s.nativeRegistryForwardAuth(w, r) {
		return
	}
	registry.HandleAuthz(w, r, s.registryAuthzDependencies())
}

func (s *apiServer) registryAuthzDependencies() registry.Dependencies {
	cfg := s.registryAuthzSettings()
	return registry.Dependencies{
		AuthenticateRequest:             s.authenticateRegistryRequest,
		RegistryCredentialAuthenticator: s.registryCredentialAuthenticator(),
		RegistryAuthzSettings:           func() *registry.AuthzConfig { return cfg },
		PrincipalCanAccessRegistryPath: func(p apiauth.Principal, r *http.Request) bool {
			return registry.PrincipalCanAccessRegistryPath(p, r, cfg)
		},
	}
}

func (s *apiServer) authenticateRegistryRequest(r *http.Request) (principal, bool, error) {
	if p, ok, err := s.authenticateRequest(r); err != nil || ok {
		if ok {
			p = s.enrichRegistryPrincipal(r.Context(), p)
		}
		return p, ok, err
	}

	username, password, ok := r.BasicAuth()
	if !ok {
		return principal{}, false, nil
	}
	// Registry-issued Docker credentials use the mcpr_ prefix. Route them through
	// AuthenticateRegistryCredential so username matching and team membership are
	// resolved from Postgres instead of the generic user API key shortcut below.
	if password != "" && !strings.HasPrefix(password, "mcpr_") {
		clone := r.Clone(r.Context())
		clone.Header.Set("x-api-key", password)
		if p, ok, err := s.authenticateRequest(clone); err == nil && ok {
			_ = username
			return s.enrichRegistryPrincipal(r.Context(), p), true, nil
		}
	}
	authn := s.registryCredentialAuthenticator()
	if authn == nil {
		return principal{}, false, nil
	}
	p, ok, err := authn.AuthenticateRegistryCredential(r.Context(), username, password)
	if !ok || err != nil {
		return principal(p), ok, err
	}
	return s.enrichRegistryPrincipal(r.Context(), principal(p)), true, nil
}

func (s *apiServer) enrichRegistryPrincipal(ctx context.Context, p principal) principal {
	if s.platform == nil || p.IsService || p.Role == roleAdmin {
		return p
	}
	userID := strings.TrimSpace(p.Subject)
	if userID == "" {
		return p
	}
	enriched, err := s.platform.PrincipalForUserID(ctx, userID)
	if err != nil {
		return p
	}
	enriched.AuthType = p.AuthType
	enriched.APIKeyID = p.APIKeyID
	enriched.IsService = p.IsService
	return principal(enriched)
}

func (s *apiServer) registryCredentialAuthenticator() registryCredentialAuthenticator {
	if s.registryAuth != nil {
		return s.registryAuth
	}
	if s.platform != nil {
		return s.platform
	}
	return nil
}

func (s *apiServer) registryAuthzSettings() *registry.AuthzConfig {
	s.registryAuthzOnce.Do(func() {
		if s.registryAuthz == nil {
			s.registryAuthz = registry.NewAuthzConfigFromEnv()
		}
	})
	return s.registryAuthz
}

func (s *apiServer) handleRegistryToken(w http.ResponseWriter, r *http.Request) {
	registry.HandleToken(w, r, registry.TokenDependencies{AuthenticateRequest: s.authenticateRegistryRequest, Config: s.registryAuthzSettings(), Signer: s.registrySigner, Pull: s.platform})
}

func (s *apiServer) nativeRegistryForwardAuth(w http.ResponseWriter, r *http.Request) bool {
	if s.registrySigner == nil {
		return false
	}
	path := registry.RegistryForwardedPath(r)
	if r.Header.Get("Authorization") == "" && r.Header.Get("x-api-key") == "" && os.Getenv("REGISTRY_TOKEN_REALM") != "" {
		challenge := fmt.Sprintf("Bearer realm=%q,service=%q", os.Getenv("REGISTRY_TOKEN_REALM"), "mcp-runtime-registry")
		repo := registry.RegistryRepoFromPath(strings.TrimPrefix(path, "/v2/"))
		if repo != "" && path != "/v2/_catalog" {
			action := "pull"
			method := r.Header.Get("X-Forwarded-Method")
			if method != "" && method != "GET" && method != "HEAD" {
				action = "push"
			}
			challenge += fmt.Sprintf(",scope=%q", "repository:"+repo+":"+action)
		}
		w.Header().Set("WWW-Authenticate", challenge)
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusUnauthorized)
		return true
	}
	method := r.Header.Get("X-Forwarded-Method")
	if method == "" {
		method = r.Method
	}
	if method != http.MethodGet && method != http.MethodHead && method != http.MethodPost && method != http.MethodPatch && method != http.MethodPut {
		w.WriteHeader(http.StatusForbidden)
		return true
	}
	action := "pull"
	if method != http.MethodGet && method != http.MethodHead {
		action = "push"
	}
	if username, password, ok := r.BasicAuth(); ok && strings.HasPrefix(password, "mcpp_") {
		if s.platform == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return true
		}
		scope, valid, err := s.platform.AuthenticateRegistryPullCredential(r.Context(), username, password)
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return true
		}
		if !valid {
			w.WriteHeader(http.StatusUnauthorized)
			return true
		}
		if action != "pull" {
			w.WriteHeader(http.StatusForbidden)
			return true
		}
		if path == "/v2/" || scope.Allows(registry.RegistryRepoFromPath(strings.TrimPrefix(path, "/v2/"))) {
			w.WriteHeader(http.StatusNoContent)
		} else {
			w.WriteHeader(http.StatusForbidden)
		}
		return true
	}
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return false
	}
	grants, valid := s.registrySigner.VerifyAccess(raw)
	if !valid {
		return false
	}
	if path == "/v2/" && action == "pull" {
		w.WriteHeader(http.StatusNoContent)
		return true
	}
	if source := registry.RegistryMountSource(r); source != "" {
		allowed := false
		for _, grant := range grants {
			if grant.Type == "repository" && grant.Name == source {
				for _, a := range grant.Actions {
					if a == "pull" {
						allowed = true
					}
				}
			}
		}
		if !allowed {
			w.WriteHeader(http.StatusForbidden)
			return true
		}
	}
	repo := registry.RegistryRepoFromPath(strings.TrimPrefix(path, "/v2/"))
	for _, grant := range grants {
		if grant.Type == "registry" && grant.Name == "catalog" && path == "/v2/_catalog" && action == "pull" {
			for _, a := range grant.Actions {
				if a == "*" {
					w.WriteHeader(http.StatusNoContent)
					return true
				}
			}
		}
		if grant.Type == "repository" && grant.Name == repo {
			for _, a := range grant.Actions {
				if a == action {
					w.WriteHeader(http.StatusNoContent)
					return true
				}
			}
		}
	}
	w.WriteHeader(http.StatusForbidden)
	return true
}
