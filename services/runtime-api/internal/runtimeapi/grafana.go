package runtimeapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const grafanaProxyPrefix = "/runtime/observability/grafana"

// This is an identity extractor, not a PromQL parser or rewriter. The whole
// expression must subsequently equal a server-generated query byte for byte.
var grafanaTargetSelector = regexp.MustCompile(`namespace="([a-z0-9][a-z0-9.-]*)",server="([a-z0-9][a-z0-9.-]*)",server_uid="([a-zA-Z0-9-]+)"`)

type grafanaDataSource struct {
	UID  string `json:"uid"`
	Type string `json:"type"`
}

// Reconstruct the supported Prometheus model. In particular, no caller-supplied
// datasourceId, URL, expression datasource, queryType, or plugin option survives.
type grafanaQuery struct {
	RefID         string            `json:"refId"`
	Datasource    grafanaDataSource `json:"datasource"`
	Expr          string            `json:"expr"`
	Instant       bool              `json:"instant"`
	Range         bool              `json:"range"`
	Format        string            `json:"format,omitempty"`
	LegendFormat  string            `json:"legendFormat,omitempty"`
	IntervalMS    int64             `json:"intervalMs"`
	MaxDataPoints int64             `json:"maxDataPoints"`
}

type grafanaQueryRequest struct {
	From    string         `json:"from"`
	To      string         `json:"to"`
	Queries []grafanaQuery `json:"queries"`
}

// HandleRuntimeGrafana is the sole supported Grafana entry point. Authentication
// is performed by the runtime routes, not by a caller-provided identity header.
// Tenant data requests are restricted before they reach Grafana's shared
// datasource; editing a dashboard variable or using Explore cannot widen access.
func (s *RuntimeServer) HandleRuntimeGrafana(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r.Context())
	if !ok {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if p.Role != roleAdmin && !grafanaScopedAccessEnabled() {
		writeAPIError(w, http.StatusForbidden, "grafana_scoped_access_not_configured")
		return
	}
	_, suffix, ok := strings.Cut(r.URL.Path, grafanaProxyPrefix)
	if !ok || suffix == "" || !strings.HasPrefix(suffix, "/") ||
		path.Clean(suffix) != strings.TrimSuffix(suffix, "/") && suffix != "/" ||
		strings.ContainsAny(suffix, `\%`) {
		writeAPIError(w, http.StatusNotFound, "not_found")
		return
	}
	if p.Role != roleAdmin && s.handleTenantGrafanaAuxiliary(w, r, suffix) {
		return
	}
	var body io.Reader
	if p.Role != roleAdmin {
		if suffix == "/api/ds/query" && r.Method == http.MethodPost {
			var payload grafanaQueryRequest
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
			if err := decoder.Decode(&payload); err != nil || decoder.Decode(new(any)) != io.EOF {
				writeAPIError(w, http.StatusBadRequest, "invalid_grafana_query")
				return
			}
			if err := s.authorizeGrafanaQueries(r, &payload); err != nil {
				writeObservabilityError(w, err)
				return
			}
			encoded, err := json.Marshal(payload)
			if err != nil {
				writeAPIError(w, http.StatusBadRequest, "invalid_grafana_query")
				return
			}
			body = bytes.NewReader(encoded)
		} else if r.Method != http.MethodGet || !tenantGrafanaReadPath(suffix) {
			writeAPIError(w, http.StatusForbidden, "grafana_route_not_available")
			return
		}
		// Search and annotations never read global Grafana state for a tenant.
		if suffix == "/api/search" {
			writeJSON(w, http.StatusOK, []any{map[string]any{"uid": "mcp-server", "title": "MCP Server", "url": strings.TrimSuffix(envOr("UI_GRAFANA_PATH", "/grafana"), "/") + "/d/mcp-server/mcp-server", "type": "dash-db"}})
			return
		}
		if suffix == "/api/annotations" {
			writeJSON(w, http.StatusOK, []any{})
			return
		}
	} else if r.Body != nil {
		body = http.MaxBytesReader(w, r.Body, observabilityBodyLimit)
	}

	base, err := url.Parse(envOr("GRAFANA_UPSTREAM", "http://grafana.mcp-observability.svc:3000"))
	if err != nil || base.Host == "" || base.User != nil || base.Scheme != "http" && base.Scheme != "https" {
		writeAPIError(w, http.StatusServiceUnavailable, "grafana_not_configured")
		return
	}
	base.Path = strings.TrimSuffix(envOr("UI_GRAFANA_PATH", "/grafana"), "/") + suffix
	base.RawPath, base.Fragment = "", ""
	// Query parameters cannot influence tenant backend selection or metadata
	// endpoints. Only dashboard/Explore shells need their UI state preserved.
	base.RawQuery = ""
	if p.Role == roleAdmin || !strings.HasPrefix(suffix, "/api/") {
		base.RawQuery = r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, base.String(), body)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "grafana_unavailable")
		return
	}
	// Construct headers from scratch. Never forward cookies, authorization,
	// X-Grafana-Org-Id, X-WEBAUTH-*, forwarding, or plugin identity headers.
	identity := sha256.Sum256([]byte(p.Role + ":" + p.Subject))
	req.Header.Set("X-WEBAUTH-USER", fmt.Sprintf("mcp-%x", identity))
	req.Header.Set("X-WEBAUTH-ROLE", "Viewer")
	if p.Role == roleAdmin {
		req.Header.Set("X-WEBAUTH-ROLE", "Admin")
	}
	req.Header.Set("Content-Type", "application/json")
	if r.Method == http.MethodGet {
		req.Header.Set("Accept", r.Header.Get("Accept"))
	}
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "grafana_unavailable")
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Cache-Control", "no-store")
	for _, header := range []string{"Content-Type", "Content-Security-Policy", "X-Content-Type-Options"} {
		if value := resp.Header.Get(header); value != "" {
			w.Header().Set(header, value)
		}
	}
	if location := resp.Header.Get("Location"); location != "" {
		// Auth redirects must stay under the configured public Grafana path.
		if strings.HasPrefix(location, strings.TrimSuffix(envOr("UI_GRAFANA_PATH", "/grafana"), "/")+"/") && !strings.HasPrefix(location, "//") {
			w.Header().Set("Location", location)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 32<<20))
}

