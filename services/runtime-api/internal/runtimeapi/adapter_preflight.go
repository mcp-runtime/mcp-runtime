package runtimeapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"mcp-runtime/pkg/serviceutil"
)

var clusterIssuerGVR = schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "clusterissuers"}

type adapterPreflightRequest struct {
	ServerName string `json:"serverName"`
	Namespace  string `json:"namespace,omitempty"`
	AgentID    string `json:"agentID"`
}

// HandleAdapterSessionItemPath deletes only a newly-created session whose
// certificate enrollment is still pending and whose owner is the caller.
func (s *AccessService) HandleAdapterSessionItemPath(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", http.MethodDelete)
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s == nil || s.accessMgr == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "kubernetes not available")
		return
	}
	namespace, name, err := serviceutil.ExtractNamespacedResourceDelete(r, "/api/runtime/adapter/sessions/")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	p, ok := principalFromContext(r.Context())
	if !ok {
		writeAPIError(w, http.StatusUnauthorized, "no principal on request")
		return
	}
	humanID := strings.TrimSpace(p.Subject)
	if humanID == "" {
		humanID = strings.TrimSpace(p.Email)
	}
	ctx := r.Context()
	session, err := s.accessMgr.GetSession(ctx, name, namespace)
	if err != nil || session == nil {
		writeAPIError(w, http.StatusNotFound, "pending adapter session not found")
		return
	}
	if humanID == "" || humanID != string(session.Spec.Subject.HumanID) {
		writeAPIError(w, http.StatusForbidden, "adapter session is not owned by the authenticated principal")
		return
	}
	if session.Annotations[adapterEnrollmentPendingAnnotation] != "true" {
		writeAPIError(w, http.StatusConflict, "adapter session is no longer pending certificate enrollment")
		return
	}
	if err := s.accessMgr.DeleteSession(ctx, name, namespace); err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "failed to delete pending adapter session", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "namespace": namespace, "name": name})
}

type adapterPreflightResponse struct {
	Ready               bool   `json:"ready"`
	AdapterCertificates bool   `json:"adapterCertificatesEnabled"`
	Issuer              string `json:"issuer,omitempty"`
	IssuerReady         bool   `json:"issuerReady"`
	TrustDomain         string `json:"trustDomain,omitempty"`
	RouteEligible       bool   `json:"routeEligible"`
	RouteReady          bool   `json:"routeReady"`
	Reason              string `json:"reason,omitempty"`
}

// HandleAdapterPreflight reports whether the selected server can use the
// configured adapter-certificate path. It performs reads only; enrollment
// still rechecks every condition before creating a session.
func (s *AccessService) HandleAdapterPreflight(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s == nil || s.accessMgr == nil || s.k8sClients == nil || s.k8sClients.Dynamic == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "kubernetes not available")
		return
	}
	var req adapterPreflightRequest
	r.Body = http.MaxBytesReader(w, r.Body, adapterSessionRequestMaxBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBodyDecodeError(w, err)
		return
	}
	req.ServerName = strings.TrimSpace(req.ServerName)
	req.Namespace = strings.TrimSpace(req.Namespace)
	req.AgentID = strings.TrimSpace(req.AgentID)
	if req.ServerName == "" || req.AgentID == "" {
		writeAPIError(w, http.StatusBadRequest, "serverName and agentID are required")
		return
	}
	p, ok := principalFromContext(r.Context())
	if !ok {
		writeAPIError(w, http.StatusUnauthorized, "no principal on request")
		return
	}
	if req.Namespace == "" {
		req.Namespace = strings.TrimSpace(p.Namespace)
	}
	if req.Namespace == "" {
		writeAPIError(w, http.StatusBadRequest, "namespace is required")
		return
	}
	humanID := strings.TrimSpace(p.Subject)
	if humanID == "" {
		humanID = strings.TrimSpace(p.Email)
	}
	if humanID == "" {
		writeAPIError(w, http.StatusUnauthorized, "principal has no subject or email")
		return
	}
	teams := adapterPrincipalTeamIDs(p)
	if s.identity == nil || !s.identity.Configured() {
		writeAPIError(w, http.StatusServiceUnavailable, "platform identity is unavailable for adapter validation")
		return
	}
	ctx := r.Context()
	agent, found, err := s.identity.GetAgent(ctx, req.AgentID)
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "failed to validate adapter agent")
		return
	}
	if !found || strings.TrimSpace(agent.ID) != req.AgentID || strings.TrimSpace(agent.Status) != "active" {
		writeAPIError(w, http.StatusForbidden, "adapter agent is unknown or inactive")
		return
	}
	if !containsString(teams, strings.TrimSpace(agent.TeamID)) {
		writeAPIError(w, http.StatusForbidden, "adapter agent does not belong to a caller team")
		return
	}
	if _, _, err := s.selectAdapterGrant(ctx, req.Namespace, req.ServerName, humanID, req.AgentID, teams, defaultAdapterSessionTeamID(p, req.Namespace, teams), false); err != nil {
		writeAPIError(w, http.StatusForbidden, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.adapterCertificateReadiness(ctx, req.Namespace, req.ServerName))
}

