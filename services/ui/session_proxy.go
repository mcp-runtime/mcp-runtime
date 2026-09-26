package main

import (
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"mcp-runtime/pkg/serviceutil"
)

const (
	defaultRuntimeUpstream   = "http://mcp-runtime-api.mcp-sentinel.svc.cluster.local:8084"
	defaultAnalyticsUpstream = "http://mcp-analytics-api.mcp-sentinel.svc.cluster.local:8085"
	uiSessionAPIPrefix       = "/api/ui/v1"
	sessionProxyTimeout      = 15 * time.Second
)

var sessionProxyRuntimePrefixes = []string{
	"/dashboard/summary",
	"/runtime/namespaces",
	"/runtime/servers",
	"/runtime/tools",
	"/runtime/server-events",
	"/runtime/observability/links",
	"/runtime/observability/grafana/dashboard",
	"/runtime/observability/prometheus/query",
	"/runtime/teams",
	"/runtime/agents",
	"/runtime/grants",
	"/runtime/sessions",
	"/runtime/components",
	"/runtime/actions/restart",
	"/runtime/policy",
	"/user/api-keys",
	"/admin/operations",
	"/admin/deployments",
}

var sessionProxyAnalyticsPrefixes = []string{
	"/events",
	"/analytics/usage",
	"/user/analytics/usage",
}

// State-changing routes are kept separate from the read allowlist. A route is
// admitted only for the listed methods and, when Segments is non-zero, the
// exact number of path segments after Prefix.
var sessionProxyWriteRoutes = []sessionProxyWriteRoute{
	{Prefix: "/user/api-keys", Methods: []string{http.MethodPost}},
	{Prefix: "/user/api-keys/", Methods: []string{http.MethodDelete}, Segments: 1},
	{Prefix: "/runtime/grants/", Methods: []string{http.MethodPatch, http.MethodDelete}, Segments: 2},
	{Prefix: "/runtime/grants/", Methods: []string{http.MethodPost}, Segments: 3, Suffixes: []string{"revoke-sessions"}},
	{Prefix: "/runtime/grants", Methods: []string{http.MethodPost}},
	{Prefix: "/runtime/sessions/", Methods: []string{http.MethodPatch, http.MethodDelete}, Segments: 2},
	{Prefix: "/runtime/sessions", Methods: []string{http.MethodPost}},
	{Prefix: "/runtime/teams", Methods: []string{http.MethodPost}},
	{Prefix: "/runtime/teams/", Methods: []string{http.MethodPost}, Segments: 2, Suffixes: []string{"members", "users"}},
	{Prefix: "/runtime/teams/", Methods: []string{http.MethodPost}, Segments: 2, Suffixes: []string{"agents"}},
	{Prefix: "/runtime/teams/", Methods: []string{http.MethodPut, http.MethodDelete}, Segments: 3, Suffixes: []string{"members"}, SuffixIndex: 1},
	{Prefix: "/runtime/agents/", Methods: []string{http.MethodPatch}, Segments: 1},
	{Prefix: "/runtime/agents/", Methods: []string{http.MethodPost}, Segments: 2, Suffixes: []string{"deactivate", "reactivate"}},
	{Prefix: "/runtime/actions/restart", Methods: []string{http.MethodPost}},
	{Prefix: "/runtime/servers/", Methods: []string{http.MethodDelete}, Segments: 2},
}

type sessionProxyWriteRoute struct {
	Prefix      string
	Methods     []string
	Segments    int
	Suffixes    []string
	SuffixIndex int
}

