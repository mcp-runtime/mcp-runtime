package platformauth

import (
	"context"
	"crypto/hmac"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strings"

	"mcp-runtime/pkg/apihttp"
	"mcp-runtime/pkg/serviceutil"
)

type UserKeyResolver interface {
	ResolveAPIKey(context.Context, string) (Principal, bool, error)
}

type OIDCVerifier interface {
	Verify(context.Context, string) (Principal, bool, error)
}

// PrincipalResolver refreshes current account and membership state after token
// verification. A signed token cannot keep a deactivated account authorized.
type PrincipalResolver interface {
	PrincipalForUserID(context.Context, string) (Principal, error)
}

type Authenticator struct {
	Secret          []byte
	Audience        string
	ServiceAPIKeys  map[string]struct{}
	AdminAPIKeys    map[string]struct{}
	UserKeyResolver UserKeyResolver
	OIDC            OIDCVerifier
	Principals      PrincipalResolver
	PublicFallback  func(*http.Request) (Principal, bool)
}

func (a Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok, err := a.AuthenticateRequest(r)
		if err != nil {
			log.Printf("auth error: %v", err)
			apihttp.WriteEnvelope(w, http.StatusInternalServerError, apihttp.CodeAuthFailed, "authentication failed")
			return
		}
		if !ok && a.PublicFallback != nil {
			p, ok = a.PublicFallback(r)
		}
		if !ok {
			apihttp.WriteEnvelope(w, http.StatusUnauthorized, apihttp.CodeUnauthorized, "authentication required")
			return
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	})
}

func (a Authenticator) RequireRole(role string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := FromContext(r.Context())
		if !ok || p.Role != role {
			apihttp.WriteEnvelope(w, http.StatusForbidden, apihttp.CodeForbidden, "insufficient permissions")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a Authenticator) AuthenticateRequest(r *http.Request) (Principal, bool, error) {
	if apiKey := strings.TrimSpace(r.Header.Get("x-api-key")); apiKey != "" {
		if containsAPIKey(a.ServiceAPIKeys, apiKey) {
			role := RoleUser
			if containsAPIKey(a.AdminAPIKeys, apiKey) {
				role = RoleAdmin
			}
			return Principal{Role: role, AuthType: "service_api_key", IsService: true}, true, nil
		}
		if a.UserKeyResolver != nil {
			if p, ok, err := a.UserKeyResolver.ResolveAPIKey(r.Context(), apiKey); err != nil || ok {
				return p, ok, err
			}
		}
	}
	token := serviceutil.ExtractBearer(r.Header.Get("authorization"))
	if token == "" {
		return Principal{}, false, nil
	}
	if len(a.Secret) > 0 {
		if claims, err := Verify(a.Secret, token, a.Audience); err == nil {
			return a.currentPrincipal(r.Context(), ToPrincipal(claims))
		}
	}
	if a.OIDC != nil {
		p, ok, err := a.OIDC.Verify(r.Context(), token)
		if err != nil || !ok {
			return p, ok, err
		}
		return a.currentPrincipal(r.Context(), p)
	}
	return Principal{}, false, nil
}

func (a Authenticator) currentPrincipal(ctx context.Context, p Principal) (Principal, bool, error) {
	if a.Principals == nil || p.IsService {
		return p, true, nil
	}
	if p.UserID() == "" {
		return Principal{}, false, nil
	}
	current, err := a.Principals.PrincipalForUserID(ctx, p.UserID())
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, false, nil
	}
	if err != nil {
		return Principal{}, false, err
	}
	if current.UserID() != p.UserID() {
		return Principal{}, false, errors.New("identity resolver subject mismatch")
	}
	current.AuthType = p.AuthType
	current.APIKeyID = p.APIKeyID
	return current, true, nil
}

func containsAPIKey(keys map[string]struct{}, apiKey string) bool {
	if len(keys) == 0 || apiKey == "" {
		return false
	}
	found := false
	for key := range keys {
		if hmac.Equal([]byte(key), []byte(apiKey)) {
			found = true
		}
	}
	return found
}
