package registry

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"strings"

	"mcp-platform-api/internal/apiauth"
	"mcp-platform-api/internal/httperrors"
	"mcp-platform-api/internal/platformstore"
	"mcp-runtime/pkg/publishscope"
	"mcp-runtime/pkg/serviceutil"
)

const authChallenge = `Basic realm="mcp-runtime-registry"`

type RegistryCredentialAuthenticator interface {
	AuthenticateRegistryCredential(ctx context.Context, username, password string) (apiauth.Principal, bool, error)
}

type Dependencies struct {
	AuthenticateRequest             func(*http.Request) (apiauth.Principal, bool, error)
	RegistryCredentialAuthenticator RegistryCredentialAuthenticator
	RegistryAuthzSettings           func() *AuthzConfig
	PrincipalCanAccessRegistryPath  func(apiauth.Principal, *http.Request) bool
}

type AuthzConfig struct {
	mode              string
	catalogNamespaces map[string]struct{}
}

func HandleAuthz(w http.ResponseWriter, r *http.Request, deps Dependencies) {
	p, ok, err := deps.AuthenticateRequest(r)
	if err != nil {
		log.Printf("registry auth error: %v", err)
		httperrors.AuthFailed(w)
		return
	}
	if !ok {
		writeRegistryAuthChallenge(w)
		return
	}
	if p.Role != apiauth.RoleAdmin {
		if !deps.PrincipalCanAccessRegistryPath(p, r) {
			httperrors.Forbidden(w, "forbidden")
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	serviceutil.WriteJSON(w, status, payload)
}

func writeRegistryAuthChallenge(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", authChallenge)
	w.Header().Set("Cache-Control", "no-store")
	httperrors.Unauthorized(w)
}

func RegistryForwardedPath(r *http.Request) string {
	if r == nil {
		return ""
	}
	var fallback string
	for _, key := range []string{"X-Forwarded-Uri", "X-Forwarded-URL"} {
		path := forwardedHeaderPath(r, key)
		if path == "" {
			continue
		}
		if strings.HasPrefix(path, "/v2/") {
			return path
		}
		if fallback == "" {
			fallback = path
		}
	}
	if r.URL != nil {
		if path := strings.TrimSpace(r.URL.Path); strings.HasPrefix(path, "/v2/") {
			return path
		}
		if fallback == "" {
			fallback = r.URL.Path
		}
	}
	return fallback
}

func forwardedHeaderPath(r *http.Request, key string) string {
	raw := strings.TrimSpace(r.Header.Get(key))
	if raw == "" {
		return ""
	}
	if parsed, err := url.Parse(raw); err == nil && parsed.Path != "" {
		return parsed.Path
	}
	return raw
}

func RegistryPathScope(r *http.Request) (string, bool) {
	path := RegistryForwardedPath(r)
	if path == "" {
		return "", false
	}
	if !strings.HasPrefix(path, "/v2/") {
		return "", false
	}
	rest := strings.Trim(strings.TrimPrefix(path, "/v2/"), "/")
	if rest == "" {
		return "", true
	}
	if strings.HasPrefix(rest, "_catalog") {
		return "", false
	}
	repo := RegistryRepoFromPath(rest)
	if repo == "" {
		return "", false
	}
	scope, _, _ := strings.Cut(repo, "/")
	scope = strings.TrimSpace(scope)
	return scope, scope != ""
}

func RegistryRepoFromPath(rest string) string {
	end := len(rest)
	for _, marker := range []string{"/blobs/", "/manifests/", "/tags/"} {
		if idx := strings.Index(rest, marker); idx >= 0 && idx < end {
			end = idx
		}
	}
	if end == len(rest) {
		return ""
	}
	return strings.Trim(rest[:end], "/")
}

func PrincipalCanAccessRegistryScope(p apiauth.Principal, scope string, cfg *AuthzConfig) bool {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return false
	}
	if cfg != nil {
		if cfg.catalogScopeWritable(scope) {
			return true
		}
		if scope == platformstore.SharedCatalogNamespace && !cfg.sharedCatalogWritableForUsers() {
			return false
		}
	}
	for _, team := range p.Teams {
		if strings.TrimSpace(team.Slug) == scope || strings.TrimSpace(team.Namespace) == scope {
			return true
		}
	}
	return false
}

// PrincipalCanAccessRegistryPath resolves the repository scope from the forwarded registry path.
func PrincipalCanAccessRegistryPath(p apiauth.Principal, r *http.Request, cfg *AuthzConfig) bool {
	scope, ok := RegistryPathScope(r)
	if !ok {
		return false
	}
	if source := RegistryMountSource(r); source != "" {
		sourceScope, _, ok := strings.Cut(source, "/")
		if !ok || !PrincipalCanAccessRegistryScope(p, sourceScope, cfg) {
			return false
		}
	}
	if scope == "" {
		return true
	}
	return PrincipalCanAccessRegistryScope(p, scope, cfg)
}

func (cfg *AuthzConfig) catalogScopeWritable(scope string) bool {
	if cfg == nil {
		return false
	}
	scope = strings.TrimSpace(scope)
	switch cfg.mode {
	case string(publishscope.Public):
		if scope == publishscope.PublicRegistryAlias {
			return true
		}
	case string(publishscope.Org):
		if scope == publishscope.OrgRegistryAlias {
			return true
		}
	default:
		return false
	}
	_, ok := cfg.catalogNamespaces[scope]
	return ok
}

func (cfg *AuthzConfig) sharedCatalogWritableForUsers() bool {
	if cfg == nil {
		return false
	}
	switch cfg.mode {
	case string(publishscope.Public), string(publishscope.Org):
		return true
	default:
		return false
	}
}

// RegistryMountSource preserves the source repository query for cross-repository
// blob mounts. Both source pull and destination push must be authorized.
func RegistryMountSource(r *http.Request) string {
	for _, key := range []string{"X-Forwarded-Uri", "X-Forwarded-URL"} {
		if raw := r.Header.Get(key); raw != "" {
			if u, err := url.Parse(raw); err == nil && strings.HasPrefix(u.Path, "/v2/") {
				return u.Query().Get("from")
			}
		}
	}
	if r.URL != nil {
		return r.URL.Query().Get("from")
	}
	return ""
}
