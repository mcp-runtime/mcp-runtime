package runtimeapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	chpkg "mcp-runtime/pkg/clickhouse"
	"mcp-runtime/pkg/controlplane"
	"mcp-runtime/pkg/k8sclient"
	"mcp-runtime/pkg/platformauth"
	"mcp-runtime/pkg/serviceutil"
)

const (
	defaultPrometheusAPIURL  = "http://prometheus.mcp-observability.svc:9090/prometheus"
	defaultLokiAPIURL        = "http://loki.mcp-observability.svc:3100"
	prometheusErrorBodyLimit = 1024
	observabilityBodyLimit   = 4 << 20
	observabilityLogLimit    = 100

	envPrometheusAPIURL          = "PROMETHEUS_API_URL"
	envMCPPrometheusAPIURL       = "MCP_PROMETHEUS_API_URL"
	envLokiAPIURL                = "LOKI_API_URL"
	envGrafanaServerDashboardURL = "GRAFANA_SERVER_DASHBOARD_URL"
)

type observabilityLinksResponse struct {
	Namespace  string                     `json:"namespace"`
	Server     string                     `json:"server"`
	TeamID     string                     `json:"team_id,omitempty"`
	Prometheus observabilityPrometheusSet `json:"prometheus"`
	Grafana    observabilityGrafanaLink   `json:"grafana"`
}

type observabilityPrometheusSet struct {
	Queries         []observabilityPrometheusQueryLink `json:"queries"`
	DirectAdminOnly bool                               `json:"direct_admin_only"`
}

type observabilityPrometheusQueryLink struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	URL         string `json:"url"`
	GrafanaURL  string `json:"grafana_url,omitempty"`
	Query       string `json:"query,omitempty"`
}

type observabilityGrafanaLink struct {
	Available       bool   `json:"available"`
	URL             string `json:"url,omitempty"`
	DirectAdminOnly bool   `json:"direct_admin_only"`
	Reason          string `json:"reason,omitempty"`
}

type observabilityMetricsSummary struct {
	Window          string   `json:"window"`
	ToolCallRate    *float64 `json:"tools_call_rate_per_second"`
	ToolCallP95     *float64 `json:"tools_call_p95_seconds"`
	Upstream5xxRate *float64 `json:"upstream_5xx_rate_per_second"`
	PolicyDenyCount *float64 `json:"policy_deny_count"`
	SlowestTool     string   `json:"slowest_tool,omitempty"`
}

type observabilityMetricQuery struct {
	id    string
	query string
}

type scopedPrometheusQuery struct {
	ID          string
	Name        string
	Description string
	Query       string
	PanelID     int
}

type observabilityRequestError struct {
	status  int
	message string
}

func (e observabilityRequestError) Error() string {
	return e.message
}

func (s *RuntimeServer) HandleRuntimeObservabilityLinks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	p, target, err := s.authorizedObservabilityTarget(r)
	if err != nil {
		writeObservabilityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, observabilityLinksForMCPServer(*target, p, r))
}

func (s *RuntimeServer) HandleRuntimeObservabilityPrometheusQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	_, target, err := s.authorizedObservabilityTarget(r)
	if err != nil {
		writeObservabilityError(w, err)
		return
	}

	queryID := strings.TrimSpace(r.URL.Query().Get("query_id"))
	query, ok := scopedPrometheusQueryByID(queryID, target.Namespace, target.Name, string(target.UID))
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown query_id"})
		return
	}

	promURL, err := prometheusQueryURL(prometheusAPIBaseURL(), query.Query)
	if err != nil {
		log.Printf("observability prometheus proxy configuration error: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "prometheus not configured"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, promURL, nil)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query_build_failed"})
		return
	}
	resp, err := serviceutil.DoHTTP(nil, req)
	if err != nil {
		log.Printf("observability prometheus query failed namespace=%q server=%q query_id=%q err=%v", target.Namespace, target.Name, query.ID, err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "prometheus_unavailable"})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body := readPrometheusErrorBody(resp.Body)
		log.Printf(
			"observability prometheus query failed namespace=%q server=%q query_id=%q status=%d body=%q",
			target.Namespace,
			target.Name,
			query.ID,
			resp.StatusCode,
			body,
		)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "prometheus_query_failed"})
		return
	}
	var payload any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "prometheus_response_invalid"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"namespace":  target.Namespace,
		"server":     target.Name,
		"query_id":   query.ID,
		"query":      query.Query,
		"prometheus": payload,
	})
}

