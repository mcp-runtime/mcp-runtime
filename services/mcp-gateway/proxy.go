package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"mcp-runtime/pkg/events"
	policypkg "mcp-runtime/pkg/policy"
	"mcp-runtime/pkg/serviceutil"
)

const upstreamResponseHeaderTimeout = 30 * time.Second

func newUpstreamReverseProxy(target *url.URL) *httputil.ReverseProxy {
	return newUpstreamReverseProxyWithTimeout(target, upstreamResponseHeaderTimeout)
}

func newUpstreamReverseProxyWithTimeout(target *url.URL, responseHeaderTimeout time.Duration) *httputil.ReverseProxy {
	proxy := &httputil.ReverseProxy{
		Rewrite: func(req *httputil.ProxyRequest) {
			req.SetURL(target)
			req.Out.Host = target.Host
			req.SetXForwarded()
		},
		Transport: newUpstreamTransport(responseHeaderTimeout),
	}
	return proxy
}

func newUpstreamTransport(responseHeaderTimeout time.Duration) http.RoundTripper {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = responseHeaderTimeout
	return otelhttp.NewTransport(transport)
}

func handleUpstreamError(w http.ResponseWriter, r *http.Request, err error) {
	logMessage := "gateway upstream error"
	status := http.StatusBadGateway
	errorCode := "upstream_error"
	message := "upstream error"
	if isUpstreamTimeout(err) {
		logMessage = "gateway upstream response timeout"
		status = http.StatusGatewayTimeout
		errorCode = "upstream_timeout"
		message = "upstream response timeout"
	}
	serviceutil.RecordSpanFailure(r.Context(), "gateway.upstream", errorCode, status, err)
	serviceutil.LogfCtx(r.Context(), "%s: %v", logMessage, err)

	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":   errorCode,
		"message": message,
	})
}

func isUpstreamTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// handleGateway is the pipeline orchestrator for the gateway request path.
// It runs the five ordered filters (inspect → policy → auth → authz → upstream)
// and then unconditionally runs stage 6 (audit/analytics finalization).
func (s *gatewayServer) handleGateway(w http.ResponseWriter, r *http.Request) {
	ex := newExchange(w, r, s.defaultPolicyVersion)

	policy, _ := s.currentPolicy()
	scope := s.metricScope(policy)
	stopInflight := s.metrics.trackInflight(scope)
	defer stopInflight()
	policyDecisionObserved := false
	defer func() {
		metricScope := s.metricScope(ex.Policy)
		s.metrics.recordRequest(
			metricScope, ex.R, ex.Inspection.Method, ex.Decision,
			ex.W.status, time.Since(ex.StartTime), ex.R.ContentLength, ex.W.bytes,
		)
		if policyDecisionObserved {
			s.metrics.recordPolicyDecision(metricScope, ex.Inspection.Method, ex.Decision)
		}
	}()

	for _, f := range s.buildPipeline() {
		if f.Handle(ex) != Continue {
			break
		}
	}
	if ex.Decision.PolicyVersion == "" {
		ex.Decision.PolicyVersion = s.defaultPolicyVersion
	}
	policyDecisionObserved = ex.Inspection.ToolCall || ex.Inspection.Indeterminate
	if !policyDecisionObserved && !ex.Decision.Allowed && !ex.SkipAudit {
		policyDecisionObserved = true
	}
	s.recordGatewayOutcome(ex)
	s.emitAuditFromExchange(ex)
}

// recordGatewayOutcome marks the request span as failed and writes a
// trace-correlated log line for denied or failed requests. Only the bounded
// decision reason, RPC method, and status are recorded: never tokens, headers,
// tool arguments, or bodies. Upstream failures are recorded by
// handleUpstreamError.
func (s *gatewayServer) recordGatewayOutcome(ex *Exchange) {
	status := ex.W.status
	denied := !ex.Decision.Allowed && !ex.SkipAudit
	if !denied && status < http.StatusBadRequest {
		return
	}
	if status >= http.StatusInternalServerError && ex.Decision.Allowed {
		return
	}
	reason := ex.Decision.Reason
	if reason == "" {
		reason = http.StatusText(status)
	}
	method := ex.Inspection.Method
	if method == "" {
		method = ex.R.Method
	}
	ctx := ex.R.Context()
	serviceutil.RecordSpanFailure(ctx, "gateway.request", reason, status, nil)
	serviceutil.LogfCtx(ctx, "gateway request rejected method=%q status=%d reason=%q duration=%s",
		method, status, reason, time.Since(ex.StartTime))
}