func sessionProxyWriteRouteMatches(route sessionProxyWriteRoute, requestPath string) bool {
	if route.Segments == 0 {
		return requestPath == route.Prefix
	}
	rest, ok := strings.CutPrefix(requestPath, route.Prefix)
	if !ok || rest == "" || strings.HasPrefix(rest, "/") || strings.HasSuffix(rest, "/") {
		return false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != route.Segments {
		return false
	}
	if len(route.Suffixes) == 0 {
		return true
	}
	index := len(parts) - 1
	if route.SuffixIndex > 0 {
		index = route.SuffixIndex
	}
	for _, suffix := range route.Suffixes {
		if parts[index] == suffix {
			return true
		}
	}
	return false
}

func sessionProxyWriteAllowed(method, requestPath string) bool {
	for _, route := range sessionProxyWriteRoutes {
		if !sessionProxyWriteRouteMatches(route, requestPath) {
			continue
		}
		for _, allowed := range route.Methods {
			if allowed == method {
				return true
			}
		}
	}
	return false
}

func sessionProxyAllowHeader(requestPath string) string {
	methods := []string{http.MethodGet}
	for _, route := range sessionProxyWriteRoutes {
		if sessionProxyWriteRouteMatches(route, requestPath) {
			methods = append(methods, route.Methods...)
		}
	}
	return strings.Join(methods, ", ")
}

const sessionProxyMaxWriteBody = 32 * 1024

var sessionProxyHTTPClient = &http.Client{
	Timeout: sessionProxyTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

type sessionProxy struct {
	runtimeBase   *url.URL
	analyticsBase *url.URL
	store         *uiSessionStore
	client        *http.Client
}

func parseRuntimeUpstream(raw string) (*url.URL, error) {
	base := strings.TrimSpace(raw)
	if base == "" {
		return nil, errors.New("runtime upstream is empty")
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("runtime upstream must use http or https")
	}
	if u.Host == "" {
		return nil, errors.New("runtime upstream must include scheme and host")
	}
	if u.User != nil {
		return nil, errors.New("runtime upstream must not include userinfo")
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u, nil
}

func newSessionProxy(runtimeBase *url.URL, store *uiSessionStore) *sessionProxy {
	return newSessionProxyWithUpstreams(runtimeBase, runtimeBase, store)
}

func newSessionProxyWithUpstreams(runtimeBase, analyticsBase *url.URL, store *uiSessionStore) *sessionProxy {
	return &sessionProxy{
		runtimeBase:   runtimeBase,
		analyticsBase: analyticsBase,
		store:         store,
		client:        sessionProxyHTTPClient,
	}
}

func sessionProxyUpstreamPath(requestPath string) (string, bool) {
	_, upstreamPath, ok := sessionProxyRoute(requestPath)
	return upstreamPath, ok
}

func sessionProxyRoute(requestPath string) (*url.URL, string, bool) {
	cleaned := path.Clean("/" + strings.TrimPrefix(strings.TrimSpace(requestPath), "/"))
	suffix, ok := strings.CutPrefix(cleaned, uiSessionAPIPrefix)
	if !ok || suffix == "" {
		return nil, "", false
	}
	if sessionProxyPathAllowed(suffix, sessionProxyRuntimePrefixes) {
		return nil, "/api/v1" + suffix, true
	}
	if sessionProxyPathAllowed(suffix, sessionProxyAnalyticsPrefixes) {
		return nil, "/api/v1" + suffix, true
	}
	return nil, "", false
}

func sessionProxyPathAllowed(requestPath string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if requestPath == prefix || strings.HasPrefix(requestPath, prefix+"/") {
			return true
		}
	}
	return false
}

func (p *sessionProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	write := unsafeHTTPMethod(r.Method)
	base, upstreamPath, ok := sessionProxyRoute(r.URL.Path)
	if !ok {
		serviceutil.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	if write {
		suffix := strings.TrimPrefix(upstreamPath, "/api/v1")
		if !sessionProxyWriteAllowed(r.Method, suffix) {
			w.Header().Set("allow", sessionProxyAllowHeader(suffix))
			serviceutil.WriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
	}
	sess, ok := p.store.sessionFromRequest(r)
	if !ok {
		serviceutil.WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	authHeader, apiKey, ok := sessionUpstreamCredential(sess)
	if !ok {
		serviceutil.WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if status := verifyCSRF(r, sess); status != 0 {
		// #nosec G706 -- path-only telemetry; no credential or token material.
		log.Printf("ui session proxy csrf rejection method=%q path=%q", r.Method, r.URL.Path)
		serviceutil.WriteJSON(w, status, map[string]string{"error": "csrf_failed"})
		return
	}

	var body io.Reader
	if write && r.Body != nil {
		body = http.MaxBytesReader(w, r.Body, sessionProxyMaxWriteBody)
	}

	if strings.HasPrefix(upstreamPath, "/api/v1/events") ||
		strings.HasPrefix(upstreamPath, "/api/v1/analytics/usage") ||
		strings.HasPrefix(upstreamPath, "/api/v1/user/analytics/usage") {
		base = p.analyticsBase
	} else {
		base = p.runtimeBase
	}
	upstreamURL, err := resolveSessionProxyURL(base, upstreamPath, r.URL.RawQuery)
	if err != nil {
		serviceutil.WriteJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream_error"})
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL.String(), body)
	if err != nil {
		serviceutil.WriteJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream_error"})
		return
	}
	if accept := strings.TrimSpace(r.Header.Get("accept")); accept != "" {
		req.Header.Set("accept", accept)
	} else {
		req.Header.Set("accept", "application/json")
	}
	if write {
		contentType := strings.TrimSpace(r.Header.Get("content-type"))
		if contentType == "" {
			contentType = "application/json"
		}
		req.Header.Set("content-type", contentType)
	}
	copySessionProxyOriginHeaders(req, r)
	req.Header.Set("x-mcp-source", "ui")
	if authHeader != "" {
		req.Header.Set("authorization", authHeader)
	} else {
		req.Header.Set("x-api-key", apiKey)
	}

	client := p.client
	if client == nil {
		client = sessionProxyHTTPClient
	}
	resp, err := client.Do(req)
	if err != nil {
		// #nosec G706 -- path-only telemetry; upstream URL and credentials are omitted.
		log.Printf("ui session proxy upstream error path=%q", r.URL.Path)
		serviceutil.WriteJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream_error"})
		return
	}
	copySessionProxyResponse(w, resp)
}

func sessionUpstreamCredential(sess uiSession) (authHeader, apiKey string, ok bool) {
	if header := strings.TrimSpace(sess.UpstreamAuthHeader); header != "" {
		return header, "", true
	}
	if key := strings.TrimSpace(sess.UpstreamAPIKey); key != "" {
		return "", key, true
	}
	return "", "", false
}

func resolveSessionProxyURL(base *url.URL, upstreamPath, rawQuery string) (*url.URL, error) {
	if base == nil {
		return nil, errors.New("runtime upstream is empty")
	}
	ref, err := url.Parse(upstreamPath)
	if err != nil {
		return nil, err
	}
	resolved := base.ResolveReference(ref)
	if resolved.Scheme != base.Scheme || resolved.Host != base.Host {
		return nil, errors.New("runtime upstream redirected off host")
	}
	resolved.RawQuery = rawQuery
	resolved.Fragment = ""
	return resolved, nil
}

// copySessionProxyOriginHeaders preserves the public origin used by the
// runtime API when the dashboard is reached through an ingress or proxy.
func copySessionProxyOriginHeaders(dst, src *http.Request) {
	if forwardedHost := strings.TrimSpace(src.Header.Get("x-forwarded-host")); forwardedHost != "" {
		dst.Header.Set("x-forwarded-host", forwardedHost)
	}
	if forwardedProto := strings.TrimSpace(src.Header.Get("x-forwarded-proto")); forwardedProto != "" {
		dst.Header.Set("x-forwarded-proto", forwardedProto)
	}
}

func copySessionProxyResponse(w http.ResponseWriter, resp *http.Response) {
	defer drainAndClose(resp.Body)
	dst := w.Header()
	for key, values := range resp.Header {
		if skipSessionProxyResponseHeader(key) {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func skipSessionProxyResponseHeader(key string) bool {
	switch http.CanonicalHeaderKey(key) {
	case "Authorization", "Cookie", "Set-Cookie", "Connection", "Keep-Alive",
		"Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Trailers",
		"Transfer-Encoding", "Upgrade":
		return true
	default:
		return false
	}
}