// HandleRuntimeObservabilityMetrics returns a fixed set of owner-scoped,
// one-hour gateway metric values. Callers cannot submit PromQL.
func (s *RuntimeServer) HandleRuntimeObservabilityMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	_, target, err := s.authorizedObservabilityTarget(r)
	if err != nil {
		writeObservabilityError(w, err)
		return
	}

	queries := scopedObservabilityMetricQueries(target.Namespace, target.Name, string(target.UID))
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	type result struct {
		id    string
		value *float64
		tool  string
		err   error
	}
	results := make(chan result, len(queries))
	var wg sync.WaitGroup
	for _, query := range queries {
		query := query
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, tool, err := fetchObservabilityMetric(ctx, query.query)
			results <- result{id: query.id, value: value, tool: tool, err: err}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		tool, err := s.slowestObservedTool(ctx, target.Namespace, target.Name, string(target.UID))
		results <- result{id: "slowest_tool", tool: tool, err: err}
	}()
	go func() { wg.Wait(); close(results) }()

	var summary observabilityMetricsSummary
	summary.Window = "1h"
	for result := range results {
		if result.err != nil {
			log.Printf("observability metric query failed namespace=%q server=%q query_id=%q err=%v", target.Namespace, target.Name, result.id, result.err)
			continue
		}
		switch result.id {
		case "tools_call_rate":
			summary.ToolCallRate = result.value
		case "tools_call_p95":
			summary.ToolCallP95 = result.value
		case "upstream_5xx_rate":
			summary.Upstream5xxRate = result.value
		case "policy_deny_count":
			summary.PolicyDenyCount = result.value
		case "slowest_tool":
			summary.SlowestTool = result.tool
		}
	}
	writeJSON(w, http.StatusOK, summary)
}

func scopedObservabilityMetricQueries(namespace, serverName, uid string) []observabilityMetricQuery {
	selector := observabilityMetricSelector(namespace, serverName, uid)
	callSelector := selector + `,rpc_method="tools/call"`
	return []observabilityMetricQuery{
		{id: "tools_call_rate", query: fmt.Sprintf(`sum(rate(mcp_gateway_requests_total{%s}[1h]))`, callSelector)},
		{id: "tools_call_p95", query: fmt.Sprintf(`histogram_quantile(0.95, sum by (le) (rate(mcp_gateway_request_duration_seconds_bucket{%s}[1h])))`, callSelector)},
		{id: "upstream_5xx_rate", query: fmt.Sprintf(`sum(rate(mcp_gateway_requests_total{%s,decision="allow",status=~"5.."}[1h]))`, callSelector)},
		{id: "policy_deny_count", query: fmt.Sprintf(`sum(increase(mcp_gateway_policy_decisions_total{%s,decision="deny"}[1h]))`, selector)},
	}
}

// Use actual allowed tools/call rows, rather than extrapolated histogram counts,
// before naming a slowest tool. The immutable UID prevents name-reuse leaks.
func (s *RuntimeServer) slowestObservedTool(ctx context.Context, namespace, server, uid string) (string, error) {
	if s.db == nil || s.db.Conn == nil {
		return "", nil
	}
	if err := chpkg.ValidateDBName(s.db.DBName); err != nil {
		return "", err
	}
	query := `SELECT tool_name FROM ` + s.db.DBName + `.events
		WHERE namespace = ? AND server = ? AND JSONExtractString(payload, 'server_uid') = ?
		AND timestamp >= now() - INTERVAL 1 HOUR AND timestamp <= now()
		AND JSONExtractString(payload, 'rpc_method') = 'tools/call'
		AND event_type = 'mcp.request'
		AND decision = 'allow' AND tool_name != '' AND JSONHas(payload, 'latency_ms')
		GROUP BY tool_name HAVING count() >= 5
		ORDER BY quantileExact(0.95)(JSONExtractInt(payload, 'latency_ms')) DESC, tool_name ASC LIMIT 1`
	rows, err := s.db.Conn.Query(ctx, query, namespace, server, uid)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if !rows.Next() {
		return "", rows.Err()
	}
	var tool string
	if err := rows.Scan(&tool); err != nil {
		return "", err
	}
	return tool, rows.Err()
}