// buildPipeline returns the fixed ordered filter slice for a gateway request.
// Stages are defined in exchange.go; order is security-sensitive and must not
// be changed without updating the ordering guarantees documented there.
func (s *gatewayServer) buildPipeline() []Filter {
	return []Filter{
		FilterFunc(s.inspectFilter),
		FilterFunc(s.policyFilter),
		FilterFunc(s.authFilter),
		FilterFunc(s.authzFilter),
		FilterFunc(s.upstreamFilter),
	}
}

// emitAuditFromExchange is stage 6 of the pipeline. It emits a single audit
// event after the pipeline completes, preserving the following semantics from
// the previous monolithic handleGateway:
//
//   - Allowed requests: audit when rpcMethod is non-empty (actual RPC traffic).
//   - Denied requests: audit when rpcMethod is non-empty OR the request was a
//     genuine MCP client attempt (application/json body that failed parsing).
//   - Internal platform service probes (mcp-runtime-live-inventory) are never audited.
func (s *gatewayServer) emitAuditFromExchange(ex *Exchange) {
	if ex.SkipAudit {
		return
	}
	if ex.Identity.AgentID == "mcp-runtime-live-inventory" {
		return
	}
	rpcMethod := ex.Inspection.Method
	if ex.Decision.Allowed {
		if rpcMethod == "" {
			return
		}
	} else if rpcMethod == "" && !ex.Inspection.IsRPCAttempt {
		return
	}
	s.emitAuditEvent(
		ex.R, ex.OriginalPath, rpcMethod, ex.Inspection.ToolName,
		ex.Identity, ex.Policy, ex.Decision,
		ex.W.status, time.Since(ex.StartTime).Milliseconds(), ex.W.bytes,
	)
}

// writeDeniedResponse writes the HTTP denial response for the current exchange.
// Audit emission is intentionally absent here; it is centralised in
// emitAuditFromExchange (stage 6) so each denial is audited exactly once.
func (s *gatewayServer) writeDeniedResponse(ex *Exchange) {
	ex.W.Header().Set("content-type", "application/json")
	if shouldChallengeOAuth(ex.Policy, ex.Decision) {
		ex.W.Header().Set("www-authenticate", oauthAuthenticateHeader(
			ex.Policy, ex.Decision.Reason, ex.Inspection.ToolName, ex.Decision,
		))
	}
	status := gatewayDeniedStatus(ex.Policy, ex.Decision)
	ex.Decision.Status = status
	ex.W.WriteHeader(status)
	_ = json.NewEncoder(ex.W).Encode(gatewayDeniedPayload(ex.Policy, ex.Decision))
}

func gatewayDeniedStatus(policy *policypkg.Document, decision policypkg.Decision) int {
	if decision.Status > 0 {
		return decision.Status
	}
	return http.StatusForbidden
}

func gatewayDeniedPayload(_ *policypkg.Document, decision policypkg.Decision) map[string]any {
	return map[string]any{"error": decision.Reason}
}

func (s *gatewayServer) emitAuditEvent(
	r *http.Request,
	path, rpcMethod, toolName string,
	authCtx identityContext,
	policy *policypkg.Document,
	decision policypkg.Decision,
	status int,
	latencyMs int64,
	bytesOut int,
) {
	envelope, err := events.NewEnvelope(
		s.source,
		s.eventType,
		s.auditPayload(r, path, rpcMethod, toolName, authCtx, policy, decision, status, latencyMs, bytesOut),
		time.Now().UTC(),
	)
	if err != nil {
		return
	}
	s.emitIfEnabled(r.Context(), envelope)
}