func (s *AccessService) adapterCertificateReadiness(ctx context.Context, namespace, serverName string) adapterPreflightResponse {
	issuer := strings.TrimSpace(os.Getenv("MCP_MTLS_CLUSTER_ISSUER"))
	trustDomain := strings.TrimSpace(os.Getenv("MCP_TRUST_DOMAIN"))
	enabled, _ := strconv.ParseBool(strings.TrimSpace(os.Getenv("MCP_ADAPTER_CERTIFICATES")))
	state := adapterPreflightResponse{AdapterCertificates: enabled, Issuer: issuer, TrustDomain: trustDomain}
	if s == nil || s.k8sClients == nil || s.k8sClients.Dynamic == nil {
		state.Reason = "kubernetes is unavailable for adapter certificate readiness checks"
		return state
	}
	switch {
	case !enabled:
		state.Reason = "adapter certificate ingress is disabled (MCP_ADAPTER_CERTIFICATES is not true)"
		return state
	case issuer == "":
		state.Reason = "workload certificate issuer is not configured"
		return state
	case trustDomain == "":
		state.Reason = "platform SPIFFE trust domain is not configured"
		return state
	}
	obj, err := s.k8sClients.Dynamic.Resource(clusterIssuerGVR).Get(ctx, issuer, metav1.GetOptions{})
	if err != nil || !conditionTrue(obj, "Ready") {
		state.Reason = "configured workload issuer is not Ready"
		return state
	}
	state.IssuerReady = true
	server, err := s.k8sClients.Dynamic.Resource(adapterMCPServerGVR).Namespace(namespace).Get(ctx, serverName, metav1.GetOptions{})
	if err != nil {
		state.Reason = "selected MCP server route is unavailable"
		return state
	}
	state.RouteEligible = adapterServerRouteEligible(server)
	if !state.RouteEligible {
		state.Reason = "selected MCP server does not use a supported client-certificate gateway route"
		return state
	}
	state.RouteReady, _, _ = unstructured.NestedBool(server.Object, "status", "ingressReady")
	if !state.RouteReady {
		state.Reason = "selected MCP server route is not Ready; wait for gateway and ingress reconciliation"
		return state
	}
	state.Ready = true
	return state
}

func adapterServerRouteEligible(server *unstructured.Unstructured) bool {
	if server == nil {
		return false
	}
	gatewayEnabled, found, _ := unstructured.NestedBool(server.Object, "spec", "gateway", "enabled")
	if !found {
		gatewayEnabled = true
	}
	if !gatewayEnabled {
		return false
	}
	mode, _, _ := unstructured.NestedString(server.Object, "spec", "auth", "mode")
	if strings.EqualFold(strings.TrimSpace(mode), "header") {
		return false
	}
	ingressClass, _, _ := unstructured.NestedString(server.Object, "spec", "ingressClass")
	return strings.TrimSpace(ingressClass) == "" || strings.TrimSpace(ingressClass) == "traefik"
}

func conditionTrue(object *unstructured.Unstructured, conditionType string) bool {
	if object == nil {
		return false
	}
	conditions, _, _ := unstructured.NestedSlice(object.Object, "status", "conditions")
	for _, item := range conditions {
		condition, ok := item.(map[string]any)
		if !ok {
			continue
		}
		typ, _, _ := unstructured.NestedString(condition, "type")
		status, _, _ := unstructured.NestedString(condition, "status")
		if typ == conditionType && status == "True" {
			return true
		}
	}
	return false
}