func fetchObservabilityMetric(ctx context.Context, query string) (*float64, string, error) {
	queryURL, err := prometheusQueryURL(prometheusAPIBaseURL(), query)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, queryURL, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := serviceutil.DoHTTP(nil, req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("prometheus returned status %d", resp.StatusCode)
	}
	var payload struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  []json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, prometheusErrorBodyLimit*4)).Decode(&payload); err != nil || payload.Status != "success" {
		return nil, "", errors.New("invalid prometheus response")
	}
	if len(payload.Data.Result) == 0 || len(payload.Data.Result[0].Value) < 2 {
		return nil, "", nil
	}
	var textValue string
	if err := json.Unmarshal(payload.Data.Result[0].Value[1], &textValue); err != nil {
		return nil, "", err
	}
	value, err := strconv.ParseFloat(textValue, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return nil, "", errors.New("invalid prometheus sample")
	}
	return &value, payload.Data.Result[0].Metric["tool"], nil
}

// HandleRuntimeObservabilityLogs exposes only the authenticated server's
// recent container logs. The LogQL selector is constructed from the resolved
// MCPServer identity; callers cannot supply arbitrary Loki queries.
func (s *RuntimeServer) HandleRuntimeObservabilityLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	_, target, err := s.authorizedObservabilityTarget(r)
	if err != nil {
		writeObservabilityError(w, err)
		return
	}

	query := fmt.Sprintf(`{namespace=%s,app=%s,server_uid=%s}`, promQLString(target.Namespace), promQLString(target.Name), promQLString(string(target.UID)))
	queryURL, err := lokiQueryURL(lokiAPIBaseURL(), query)
	if err != nil {
		log.Printf("observability loki proxy configuration error: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "logs not configured"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, queryURL, nil)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query_build_failed"})
		return
	}
	resp, err := serviceutil.DoHTTP(nil, req)
	if err != nil {
		log.Printf("observability loki query failed namespace=%q server=%q err=%v", target.Namespace, target.Name, err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "loki_unavailable"})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("observability loki query failed namespace=%q server=%q status=%d", target.Namespace, target.Name, resp.StatusCode)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "loki_query_failed"})
		return
	}
	var payload struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Stream map[string]string `json:"stream"`
				Values [][]string        `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, observabilityBodyLimit)).Decode(&payload); err != nil || payload.Status != "success" {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "loki_response_invalid"})
		return
	}
	type logEntry struct {
		Timestamp string `json:"timestamp"`
		Line      string `json:"line"`
		Container string `json:"container,omitempty"`
		Pod       string `json:"pod,omitempty"`
	}
	entries := make([]logEntry, 0, observabilityLogLimit)
	for _, stream := range payload.Data.Result {
		for _, value := range stream.Values {
			if len(value) < 2 {
				continue
			}
			entries = append(entries, logEntry{Timestamp: value[0], Line: value[1], Container: stream.Stream["container"], Pod: stream.Stream["pod"]})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Timestamp > entries[j].Timestamp })
	if len(entries) > observabilityLogLimit {
		entries = entries[:observabilityLogLimit]
	}
	writeJSON(w, http.StatusOK, map[string]any{"namespace": target.Namespace, "server": target.Name, "entries": entries})
}

func lokiAPIBaseURL() string {
	return strings.TrimSpace(envOr(envLokiAPIURL, defaultLokiAPIURL))
}

func lokiQueryURL(base, query string) (string, error) {
	base = strings.TrimSpace(base)
	if base == "" {
		return "", errors.New("Loki API URL is empty")
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("Loki API URL must be absolute http(s)")
	}
	joined, err := url.JoinPath(u.String(), "loki", "api", "v1", "query_range")
	if err != nil {
		return "", err
	}
	queryURL, err := url.Parse(joined)
	if err != nil {
		return "", err
	}
	values := queryURL.Query()
	values.Set("query", query)
	values.Set("limit", fmt.Sprint(observabilityLogLimit))
	values.Set("direction", "backward")
	values.Set("start", fmt.Sprint(time.Now().Add(-time.Hour).UnixNano()))
	values.Set("end", fmt.Sprint(time.Now().UnixNano()))
	queryURL.RawQuery = values.Encode()
	return queryURL.String(), nil
}

func (s *RuntimeServer) authorizedObservabilityTarget(r *http.Request) (principal, *mcpv1alpha1.MCPServer, error) {
	if s == nil {
		return principal{}, nil, observabilityRequestError{status: http.StatusServiceUnavailable, message: "runtime not available"}
	}
	control := s.controlPlane()
	if control == nil {
		return principal{}, nil, observabilityRequestError{status: http.StatusServiceUnavailable, message: "kubernetes not available"}
	}
	p, ok := principalFromContext(r.Context())
	if !ok {
		return principal{}, nil, observabilityRequestError{status: http.StatusUnauthorized, message: "unauthorized"}
	}
	namespace := strings.TrimSpace(r.URL.Query().Get("namespace"))
	serverName := strings.TrimSpace(r.URL.Query().Get("server"))
	if namespace == "" || serverName == "" {
		return principal{}, nil, observabilityRequestError{status: http.StatusBadRequest, message: "namespace and server are required"}
	}
	if p.Role != roleAdmin && !principalCanReadNamespace(p, namespace) {
		return principal{}, nil, observabilityRequestError{status: http.StatusNotFound, message: "server not found"}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	target, err := control.GetServer(ctx, namespace, serverName)
	if apierrors.IsNotFound(err) {
		return principal{}, nil, observabilityRequestError{status: http.StatusNotFound, message: "server not found"}
	}
	if err != nil {
		code, msg := observabilityTargetReadStatus(err)
		return principal{}, nil, observabilityRequestError{status: code, message: msg}
	}
	if !mcpServerObservableByPrincipal(*target, p) {
		return principal{}, nil, observabilityRequestError{status: http.StatusNotFound, message: "server not found"}
	}
	if target.UID == "" {
		return principal{}, nil, observabilityRequestError{status: http.StatusServiceUnavailable, message: "server identity not available"}
	}
	return p, target, nil
}

func observabilityTargetReadStatus(err error) (int, string) {
	if err == nil {
		return http.StatusNotFound, "server not found"
	}
	if apierrors.IsNotFound(err) {
		return http.StatusNotFound, "server not found"
	}
	code, msg := k8sclient.HTTPStatusFromK8sError(err)
	if code == http.StatusForbidden {
		return http.StatusNotFound, "server not found"
	}
	return code, msg
}

func writeObservabilityError(w http.ResponseWriter, err error) {
	var requestErr observabilityRequestError
	if errors.As(err, &requestErr) {
		writeJSON(w, requestErr.status, map[string]string{"error": requestErr.message})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "observability_failed"})
}

func observabilityLinksForMCPServer(server mcpv1alpha1.MCPServer, p principal, r *http.Request) observabilityLinksResponse {
	info := controlplane.ServerInfoFromMCPServer(server, controlplane.ServerDeploymentStatus{})
	return observabilityLinksForServerInfo(info, p, r)
}

func observabilityLinksForServerInfo(info controlplane.ServerInfo, p principal, r *http.Request) observabilityLinksResponse {
	queries := scopedPrometheusQueries(info.Namespace, info.Name, info.UID)
	grafana := grafanaLinkForServer(info, p)
	queryLinks := make([]observabilityPrometheusQueryLink, 0, len(queries))
	for _, query := range queries {
		apiPath := observabilityPrometheusAPIPath(r, info.Namespace, info.Name, query.ID)
		link := observabilityPrometheusQueryLink{
			ID:          query.ID,
			Name:        query.Name,
			Description: query.Description,
			URL:         publicAPIURL(r, apiPath),
			Query:       query.Query,
		}
		if grafana.Available && grafana.URL != "" {
			link.GrafanaURL = grafanaPanelURL(grafana.URL, query.PanelID)
		}
		queryLinks = append(queryLinks, link)
	}
	return observabilityLinksResponse{
		Namespace: info.Namespace,
		Server:    info.Name,
		TeamID:    strings.TrimSpace(info.TeamID),
		Prometheus: observabilityPrometheusSet{
			Queries:         queryLinks,
			DirectAdminOnly: true,
		},
		Grafana: grafana,
	}
}

func observabilityPrometheusAPIPath(r *http.Request, namespace, serverName, queryID string) string {
	values := url.Values{}
	values.Set("namespace", namespace)
	values.Set("server", serverName)
	values.Set("query_id", queryID)
	return observabilityRuntimeAPIPrefix(r) + "/observability/prometheus/query?" + values.Encode()
}

// observabilityRuntimeAPIPrefix returns the path prefix for observability
// links. Requests forwarded by the UI session proxy (x-mcp-source: ui) come
// from a browser, whose platform credentials live in the UI's HttpOnly session
// cookie, so their links go back through that proxy. Direct API callers get
// runtime-api paths they can call with their own API key or bearer token.
// The header only selects the link form; both paths enforce the same auth.
func observabilityRuntimeAPIPrefix(r *http.Request) string {
	if platformauth.RequestSource(r) == "ui" {
		return strings.TrimSuffix(envOr("UI_PATH_PREFIX", "/"), "/") + "/api/ui/v1/runtime"
	}
	return "/api/v1/runtime"
}

func publicAPIURL(r *http.Request, apiPath string) string {
	if !strings.HasPrefix(apiPath, "/") {
		apiPath = "/" + apiPath
	}
	host := forwardedHost(r)
	if host == "" {
		return apiPath
	}
	return forwardedScheme(r) + "://" + strings.TrimRight(host, "/") + apiPath
}

func grafanaLinkForServer(info controlplane.ServerInfo, p principal) observabilityGrafanaLink {
	if grafanaScopedAccessEnabled() {
		values := url.Values{"var-namespace": {info.Namespace}, "var-server": {info.Name}, "var-server_uid": {info.UID}}
		return observabilityGrafanaLink{Available: true, URL: strings.TrimSuffix(envOr("UI_GRAFANA_PATH", "/grafana"), "/") + "/d/mcp-server/mcp-server?" + values.Encode()}
	}
	template := strings.TrimSpace(envOr(envGrafanaServerDashboardURL, ""))
	if template == "" {
		if p.Role != roleAdmin {
			return observabilityGrafanaLink{
				DirectAdminOnly: true,
				Reason:          "Grafana requires an admin account unless tenant-aware dashboard access is configured",
			}
		}
		values := url.Values{}
		values.Set("var-namespace", info.Namespace)
		values.Set("var-server", info.Name)
		values.Set("var-server_uid", info.UID)
		return observabilityGrafanaLink{
			Available:       true,
			URL:             strings.TrimSuffix(envOr("UI_GRAFANA_PATH", "/grafana"), "/") + "/d/mcp-server/mcp-server?" + values.Encode(),
			DirectAdminOnly: true,
		}
	}
	link := expandObservabilityURLTemplate(template, info.Namespace, info.Name)
	if p.Role == roleAdmin {
		return observabilityGrafanaLink{
			Available:       true,
			URL:             link,
			DirectAdminOnly: true,
		}
	}
	return observabilityGrafanaLink{
		Available:       false,
		DirectAdminOnly: true,
		Reason:          "grafana requires tenant-aware access before user links are exposed",
	}
}

func grafanaPanelURL(dashboardURL string, panelID int) string {
	parsed, err := url.Parse(strings.TrimSpace(dashboardURL))
	if err != nil || panelID < 1 {
		return dashboardURL
	}
	values := parsed.Query()
	values.Set("viewPanel", fmt.Sprintf("%d", panelID))
	parsed.RawQuery = values.Encode()
	return parsed.String()
}

func readPrometheusErrorBody(body io.Reader) string {
	if body == nil {
		return ""
	}
	payload, err := io.ReadAll(io.LimitReader(body, prometheusErrorBodyLimit))
	if err != nil {
		return "<unreadable>"
	}
	return strings.TrimSpace(string(payload))
}

func expandObservabilityURLTemplate(template, namespace, serverName string) string {
	replacer := strings.NewReplacer(
		"{namespace}", url.QueryEscape(namespace),
		"{server}", url.QueryEscape(serverName),
	)
	return replacer.Replace(template)
}

func mcpServerObservableByPrincipal(server mcpv1alpha1.MCPServer, p principal) bool {
	if !mcpv1alpha1.GatewayIsEnabled(server.Spec.Gateway) {
		return false
	}
	if p.Role == roleAdmin {
		return true
	}
	if principalOwnsObservabilityNamespace(p, server.Namespace) {
		return true
	}
	return serverHasUserOwnerLabel(server.Labels, p)
}

func serverInfoObservableByPrincipal(info controlplane.ServerInfo, p principal) bool {
	if !info.GatewayEnabled {
		return false
	}
	if p.Role == roleAdmin {
		return true
	}
	if principalOwnsObservabilityNamespace(p, info.Namespace) {
		return true
	}
	return serverHasUserOwnerLabel(info.Labels, p)
}

func principalOwnsObservabilityNamespace(p principal, namespace string) bool {
	namespace = strings.TrimSpace(namespace)
	if namespace == "" || namespace == sharedCatalogNamespace || isModeCatalogNamespace(namespace) {
		return false
	}
	if strings.TrimSpace(p.Namespace) == namespace {
		return true
	}
	for _, team := range p.Teams {
		if strings.TrimSpace(team.Namespace) == namespace {
			return true
		}
	}
	for _, allowed := range p.AllowedNamespaces {
		if strings.TrimSpace(allowed) == namespace {
			return true
		}
	}
	return false
}

func serverHasUserOwnerLabel(labels map[string]string, p principal) bool {
	userID := strings.TrimSpace(p.UserID())
	if userID == "" {
		return false
	}
	return strings.TrimSpace(labels[platformUserIDLabel]) == userID
}

func scopedPrometheusQueryByID(queryID, namespace, serverName, uid string) (scopedPrometheusQuery, bool) {
	queryID = strings.TrimSpace(queryID)
	if queryID == "" {
		queryID = "request_rate"
	}
	// Preserve explicit old health API links without advertising the redundant
	// panel on cards/details or in the provisioned server dashboard.
	if queryID == "up" {
		return scopedPrometheusQuery{ID: "up", Name: "Target health", Query: "up{" + observabilityMetricSelector(namespace, serverName, uid) + "}"}, true
	}
	for _, query := range scopedPrometheusQueries(namespace, serverName, uid) {
		if query.ID == queryID {
			return query, true
		}
	}
	return scopedPrometheusQuery{}, false
}

func observabilityMetricSelector(namespace, serverName, uid string) string {
	return fmt.Sprintf(`namespace=%s,server=%s,server_uid=%s`, promQLString(namespace), promQLString(serverName), promQLString(uid))
}

func scopedPrometheusQueries(namespace, serverName, uid string) []scopedPrometheusQuery {
	selector := observabilityMetricSelector(namespace, serverName, uid)
	callSelector := selector + `,rpc_method="tools/call"`
	return []scopedPrometheusQuery{
		{
			ID:          "request_rate",
			Name:        "tools/call rate",
			Description: "One-hour tools/call rate for this server.",
			Query:       "sum(rate(mcp_gateway_requests_total{" + callSelector + "}[1h]))",
			PanelID:     2,
		},
		{
			ID:          "deny_rate",
			Name:        "Policy denies",
			Description: "One-hour policy denial count for this server.",
			Query:       `sum(increase(mcp_gateway_policy_decisions_total{` + selector + `,decision="deny"}[1h]))`,
			PanelID:     3,
		},
		{
			ID:          "latency_p95",
			Name:        "tools/call p95",
			Description: "One-hour tools/call p95 gateway latency for this server.",
			Query:       "histogram_quantile(0.95, sum by (le) (rate(mcp_gateway_request_duration_seconds_bucket{" + callSelector + "}[1h])))",
			PanelID:     4,
		},
		{
			ID: "upstream_5xx_rate", Name: "Upstream 5xx rate", Description: "One-hour allowed tools/call upstream HTTP 5xx rate.",
			Query: "sum(rate(mcp_gateway_requests_total{" + callSelector + `,decision="allow",status=~"5.."}[1h]))`, PanelID: 5,
		},
	}
}

func promQLString(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`
}

func prometheusAPIBaseURL() string {
	if value := strings.TrimSpace(envOr(envMCPPrometheusAPIURL, "")); value != "" {
		return value
	}
	if value := strings.TrimSpace(envOr(envPrometheusAPIURL, "")); value != "" {
		return value
	}
	return defaultPrometheusAPIURL
}

func prometheusQueryURL(base, query string) (string, error) {
	base = strings.TrimSpace(base)
	if base == "" {
		return "", errors.New("prometheus API URL is empty")
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("prometheus API URL must be absolute http(s)")
	}
	joined, err := url.JoinPath(u.String(), "api", "v1", "query")
	if err != nil {
		return "", err
	}
	queryURL, err := url.Parse(joined)
	if err != nil {
		return "", err
	}
	values := queryURL.Query()
	values.Set("query", query)
	queryURL.RawQuery = values.Encode()
	return queryURL.String(), nil
}
