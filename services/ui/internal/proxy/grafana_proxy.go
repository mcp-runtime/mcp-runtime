package proxy

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"

	"mcp-ui/internal/session"

	"mcp-runtime/pkg/platformauth"
	"mcp-runtime/pkg/serviceutil"
)

// The platform session is the only browser credential accepted here. Every
// request is reauthenticated by runtime-api, including admin requests, so a
// stale UI role or a surviving Grafana cookie never grants data access.
func NewGrafanaProxy(platformBase, runtimeBase *url.URL, store *session.Store, prefix string, httpClient *http.Client) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Frame-Options", "DENY")
		sess, ok := store.FromRequest(r)
		if !ok {
			serviceutil.WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		auth, apiKey, ok := sessionUpstreamCredential(sess)
		if !ok {
			serviceutil.WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		// Grafana's own app does not know the platform synchronizer token. It
		// uses a strict, required Origin check against configuration instead;
		// absent/null/alternate origins fail even with a valid session cookie.
		if unsafeHTTPMethod(r.Method) && !grafanaRequestOriginAllowed(r) {
			serviceutil.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "csrf_failed"})
			return
		}
		suffix, ok := strings.CutPrefix(r.URL.Path, prefix)
		if !ok || suffix != "" && !strings.HasPrefix(suffix, "/") || strings.ContainsAny(suffix, `\%`) ||
			suffix != "" && suffix != "/" && path.Clean(suffix) != strings.TrimSuffix(suffix, "/") {
			http.NotFound(w, r)
			return
		}
		if suffix == "" {
			suffix = "/"
		}
		target, err := resolveSessionProxyURL(runtimeBase, "/api/v1/runtime/observability/grafana"+suffix, r.URL.RawQuery)
		if err != nil {
			serviceutil.WriteJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream_error"})
			return
		}
		var body io.Reader
		if r.Body != nil {
			body = http.MaxBytesReader(w, r.Body, 4<<20)
		}
		req, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), body)
		if err != nil {
			serviceutil.WriteJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream_error"})
			return
		}
		// No browser cookie, X-WEBAUTH-* / X-Grafana-* identity, or caller
		// authorization header reaches either upstream.
		req.Header.Set("Accept", r.Header.Get("Accept"))
		req.Header.Set("Content-Type", r.Header.Get("Content-Type"))
		if auth != "" {
			auth, err = platformauth.ExchangeBearer(r.Context(), httpClient, platformBase, auth, platformauth.AudienceRuntime)
			if err != nil {
				status := http.StatusBadGateway
				if errors.Is(err, platformauth.ErrExchangeUnauthorized) {
					status = http.StatusUnauthorized
				}
				serviceutil.WriteJSON(w, status, map[string]string{"error": "token_exchange_failed"})
				return
			}
			req.Header.Set("Authorization", auth)
		} else {
			req.Header.Set("X-API-Key", apiKey)
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			serviceutil.WriteJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream_error"})
			return
		}
		if strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/html") {
			// Grafana's bootstrap uses its own per-response script nonce. Two
			// CSP headers are enforced cumulatively, so retaining the console's
			// policy would block that bootstrap even with the correct nonce.
			// Delegate only this trusted Grafana response, never the console.
			policy := strings.TrimSpace(resp.Header.Get("Content-Security-Policy"))
			if policy == "" {
				drainAndClose(resp.Body)
				serviceutil.WriteJSON(w, http.StatusBadGateway, map[string]string{"error": "grafana_security_policy_missing"})
				return
			}
			w.Header().Del("Content-Security-Policy")
		}
		copySessionProxyResponse(w, resp)
	})
}

func grafanaRequestOriginAllowed(r *http.Request) bool {
	expected, err := url.Parse(strings.TrimSpace(serviceutil.EnvOr("UI_PUBLIC_ORIGIN", "http://localhost:18080")))
	if err != nil || expected.Host == "" || expected.User != nil || expected.Path != "" || expected.RawQuery != "" || expected.Fragment != "" ||
		expected.Scheme != "http" && expected.Scheme != "https" {
		return false
	}
	provided, err := url.Parse(r.Header.Get("Origin"))
	return err == nil && provided.Host != "" && provided.User == nil && provided.Path == "" && provided.RawQuery == "" && provided.Fragment == "" &&
		strings.EqualFold(provided.Scheme, expected.Scheme) && strings.EqualFold(provided.Host, expected.Host)
}
