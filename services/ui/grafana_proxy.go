package main

import (
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"

	"mcp-runtime/pkg/serviceutil"
)

// The platform session is the only browser credential accepted here. Every
// request is reauthenticated by runtime-api, including admin requests, so a
// stale UI role or a surviving Grafana cookie never grants data access.
func grafanaSessionProxy(runtimeBase *url.URL, store *uiSessionStore, prefix string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		sess, ok := store.sessionFromRequest(r)
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
			req.Header.Set("Authorization", auth)
		} else {
			req.Header.Set("X-API-Key", apiKey)
		}
		resp, err := sessionProxyHTTPClient.Do(req)
		if err != nil {
			serviceutil.WriteJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream_error"})
			return
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
