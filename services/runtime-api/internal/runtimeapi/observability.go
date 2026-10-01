package runtimeapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	"mcp-runtime/pkg/controlplane"
	"mcp-runtime/pkg/k8sclient"
	"mcp-runtime/pkg/platformauth"
)

const (
	defaultPrometheusAPIURL  = "http://prometheus.mcp-observability.svc:9090/prometheus"
	prometheusErrorBodyLimit = 1024

	envPrometheusAPIURL          = "PROMETHEUS_API_URL"
	envMCPPrometheusAPIURL       = "MCP_PROMETHEUS_API_URL"
	envGrafanaServerDashboardURL = "GRAFANA_SERVER_DASHBOARD_URL"
	envGrafanaScopedUserAccess   = "GRAFANA_SCOPED_USER_ACCESS"
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
	query, ok := scopedPrometheusQueryByID(queryID, target.Namespace, target.Name)
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
	resp, err := http.DefaultClient.Do(req)
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
	queries := scopedPrometheusQueries(info.Namespace, info.Name)
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
		return "/api/ui/v1/runtime"
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
		return observabilityGrafanaLink{
			Available:       true,
			URL:             "/grafana/d/mcp-server/mcp-server?" + values.Encode(),
			DirectAdminOnly: true,
		}
	}
	link := expandObservabilityURLTemplate(template, info.Namespace, info.Name)
	if p.Role == roleAdmin || grafanaScopedUserAccessEnabled() {
		return observabilityGrafanaLink{
			Available:       true,
			URL:             link,
			DirectAdminOnly: !grafanaScopedUserAccessEnabled(),
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

func grafanaScopedUserAccessEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(envOr(envGrafanaScopedUserAccess, ""))) {
	case "1", "true", "yes", "on", "enabled":
		return true
	default:
		return false
	}
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

func scopedPrometheusQueryByID(queryID, namespace, serverName string) (scopedPrometheusQuery, bool) {
	queryID = strings.TrimSpace(queryID)
	if queryID == "" {
		queryID = "up"
	}
	for _, query := range scopedPrometheusQueries(namespace, serverName) {
		if query.ID == queryID {
			return query, true
		}
	}
	return scopedPrometheusQuery{}, false
}

func scopedPrometheusQueries(namespace, serverName string) []scopedPrometheusQuery {
	selector := fmt.Sprintf(`namespace=%s,server=%s`, promQLString(namespace), promQLString(serverName))
	return []scopedPrometheusQuery{
		{
			ID:          "up",
			Name:        "Target health",
			Description: "Prometheus scrape health for this MCP server scope.",
			Query:       "up{" + selector + "}",
			PanelID:     1,
		},
		{
			ID:          "request_rate",
			Name:        "Request rate",
			Description: "Five-minute MCP gateway request rate for this server.",
			Query:       "sum(rate(mcp_gateway_requests_total{" + selector + "}[5m]))",
			PanelID:     2,
		},
		{
			ID:          "deny_rate",
			Name:        "Deny rate",
			Description: "Five-minute policy denial rate for this server.",
			Query:       `sum(rate(mcp_gateway_policy_decisions_total{` + selector + `,decision="deny"}[5m]))`,
			PanelID:     3,
		},
		{
			ID:          "latency_p95",
			Name:        "p95 latency",
			Description: "Five-minute p95 gateway latency for this server.",
			Query:       "histogram_quantile(0.95, sum(rate(mcp_gateway_request_duration_seconds_bucket{" + selector + "}[5m])) by (le))",
			PanelID:     4,
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