func (s *gatewayServer) auditPayload(
	r *http.Request,
	path, rpcMethod, toolName string,
	authCtx identityContext,
	policy *policypkg.Document,
	decision policypkg.Decision,
	status int,
	latencyMs int64,
	bytesOut int,
) map[string]any {
	payload := map[string]any{
		"method":           r.Method,
		"path":             path,
		"status":           status,
		"latency_ms":       latencyMs,
		"bytes_in":         maxInt64(r.ContentLength, 0),
		"bytes_out":        bytesOut,
		"server":           policypkg.FirstNonEmpty(policypkg.PolicyServerName(policy), s.serverName),
		"namespace":        policypkg.FirstNonEmpty(policypkg.PolicyServerNamespace(policy), s.serverNamespace),
		"team_id":          policypkg.PolicyServerTeamID(policy),
		"resource_team_id": policypkg.PolicyServerTeamID(policy),
		"cluster":          policypkg.FirstNonEmpty(policypkg.PolicyServerCluster(policy), s.clusterName),
		"human_id":         authCtx.HumanID,
		"agent_id":         authCtx.AgentID,
		"subject_team_id":  authCtx.TeamID,
		"session_id":       authCtx.SessionID,
		"decision":         ternary(decision.Allowed, "allow", "deny"),
		"reason":           decision.Reason,
		"policy_version":   policypkg.FirstNonEmpty(decision.PolicyVersion, s.defaultPolicyVersion),
	}
	if policypkg.UsesDelegatedHeaders(policy) {
		payload["auth_delegation"] = "upstream_header"
		payload["caller_identity"] = "unverified"
	}
	if rpcMethod != "" {
		payload["rpc_method"] = rpcMethod
	}
	if toolName != "" {
		payload["tool_name"] = toolName
	}
	if decision.MatchedGrant != "" {
		payload["matched_grant"] = decision.MatchedGrant
		if decision.MatchedGrantNamespace != "" {
			payload["matched_grant_namespace"] = decision.MatchedGrantNamespace
		}
	}
	if decision.MatchedSession != "" {
		payload["matched_session"] = decision.MatchedSession
		if decision.MatchedSessionNamespace != "" {
			payload["matched_session_namespace"] = decision.MatchedSessionNamespace
		}
	}
	if decision.RequiredTrust != "" {
		payload["required_trust"] = decision.RequiredTrust
	}
	if decision.RequiredSideEffect != "" {
		payload["required_side_effect"] = decision.RequiredSideEffect
	}
	if riskLevel := policypkg.FirstNonEmpty(decision.RiskLevel, policypkg.ToolRiskLevel(policy, toolName)); riskLevel != "" {
		payload["risk_level"] = riskLevel
	}
	if decision.AdminTrust != "" {
		payload["admin_trust"] = decision.AdminTrust
	}
	if decision.ConsentedTrust != "" {
		payload["consented_trust"] = decision.ConsentedTrust
	}
	if decision.EffectiveTrust != "" {
		payload["effective_trust"] = decision.EffectiveTrust
	}
	return payload
}
func absoluteRequestURL(r *http.Request, requestPath string) string {
	path := normalizeURLPath(requestPath)
	if r == nil {
		return path
	}

	host := ""
	if strings.TrimSpace(r.Host) != "" {
		host = strings.TrimSpace(r.Host)
	}
	if host == "" && r.URL != nil && strings.TrimSpace(r.URL.Host) != "" {
		host = strings.TrimSpace(r.URL.Host)
	}
	if host == "" {
		return path
	}

	scheme := "http"
	if r.URL != nil && r.URL.Scheme != "" {
		scheme = r.URL.Scheme
	} else if r.TLS != nil {
		scheme = "https"
	}

	return (&url.URL{
		Scheme: scheme,
		Host:   host,
		Path:   path,
	}).String()
}

func parseExternalBaseURL(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("must be an absolute URL")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed, nil
}

func (s *gatewayServer) publicRequestURL(r *http.Request, requestPath string) string {
	if s.externalBaseURL != nil {
		return resolveBaseURLPath(s.externalBaseURL, requestPath)
	}
	return absoluteRequestURL(r, requestPath)
}

func resolveBaseURLPath(base *url.URL, requestPath string) string {
	if base == nil {
		return normalizeURLPath(requestPath)
	}
	resolved := *base
	resolved.Path = path.Join(strings.TrimRight(base.Path, "/"), normalizeURLPath(requestPath))
	if !strings.HasPrefix(resolved.Path, "/") {
		resolved.Path = "/" + resolved.Path
	}
	resolved.RawPath = ""
	return resolved.String()
}

func normalizeURLPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "/"
	}
	cleaned := path.Clean(value)
	if cleaned == "." {
		return "/"
	}
	if !strings.HasPrefix(cleaned, "/") {
		cleaned = "/" + cleaned
	}
	return cleaned
}

func ternary(condition bool, truthy, falsy string) string {
	if condition {
		return truthy
	}
	return falsy
}

func trimRequestPathPrefix(value, prefix string) (string, bool) {
	prefix = strings.TrimSpace(prefix)
	prefix = strings.TrimRight(prefix, "/")
	if prefix == "" {
		return value, false
	}
	if value != prefix && !strings.HasPrefix(value, prefix+"/") {
		return value, false
	}
	return strings.TrimPrefix(value, prefix), true
}
func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// Write records response data and updates byte count.
func (r *statusRecorder) Write(data []byte) (int, error) {
	n, err := r.ResponseWriter.Write(data)
	r.bytes += n
	return n, err
}

// Flush forwards flush calls to the underlying ResponseWriter.
func (r *statusRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Hijack forwards hijack calls to the underlying ResponseWriter.
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("hijacker not supported")
	}
	return hijacker.Hijack()
}

// Push forwards HTTP/2 server push calls to the underlying ResponseWriter.
func (r *statusRecorder) Push(target string, opts *http.PushOptions) error {
	if pusher, ok := r.ResponseWriter.(http.Pusher); ok {
		return pusher.Push(target, opts)
	}
	return http.ErrNotSupported
}

func maxInt64(value, fallback int64) int64 {
	if value < 0 {
		return fallback
	}
	return value
}
