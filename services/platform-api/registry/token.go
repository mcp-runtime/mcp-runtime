package registry

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"

	"mcp-platform-api/internal/apiauth"
	"mcp-runtime/pkg/registryauth"
)

var repositoryPattern = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$`)

type PullScope = registryauth.PullScope
type PullAuthenticator interface {
	AuthenticateRegistryPullCredential(context.Context, string, string) (PullScope, bool, error)
}
type TokenDependencies struct {
	AuthenticateRequest func(*http.Request) (apiauth.Principal, bool, error)
	Config              *AuthzConfig
	Signer              *registryauth.Signer
	Pull                PullAuthenticator
}

// HandleToken issues Distribution tokens from server-authorized repository
// scopes. Forwarded path/method headers never participate in this decision.
func HandleToken(w http.ResponseWriter, r *http.Request, deps TokenDependencies) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if len(r.URL.RawQuery) > 8192 || r.URL.Query().Get("service") != registryauth.Service {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if deps.Signer == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	var principal apiauth.Principal
	var node PullScope
	authenticated, isNode := false, false
	username, password, basic := r.BasicAuth()
	if basic && strings.HasPrefix(password, "mcpp_") {
		if deps.Pull == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		var err error
		node, authenticated, err = deps.Pull.AuthenticateRegistryPullCredential(ctx, username, password)
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		isNode = true
	} else {
		var err error
		principal, authenticated, err = deps.AuthenticateRequest(r)
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
	}
	if !authenticated {
		writeRegistryAuthChallenge(w)
		return
	}
	scopes := r.URL.Query()["scope"]
	if len(scopes) > 32 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	access := []registryauth.Access{}
	for _, raw := range scopes {
		for _, scope := range strings.Fields(raw) {
			if len(access) >= 32 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			parts := strings.Split(scope, ":")
			if len(parts) != 3 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if parts[0] == "registry" && parts[1] == "catalog" && parts[2] == "*" {
				if isNode || principal.Role != apiauth.RoleAdmin {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				access = append(access, registryauth.Access{Type: "registry", Name: "catalog", Actions: []string{"*"}})
				continue
			}
			if parts[0] != "repository" || len(parts[1]) > 255 || !repositoryPattern.MatchString(parts[1]) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			repo := parts[1]
			prefix, _, _ := strings.Cut(repo, "/")
			allowed := principal.Role == apiauth.RoleAdmin || (strings.Contains(repo, "/") && PrincipalCanAccessRegistryScope(principal, prefix, deps.Config))
			if isNode {
				allowed = node.Allows(repo)
			}
			if !allowed {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			actions := []string{}
			for _, action := range strings.Split(parts[2], ",") {
				if action != "pull" && action != "push" {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				if isNode && action != "pull" {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				if len(actions) == 0 || actions[0] != action {
					actions = append(actions, action)
				}
			}
			access = append(access, registryauth.Access{Type: "repository", Name: repo, Actions: actions})
		}
	}
	subject := principal.Subject
	if isNode {
		subject = "node:" + node.Namespace
	}
	token, err := deps.Signer.Sign(subject, access)
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "access_token": token, "expires_in": int(registryauth.Lifetime.Seconds()), "issued_at": time.Now().UTC().Format(time.RFC3339)})
}
