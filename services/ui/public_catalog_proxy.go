package main

import (
	"io"
	"net/http"
	"net/url"
	"strings"

	"mcp-runtime/pkg/serviceutil"
)

// publicCatalogAPIPrefix serves anonymous public-mode catalog reads. Unlike
// uiSessionAPIPrefix, requests here never carry a session cookie, API key, or
// bearer token - the runtime API authenticates them as a synthetic public
// principal (PublicCatalogFallback in
// services/runtime-api/internal/runtimeapi/platform_mode.go), the same way a
// signed-out visitor to a public deployment reached the catalog before the
// legacy dashboard was removed.
const publicCatalogAPIPrefix = "/api/public/v1"

var publicCatalogPaths = map[string]string{
	"/servers": "/api/v1/runtime/servers",
	"/tools":   "/api/v1/runtime/tools",
}

// newPublicCatalogProxy proxies GET-only, credential-free catalog reads to
// the runtime API. It is registered unconditionally but only serves traffic
// when enabled is true (platform mode is "public"), so an org
// deployment never exposes an unauthenticated path onto its runtime API.
func newPublicCatalogProxy(runtimeBase *url.URL, enabled bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !enabled || r.Method != http.MethodGet {
			serviceutil.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		suffix := strings.TrimPrefix(r.URL.Path, publicCatalogAPIPrefix)
		upstreamPath, ok := publicCatalogPaths[suffix]
		if !ok {
			serviceutil.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		upstreamURL, err := resolveSessionProxyURL(runtimeBase, upstreamPath, r.URL.RawQuery)
		if err != nil {
			serviceutil.WriteJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream_error"})
			return
		}
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstreamURL.String(), nil)
		if err != nil {
			serviceutil.WriteJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream_error"})
			return
		}
		req.Header.Set("accept", "application/json")
		req.Header.Set("x-mcp-source", "ui-public")
		copySessionProxyOriginHeaders(req, r)

		resp, err := sessionProxyHTTPClient.Do(req)
		if err != nil {
			serviceutil.WriteJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream_error"})
			return
		}
		defer func() { _ = resp.Body.Close() }()
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	})
}