func tenantGrafanaReadPath(suffix string) bool {
	switch suffix {
	case "/", "/explore", "/d/mcp-server/mcp-server", "/api/dashboards/uid/mcp-server",
		"/api/frontend/settings", "/api/user", "/api/user/preferences", "/api/user/orgs",
		"/api/org", "/api/org/preferences", "/api/access-control/user/permissions",
		"/api/datasources", "/api/datasources/uid/prometheus", "/api/plugins/prometheus/settings",
		"/api/search", "/api/annotations", "/api/health":
		return true
	}
	// Grafana's immutable frontend files only; never /api/plugins/*/resources,
	// datasource proxy, snapshots, rendering, Live, or arbitrary plugin paths.
	return strings.HasPrefix(suffix, "/public/") && path.Clean(suffix) == suffix && !strings.ContainsAny(suffix, `\%`)
}

// Grafana 10.2 requests these auxiliary feeds while booting dashboards and
// Explore. They deliberately return empty platform-owned metadata rather than
// forwarding to global alert/metric/correlation stores. Unknown resource paths
// remain denied. Exemplars are queries, so their expression is still live-
// authorized even though bounded tenant Explore does not expose trace samples.
func (s *RuntimeServer) handleTenantGrafanaAuxiliary(w http.ResponseWriter, r *http.Request, suffix string) bool {
	if suffix == "/api/datasources/uid/prometheus/resources/api/v1/query_exemplars" && (r.Method == http.MethodGet || r.Method == http.MethodPost) {
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		if err := r.ParseForm(); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_grafana_query")
			return true
		}
		expr := r.Form.Get("query")
		if expr == "test" {
			// Grafana's Prometheus client probes exemplar support with this
			// literal. It is metadata detection, never a backend data query.
			writeJSON(w, http.StatusOK, map[string]any{"status": "success", "data": []any{}})
			return true
		}
		payload := grafanaQueryRequest{From: strconv.FormatInt(time.Now().Add(-time.Hour).UnixMilli(), 10), To: strconv.FormatInt(time.Now().UnixMilli(), 10), Queries: []grafanaQuery{{RefID: "A", Datasource: grafanaDataSource{UID: "prometheus", Type: "prometheus"}, Expr: expr, Range: true}}}
		if err := s.authorizeGrafanaQueries(r, &payload); err != nil {
			writeObservabilityError(w, err)
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "success", "data": []any{}})
		return true
	}
	if r.Method == http.MethodGet {
		switch suffix {
		case "/api/prometheus/grafana/api/v1/rules", "/api/datasources/uid/prometheus/resources/api/v1/rules":
			writeJSON(w, http.StatusOK, map[string]any{"status": "success", "data": map[string]any{"groups": []any{}}})
			return true
		case "/api/datasources/correlations":
			writeJSON(w, http.StatusOK, map[string]any{"correlations": []any{}, "totalCount": 0, "page": 1, "limit": 100})
			return true
		case "/api/datasources/uid/prometheus/resources/api/v1/metadata":
			writeJSON(w, http.StatusOK, map[string]any{"status": "success", "data": map[string]any{}})
			return true
		case "/api/datasources/uid/prometheus/resources/api/v1/label/__name__/values":
			writeJSON(w, http.StatusOK, map[string]any{"status": "success", "data": []any{}})
			return true
		}
	}
	if suffix == "/api/datasources/uid/prometheus/resources/api/v1/labels" && (r.Method == http.MethodGet || r.Method == http.MethodPost) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "success", "data": []any{}})
		return true
	}
	return false
}

