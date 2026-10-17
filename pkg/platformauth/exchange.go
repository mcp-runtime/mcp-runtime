package platformauth

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"mcp-runtime/pkg/apihttp"
	"mcp-runtime/pkg/serviceutil"
)

const ExchangeTTL = time.Minute

var ErrExchangeUnauthorized = errors.New("token exchange unauthorized")

// ExchangePath fixes the audience in server configuration rather than deriving
// it from client-supplied URLs or forwarded headers.
func ExchangePath(target string) string {
	switch target {
	case AudienceRuntime:
		return "/internal/auth/exchange/runtime"
	case AudienceAnalytics:
		return "/internal/auth/exchange/analytics"
	default:
		return ""
	}
}

// ExchangeHandler is a Traefik forward-auth endpoint, never a public API route.
// A platform user bearer is the exchange credential. Internal service secrets
// are deliberately not distributed to the gateway or UI. API keys and anonymous
// requests pass through unchanged for the destination's own authentication and
// public-catalog policy; they never acquire a JWT or a human identity here.
type ExchangeHandler struct {
	Auth   Authenticator
	Target string
}

func (h ExchangeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if ExchangePath(h.Target) == "" || h.Auth.Audience != AudiencePlatform || len(h.Auth.Secret) == 0 || h.Auth.Principals == nil {
		apihttp.WriteEnvelope(w, http.StatusServiceUnavailable, apihttp.CodeServiceUnavailable, "token exchange unavailable")
		return
	}
	token := serviceutil.ExtractBearer(r.Header.Get("Authorization"))
	claims, err := Verify(h.Auth.Secret, token, AudiencePlatform)
	// The CLI historically sends its credential in both headers. Recognize a
	// duplicated, verified platform bearer and remove its X-Api-Key copy before
	// forwarding. Otherwise retain API-key priority and destination validation.
	// authResponseHeaders replaces BOTH headers, deleting absent response fields.
	if key := strings.TrimSpace(r.Header.Get("x-api-key")); key != "" && (key != token || err != nil || claims.IsService) {
		w.Header().Set("X-Api-Key", key)
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Header.Get("Authorization") == "" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if err != nil || claims.IsService {
		apihttp.WriteEnvelope(w, http.StatusUnauthorized, apihttp.CodeUnauthorized, "platform user token required")
		return
	}
	p, ok, err := h.Auth.currentPrincipal(r.Context(), ToPrincipal(claims))
	if err != nil {
		apihttp.WriteEnvelope(w, http.StatusServiceUnavailable, apihttp.CodeServiceUnavailable, "identity unavailable")
		return
	}
	if !ok || p.IsService {
		apihttp.WriteEnvelope(w, http.StatusUnauthorized, apihttp.CodeUnauthorized, "platform user token required")
		return
	}
	now := time.Now().UTC()
	expires := now.Add(ExchangeTTL)
	if claims.ExpiresAt.Time.Before(expires) {
		expires = claims.ExpiresAt.Time
	}
	// Account resolution may have taken us past the source's expiration.
	if !expires.After(now) {
		apihttp.WriteEnvelope(w, http.StatusUnauthorized, apihttp.CodeUnauthorized, "platform user token expired")
		return
	}
	p.AuthType = "platform_jwt"
	exchanged, err := signUntil(h.Auth.Secret, p, now, expires, []string{h.Target})
	if err != nil {
		apihttp.WriteEnvelope(w, http.StatusServiceUnavailable, apihttp.CodeServiceUnavailable, "token exchange unavailable")
		return
	}
	w.Header().Set("Authorization", "Bearer "+exchanged)
	w.WriteHeader(http.StatusOK)
}

// ExchangeBearer is used by the server-side UI proxy, which reaches the split
// APIs directly. The browser keeps its ordinary opaque session cookie.
func ExchangeBearer(ctx context.Context, client *http.Client, platform *url.URL, authorization, target string) (string, error) {
	path := ExchangePath(target)
	if client == nil || platform == nil || path == "" || authorization == "" {
		return "", errors.New("token exchange is not configured")
	}
	u := platform.ResolveReference(&url.URL{Path: path})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", authorization)
	// Never follow an exchange redirect with a user's source bearer.
	exchangeClient := *client
	exchangeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := exchangeClient.Do(req)
	if err != nil {
		return "", errors.New("token exchange unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", ErrExchangeUnauthorized
	}
	if resp.StatusCode != http.StatusOK || serviceutil.ExtractBearer(resp.Header.Get("Authorization")) == "" {
		return "", errors.New("token exchange refused")
	}
	return resp.Header.Get("Authorization"), nil
}