func (s *RuntimeServer) authorizeGrafanaQueries(r *http.Request, payload *grafanaQueryRequest) error {
	deny := observabilityRequestError{status: http.StatusForbidden, message: "only fixed queries for observable servers are available in Grafana"}
	if len(payload.Queries) == 0 || len(payload.Queries) > 20 {
		return deny
	}
	from, err := strconv.ParseInt(payload.From, 10, 64)
	if err != nil {
		return deny
	}
	to, err := strconv.ParseInt(payload.To, 10, 64)
	now := time.Now().UnixMilli()
	if err != nil || from >= to || from < now-31*24*time.Hour.Milliseconds() || to > now+5*time.Minute.Milliseconds() {
		return deny
	}
	refs := make(map[string]bool)
	for i := range payload.Queries {
		query := &payload.Queries[i]
		if query.Datasource.UID != "prometheus" || query.Datasource.Type != "prometheus" ||
			query.RefID == "" || len(query.RefID) > 64 || refs[query.RefID] ||
			query.Format != "" && query.Format != "time_series" && query.Format != "table" {
			return deny
		}
		refs[query.RefID] = true
		match := grafanaTargetSelector.FindStringSubmatch(query.Expr)
		if len(match) != 4 {
			return deny
		}
		targetRequest := r.Clone(r.Context())
		targetURL := *r.URL
		targetURL.RawQuery = url.Values{"namespace": {match[1]}, "server": {match[2]}}.Encode()
		targetRequest.URL = &targetURL
		_, target, err := s.authorizedObservabilityTarget(targetRequest)
		if err != nil {
			return deny
		}
		matched := false
		for _, allowed := range scopedPrometheusQueries(target.Namespace, target.Name, string(target.UID)) {
			if allowed.Query == query.Expr {
				matched = true
				break
			}
		}
		if !matched {
			return deny
		}
		query.IntervalMS = max(query.IntervalMS, int64(15_000), (to-from)/2000)
		query.MaxDataPoints = min(max(query.MaxDataPoints, int64(1)), int64(2000))
		query.LegendFormat = ""
	}
	return nil
}

func grafanaScopedAccessEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(envOr("GRAFANA_SCOPED_USER_ACCESS", "false")), "true")
}
