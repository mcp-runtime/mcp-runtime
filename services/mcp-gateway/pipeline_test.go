package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"

	policypkg "mcp-runtime/pkg/policy"
)

// ---- helpers ----------------------------------------------------------------

func newTestExchange(method, target, body string, headers map[string]string) *Exchange {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	r.ContentLength = int64(len(body))
	ex := &Exchange{
		W:            &statusRecorder{ResponseWriter: httptest.NewRecorder(), status: http.StatusOK},
		R:            r,
		OriginalPath: r.URL.Path,
		StartTime:    time.Now(),
		Decision: policypkg.Decision{
			Allowed:       true,
			Status:        http.StatusOK,
			Reason:        "allowed",
			PolicyVersion: "test",
		},
	}
	return ex
}

func minimalServer() *gatewayServer {
	return &gatewayServer{
		defaultPolicyMode:     defaultPolicyMode,
		defaultPolicyDecision: defaultPolicyDecision,
		defaultPolicyVersion:  "test",
		oauthProviders:        map[string]*oauthProvider{},
	}
}

// ---- stage 1: inspectFilter -------------------------------------------------

func TestInspectFilterAlwaysContinues(t *testing.T) {
	t.Parallel()
	s := minimalServer()

	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		ex := newTestExchange(method, "/mcp", "", nil)
		if got := s.inspectFilter(ex); got != Continue {
			t.Errorf("inspectFilter(%s) = %v, want Continue", method, got)
		}
	}
}

func TestInspectFilterSetsInspection(t *testing.T) {
	t.Parallel()
	s := minimalServer()
	body := `{"method":"tools/call","params":{"name":"echo"}}`
	ex := newTestExchange(http.MethodPost, "/mcp", body, map[string]string{"Content-Type": "application/json"})

	s.inspectFilter(ex)

	if ex.Inspection.Method != "tools/call" {
		t.Fatalf("Method = %q, want tools/call", ex.Inspection.Method)
	}
	if ex.Inspection.ToolName != "echo" {
		t.Fatalf("ToolName = %q, want echo", ex.Inspection.ToolName)
	}
	if !ex.Inspection.ToolCall {
		t.Fatal("ToolCall = false, want true")
	}
}

// ---- stage 2: policyFilter --------------------------------------------------

func TestPolicyFilterContinuesForNonOAuthPath(t *testing.T) {
	t.Parallel()
	s := minimalServer()
	s.snapshotPolicy(policySnapshot{Policy: testPolicy()})
	ex := newTestExchange(http.MethodPost, "/mcp", `{}`, map[string]string{"Content-Type": "application/json"})

	if got := s.policyFilter(ex); got != Continue {
		t.Fatalf("policyFilter = %v, want Continue", got)
	}
	if ex.Policy == nil {
		t.Fatal("Policy not set after policyFilter")
	}
}

func TestPolicyFilterRespondsForOAuthMetadataPath(t *testing.T) {
	t.Parallel()
	s := minimalServer()
	issuer := newTestJWTIssuer(t)
	s.snapshotPolicy(policySnapshot{Policy: oauthPolicy(issuer.url)})
	ex := newTestExchange(http.MethodGet, "/.well-known/oauth-protected-resource", "", nil)

	if got := s.policyFilter(ex); got != Respond {
		t.Fatalf("policyFilter for OAuth metadata path = %v, want Respond", got)
	}
	recorder := ex.W.ResponseWriter.(*httptest.ResponseRecorder)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
}

func TestPolicyFilterSetsSkipAuditForOAuthMetadataPath(t *testing.T) {
	t.Parallel()
	// Verify that an OAuth metadata early-exit sets SkipAudit so the
	// orchestrator does not emit a spurious audit event with no identity.
	s := minimalServer()
	issuer := newTestJWTIssuer(t)
	s.snapshotPolicy(policySnapshot{Policy: oauthPolicy(issuer.url)})
	ex := newTestExchange(http.MethodGet, "/.well-known/oauth-protected-resource", "", nil)

	s.policyFilter(ex)

	if !ex.SkipAudit {
		t.Fatal("SkipAudit = false after OAuth metadata early-exit, want true")
	}
}

func TestPolicyFilterPolicySnapshotIsImmutableForExchange(t *testing.T) {
	t.Parallel()
	s := minimalServer()
	snap := testPolicy()
	s.snapshotPolicy(policySnapshot{Policy: snap})
	ex := newTestExchange(http.MethodPost, "/mcp", "", nil)

	s.policyFilter(ex)
	captured := ex.Policy

	// Replace the gateway snapshot mid-exchange — the Exchange copy must be unaffected.
	s.snapshotPolicy(policySnapshot{Policy: &policypkg.Document{Server: policypkg.Server{Name: "other"}}})
	if ex.Policy != captured {
		t.Fatal("Exchange.Policy changed after snapshot was replaced — not immutable for exchange lifetime")
	}
}

// ---- stage 3: authFilter ----------------------------------------------------

func TestAuthFilterRejectsOAuthMissingBearer(t *testing.T) {
	t.Parallel()
	issuer := newTestJWTIssuer(t)
	s := minimalServer()
	s.httpClient = issuer.server.Client()
	ex := newTestExchange(http.MethodPost, "/mcp", `{}`, map[string]string{"Content-Type": "application/json"})
	ex.Policy = oauthPolicy(issuer.url)

	if got := s.authFilter(ex); got != Reject {
		t.Fatalf("authFilter OAuth no bearer = %v, want Reject", got)
	}
	recorder := ex.W.ResponseWriter.(*httptest.ResponseRecorder)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	if ex.Decision.Allowed {
		t.Fatal("Decision.Allowed = true after auth rejection, want false")
	}
}

// verifiedProxyTLS builds a ConnectionState representing a verified mTLS hop
// from the ingress. peerURIs are the URI SANs on the ingress's client cert
// (used for trusted-proxy pinning); pass none when pinning is disabled.
func verifiedProxyTLS(peerURIs ...string) *tls.ConnectionState {
	cert := &x509.Certificate{}
	for _, u := range peerURIs {
		if parsed, err := url.Parse(u); err == nil {
			cert.URIs = append(cert.URIs, parsed)
		}
	}
	return &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{cert},
		VerifiedChains:   [][]*x509.Certificate{{cert}},
	}
}

func TestAuthFilterAdapterRequiresMatchingOAuthAndUsesCertificateIdentity(t *testing.T) {
	t.Parallel()
	issuer := newTestJWTIssuer(t)
	s := minimalServer()
	s.httpClient = issuer.server.Client()
	ex := newTestExchange(http.MethodPost, "/mcp", `{}`, map[string]string{
		// Identity comes only from the ingress-injected verified SPIFFE header.
		defaultVerifiedSPIFFEHeader: "spiffe://example.org/ns/team-a/session/session-1",
	})
	ex.R.TLS = verifiedProxyTLS()
	ex.Policy = oauthPolicy(issuer.url)
	ex.Policy.Auth.TrustDomain = "example.org"
	ex.Policy.Sessions = []policypkg.Binding{{
		Name:      "session-1",
		Namespace: "team-a",
		HumanID:   "human-1",
		AgentID:   "agent-1",
		TeamID:    "team-1",
	}}
	token := issuer.sign(t, jwt.MapClaims{
		"iss": issuer.url, "aud": "http://proxy.example.com/mcp", "sub": "human-1",
		"azp": "oauth-client-1", "team_id": "team-1", "sid": "oauth-session-1",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	ex.R.Header.Set("Authorization", "Bearer "+token)

	if got := s.authFilter(ex); got != Continue {
		t.Fatalf("authFilter adapter = %v, want Continue", got)
	}
	if ex.Identity.HumanID != "human-1" || ex.Identity.AgentID != "agent-1" || ex.Identity.SessionID != "session-1" {
		t.Fatalf("identity = %#v, want rendered session identity", ex.Identity)
	}
	if ex.OAuthToken != token {
		t.Fatal("validated OAuth token was not retained for the upstream MCP resource")
	}
}

func TestAuthFilterAdapterCertificateOnlyWhenOAuthDisabled(t *testing.T) {
	t.Parallel()
	s := minimalServer()
	ex := newTestExchange(http.MethodPost, "/mcp", `{}`, map[string]string{
		defaultVerifiedSPIFFEHeader: "spiffe://example.org/ns/team-a/session/session-1",
	})
	ex.R.TLS = verifiedProxyTLS()
	ex.Policy = &policypkg.Document{
		Auth: &policypkg.Auth{TrustDomain: "example.org"},
		Sessions: []policypkg.Binding{{
			Name: "session-1", Namespace: "team-a", HumanID: "human-1", AgentID: "agent-1",
		}},
	}
	if got := s.authFilter(ex); got != Continue {
		t.Fatalf("authFilter adapter without OAuth = %v, want Continue", got)
	}
	if ex.Identity.HumanID != "human-1" || ex.Identity.AgentID != "agent-1" || ex.Identity.SessionID != "session-1" {
		t.Fatalf("identity = %#v, want certificate session identity", ex.Identity)
	}
}

func TestAuthFilterAdapterRejectsMissingOAuthBearer(t *testing.T) {
	t.Parallel()
	s := minimalServer()
	ex := newTestExchange(http.MethodPost, "/mcp", `{}`, map[string]string{
		defaultVerifiedSPIFFEHeader: "spiffe://example.org/ns/team-a/session/session-1",
	})
	ex.R.TLS = verifiedProxyTLS()
	ex.Policy = &policypkg.Document{
		Auth: &policypkg.Auth{
			TrustDomain: "example.org",
			IssuerURL:   "https://issuer.example.com",
			Audience:    "http://proxy.example.com/mcp",
		},
		Sessions: []policypkg.Binding{{Name: "session-1", Namespace: "team-a", HumanID: "human-1"}},
	}

	if got := s.authFilter(ex); got != Reject {
		t.Fatalf("authFilter = %v, want Reject", got)
	}
	if ex.Decision.Reason != "missing_bearer_token" {
		t.Fatalf("reason = %q, want missing_bearer_token", ex.Decision.Reason)
	}
}

func TestAuthFilterAdapterRejectsMismatchedOAuthSubject(t *testing.T) {
	t.Parallel()
	issuer := newTestJWTIssuer(t)
	s := minimalServer()
	s.httpClient = issuer.server.Client()
	ex := newTestExchange(http.MethodPost, "/mcp", `{}`, map[string]string{
		defaultVerifiedSPIFFEHeader: "spiffe://example.org/ns/team-a/session/session-1",
	})
	ex.R.TLS = verifiedProxyTLS()
	ex.Policy = oauthPolicy(issuer.url)
	ex.Policy.Auth.TrustDomain = "example.org"
	ex.Policy.Sessions = []policypkg.Binding{{Name: "session-1", Namespace: "team-a", HumanID: "human-1"}}
	token := issuer.sign(t, jwt.MapClaims{
		"iss": issuer.url, "aud": "http://proxy.example.com/mcp", "sub": "different-human",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	ex.R.Header.Set("Authorization", "Bearer "+token)

	if got := s.authFilter(ex); got != Reject {
		t.Fatalf("authFilter = %v, want Reject", got)
	}
	if ex.Decision.Reason != "certificate_oauth_subject_mismatch" {
		t.Fatalf("reason = %q, want certificate_oauth_subject_mismatch", ex.Decision.Reason)
	}
}

// TestAuthFilterMTLSRejectsForgedHeaderWithoutMTLS is the gateway half of the
// "trusted header" guarantee: a request that carries the verified SPIFFE header
// but did NOT arrive over a verified mTLS hop (e.g. a pod connecting directly to
// the gateway, bypassing the ingress) must be rejected before the header is read.
func TestAuthFilterMTLSRejectsForgedHeaderWithoutMTLS(t *testing.T) {
	t.Parallel()
	s := minimalServer()
	ex := newTestExchange(http.MethodPost, "/mcp", `{}`, map[string]string{
		defaultVerifiedSPIFFEHeader: "spiffe://example.org/ns/team-a/session/session-1",
	})
	// No ex.R.TLS — plaintext / ingress-bypassing connection.
	ex.Policy = &policypkg.Document{
		Auth:     &policypkg.Auth{TrustDomain: "example.org"},
		Sessions: []policypkg.Binding{{Name: "session-1", Namespace: "team-a", HumanID: "human-1"}},
	}

	if got := s.authFilter(ex); got != Reject {
		t.Fatalf("authFilter = %v, want Reject", got)
	}
	if ex.Decision.Reason != "missing_client_certificate" {
		t.Fatalf("reason = %q, want missing_client_certificate", ex.Decision.Reason)
	}
	if ex.Identity != (identityContext{}) {
		t.Fatalf("identity = %#v, want empty (forged header must not be trusted)", ex.Identity)
	}
}

// A trust-domain-only policy enables adapter certificates without enabling
// OAuth. A direct request has no adapter identity and proceeds to policy.
func TestAuthFilterWithoutVerifiedHeaderAllowsDirectWhenOAuthDisabled(t *testing.T) {
	t.Parallel()
	s := minimalServer()
	ex := newTestExchange(http.MethodPost, "/mcp", `{}`, nil)
	ex.R.TLS = verifiedProxyTLS()
	ex.Policy = &policypkg.Document{Auth: &policypkg.Auth{TrustDomain: "example.org"}}

	if got := s.authFilter(ex); got != Continue {
		t.Fatalf("authFilter = %v, want Continue", got)
	}
	if ex.Identity != (identityContext{}) {
		t.Fatalf("identity = %#v, want empty direct identity", ex.Identity)
	}
}

func TestAuthFilterMTLSRejectsWrongTrustDomain(t *testing.T) {
	t.Parallel()
	s := minimalServer()
	ex := newTestExchange(http.MethodPost, "/mcp", `{}`, map[string]string{
		defaultVerifiedSPIFFEHeader: "spiffe://attacker.org/ns/team-a/session/session-1",
	})
	ex.R.TLS = verifiedProxyTLS()
	ex.Policy = &policypkg.Document{
		Auth:     &policypkg.Auth{TrustDomain: "example.org"},
		Sessions: []policypkg.Binding{{Name: "session-1", Namespace: "team-a"}},
	}

	if got := s.authFilter(ex); got != Reject {
		t.Fatalf("authFilter = %v, want Reject", got)
	}
	if ex.Decision.Reason != "invalid_spiffe_identity" {
		t.Fatalf("reason = %q, want invalid_spiffe_identity", ex.Decision.Reason)
	}
}

func TestAuthFilterMTLSTrustedProxyPinning(t *testing.T) {
	t.Parallel()
	const proxyID = "spiffe://example.org/ns/traefik/sa/traefik"
	for _, tc := range []struct {
		name      string
		peerURIs  []string
		wantAllow bool
		reason    string
	}{
		{name: "matching proxy identity", peerURIs: []string{proxyID}, wantAllow: true},
		{name: "wrong proxy identity", peerURIs: []string{"spiffe://example.org/ns/evil/sa/evil"}, reason: "untrusted_proxy"},
		{name: "no proxy identity", peerURIs: nil, reason: "untrusted_proxy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := minimalServer()
			s.trustedProxySPIFFE = proxyID
			ex := newTestExchange(http.MethodPost, "/mcp", `{}`, map[string]string{
				defaultVerifiedSPIFFEHeader: "spiffe://example.org/ns/team-a/session/session-1",
			})
			ex.R.TLS = verifiedProxyTLS(tc.peerURIs...)
			ex.Policy = &policypkg.Document{
				Auth:     &policypkg.Auth{TrustDomain: "example.org"},
				Sessions: []policypkg.Binding{{Name: "session-1", Namespace: "team-a", HumanID: "human-1"}},
			}

			_, reason := s.authenticateAdapterCertificate(ex.R, ex.Policy)
			if tc.wantAllow {
				if reason != "" {
					t.Fatalf("authenticateAdapterCertificate reason = %q, want empty", reason)
				}
				return
			}
			if reason != tc.reason {
				t.Fatalf("reason = %q, want %q", reason, tc.reason)
			}
		})
	}
}

func TestAuthFilterMTLSRejectsRevokedOrExpiredSession(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		binding policypkg.Binding
		reason  string
	}{
		{
			name: "revoked",
			binding: policypkg.Binding{
				Name:      "session-1",
				Namespace: "team-a",
				Revoked:   true,
			},
			reason: "session_revoked",
		},
		{
			name: "expired",
			binding: policypkg.Binding{
				Name:      "session-1",
				Namespace: "team-a",
				ExpiresAt: time.Now().Add(-time.Minute).Format(time.RFC3339),
			},
			reason: "session_expired",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := minimalServer()
			ex := newTestExchange(http.MethodPost, "/mcp", `{}`, map[string]string{
				defaultVerifiedSPIFFEHeader: "spiffe://example.org/ns/team-a/session/session-1",
			})
			ex.R.TLS = verifiedProxyTLS()
			ex.Policy = &policypkg.Document{
				Auth:     &policypkg.Auth{TrustDomain: "example.org"},
				Sessions: []policypkg.Binding{tc.binding},
			}

			if got := s.authFilter(ex); got != Reject {
				t.Fatalf("authFilter = %v, want Reject", got)
			}
			if ex.Decision.Reason != tc.reason {
				t.Fatalf("reason = %q, want %q", ex.Decision.Reason, tc.reason)
			}
		})
	}
}

func TestAuthFilterAlwaysRunsBeforeAuthz(t *testing.T) {
	t.Parallel()
	// Prove ordering by verifying that when authFilter Rejects, authzFilter
	// is never called. Use a base filter after auth in a custom pipeline.
	authzCalled := false
	s := minimalServer()

	issuer := newTestJWTIssuer(t)
	s.snapshotPolicy(policySnapshot{Policy: oauthPolicy(issuer.url)})

	pipeline := []Filter{
		FilterFunc(s.inspectFilter),
		FilterFunc(s.policyFilter),
		FilterFunc(s.authFilter),
		FilterFunc(func(_ *Exchange) Result {
			authzCalled = true
			return Continue
		}),
	}

	ex := newExchange(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/mcp", nil), "test")
	for _, f := range pipeline {
		if f.Handle(ex) != Continue {
			break
		}
	}

	if authzCalled {
		t.Fatal("authz stage was called despite authFilter rejecting — ordering violated")
	}
}

// ---- stage 4: authzFilter ---------------------------------------------------

func TestAuthzFilterContinuesForNonToolCall(t *testing.T) {
	t.Parallel()
	s := minimalServer()
	ex := newTestExchange(http.MethodPost, "/mcp", `{"method":"tools/list"}`, map[string]string{"Content-Type": "application/json"})
	s.inspectFilter(ex)
	ex.Policy = testPolicy()
	ex.Identity = identityContext{HumanID: "human-1", AgentID: "client-1", TeamID: "team-acme"}

	if got := s.authzFilter(ex); got != Continue {
		t.Fatalf("authzFilter tools/list = %v, want Continue", got)
	}
	if !ex.Decision.Allowed {
		t.Fatalf("Decision.Allowed = false for non-tool-call, want true")
	}
}

func TestNewExchangeDefaultsToDeny(t *testing.T) {
	t.Parallel()
	// The default decision must be deny so any request that reaches upstream or
	// audit without an explicit decision (a filter-ordering bug or future
	// unhandled stage) fails closed rather than being proxied as allowed.
	ex := newExchange(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/mcp", nil), "test")
	if ex.Decision.Allowed {
		t.Fatal("newExchange default Decision.Allowed = true, want false (fail-closed)")
	}
	if ex.Decision.Status != http.StatusForbidden {
		t.Fatalf("newExchange default status = %d, want 403", ex.Decision.Status)
	}
}

func TestAuthzFilterSetsExplicitAllowForNonToolCall(t *testing.T) {
	t.Parallel()
	// The non-tool passthrough must set an explicit allow, not rely on the
	// (now deny) default — otherwise the request would be denied.
	s := minimalServer()
	ex := newTestExchange(http.MethodPost, "/mcp", `{"method":"tools/list"}`, map[string]string{"Content-Type": "application/json"})
	s.inspectFilter(ex)
	ex.Policy = testPolicy()

	if got := s.authzFilter(ex); got != Continue {
		t.Fatalf("authzFilter tools/list = %v, want Continue", got)
	}
	if !ex.Decision.Allowed {
		t.Fatal("non-tool-call Decision.Allowed = false, want explicit allow")
	}
}

func TestAuthzFilterRejectsDeniedToolCall(t *testing.T) {
	t.Parallel()
	s := minimalServer()
	body := `{"method":"tools/call","params":{"name":"echo"}}`
	ex := newTestExchange(http.MethodPost, "/mcp", body, map[string]string{"Content-Type": "application/json"})
	s.inspectFilter(ex)
	// Use a session-optional allow-list policy so an unrecognised identity
	// reaches grant evaluation and gets no_matching_grant → 403.
	ex.Policy = &policypkg.Document{
		Auth: &policypkg.Auth{},
		Policy: &policypkg.Config{
			Mode:            "allow-list",
			DefaultDecision: "deny",
			PolicyVersion:   "test",
		},
		Tools: []policypkg.Tool{
			{Name: "echo", RequiredTrust: "low", SideEffect: "read"},
		},
		Grants: []policypkg.Grant{
			{Name: "g1", HumanID: "human-1", AgentID: "agent-1",
				MaxTrust: "high", AllowedSideEffects: []string{"read"},
				ToolRules: []policypkg.ToolAccess{{Name: "echo", Decision: "allow"}}},
		},
	}
	// No matching grant → default deny.
	ex.Identity = identityContext{HumanID: "unknown", AgentID: "unknown"}

	if got := s.authzFilter(ex); got != Reject {
		t.Fatalf("authzFilter denied tool call = %v, want Reject", got)
	}
	if ex.Decision.Allowed {
		t.Fatal("Decision.Allowed = true after authz rejection, want false")
	}
	recorder := ex.W.ResponseWriter.(*httptest.ResponseRecorder)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
}

func TestAuthzFilterAuthorizationInputsUnchangedAfterDecision(t *testing.T) {
	t.Parallel()
	// Prove that upstreamFilter (stage 5) receives the same Policy and Identity
	// pointers that authzFilter read, confirming no mutation happened between
	// stages 4 and 5.
	s := minimalServer()

	upstreamCalled := false
	var policyAtUpstream *policypkg.Document
	var identityAtUpstream identityContext

	target, _ := url.Parse("http://127.0.0.1:1") // unreachable; we intercept before
	s.proxy = newUpstreamReverseProxy(target)

	pipeline := []Filter{
		FilterFunc(s.authzFilter),
		FilterFunc(func(ex *Exchange) Result {
			upstreamCalled = true
			policyAtUpstream = ex.Policy
			identityAtUpstream = ex.Identity
			return Respond
		}),
	}

	body := `{"method":"tools/list"}`
	ex := newTestExchange(http.MethodPost, "/mcp", body, map[string]string{"Content-Type": "application/json"})
	s.inspectFilter(ex) // sets ToolCall=false, so authz skips evaluation
	pol := testPolicy()
	ex.Policy = pol
	ident := identityContext{HumanID: "human-1", AgentID: "client-1", TeamID: "team-acme"}
	ex.Identity = ident

	for _, f := range pipeline {
		if f.Handle(ex) != Continue {
			break
		}
	}

	if !upstreamCalled {
		t.Fatal("upstream stage was not reached")
	}
	if policyAtUpstream != pol {
		t.Fatal("Policy was replaced between authz and upstream stages")
	}
	if identityAtUpstream != ident {
		t.Fatal("Identity was mutated between authz and upstream stages")
	}
}

func TestAuthzFilterPolicyUnavailableDeniesWith503(t *testing.T) {
	t.Parallel()
	s := minimalServer()
	body := `{"method":"tools/call","params":{"name":"echo"}}`
	ex := newTestExchange(http.MethodPost, "/mcp", body, map[string]string{"Content-Type": "application/json"})
	s.inspectFilter(ex)
	ex.Policy = &policypkg.Document{Policy: &policypkg.Config{}}
	ex.PolicyErr = errPolicyUnavailable

	if got := s.authzFilter(ex); got != Reject {
		t.Fatalf("authzFilter policy_unavailable = %v, want Reject", got)
	}
	recorder := ex.W.ResponseWriter.(*httptest.ResponseRecorder)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
	var payload map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &payload)
	if payload["error"] != "policy_unavailable" {
		t.Fatalf("error = %q, want policy_unavailable", payload["error"])
	}
}

// ---- stage 5: upstreamFilter ------------------------------------------------

func TestUpstreamFilterAlwaysReturnsRespond(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)
	target, _ := url.Parse(upstream.URL)

	s := minimalServer()
	s.proxy = newUpstreamReverseProxy(target)
	ex := newTestExchange(http.MethodGet, "/mcp", "", nil)
	ex.Policy = testPolicy()

	if got := s.upstreamFilter(ex); got != Respond {
		t.Fatalf("upstreamFilter = %v, want Respond", got)
	}
}

// The verified SPIFFE header is an ingress-to-gateway assertion; forwarding it
// would let an MCP server behind a plain Ingress trust a client-forged value.
func TestUpstreamFilterStripsVerifiedIdentityHeader(t *testing.T) {
	t.Parallel()
	var seen atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Store(r.Header.Get(defaultVerifiedSPIFFEHeader))
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)
	target, _ := url.Parse(upstream.URL)

	s := minimalServer()
	s.proxy = newUpstreamReverseProxy(target)
	ex := newTestExchange(http.MethodGet, "/mcp", "", map[string]string{
		defaultVerifiedSPIFFEHeader: "spiffe://example.org/ns/team-a/session/forged",
	})
	ex.Policy = testPolicy()

	if got := s.upstreamFilter(ex); got != Respond {
		t.Fatalf("upstreamFilter = %v, want Respond", got)
	}
	if got, _ := seen.Load().(string); got != "" {
		t.Fatalf("upstream saw %s = %q, want it stripped", defaultVerifiedSPIFFEHeader, got)
	}
}

func TestUpstreamFilterPreservesOAuthBearerForMCPServer(t *testing.T) {
	t.Parallel()
	var seen atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Store(r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)
	target, _ := url.Parse(upstream.URL)

	s := minimalServer()
	s.proxy = newUpstreamReverseProxy(target)
	ex := newTestExchange(http.MethodGet, "/mcp", "", map[string]string{
		"Authorization": "Bearer validated-token",
	})
	ex.Policy = oauthPolicy("https://issuer.example.com")

	if got := s.upstreamFilter(ex); got != Respond {
		t.Fatalf("upstreamFilter = %v, want Respond", got)
	}
	if got, _ := seen.Load().(string); got != "Bearer validated-token" {
		t.Fatalf("upstream Authorization = %q, want validated bearer", got)
	}
}

func TestUpstreamFilterStripsUnvalidatedBearerWhenOAuthDisabled(t *testing.T) {
	t.Parallel()
	var seen atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Store(r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)
	target, _ := url.Parse(upstream.URL)

	s := minimalServer()
	s.proxy = newUpstreamReverseProxy(target)
	ex := newTestExchange(http.MethodGet, "/mcp", "", map[string]string{
		"Authorization": "Bearer unvalidated-token",
	})
	ex.Policy = testPolicy()

	if got := s.upstreamFilter(ex); got != Respond {
		t.Fatalf("upstreamFilter = %v, want Respond", got)
	}
	if got, _ := seen.Load().(string); got != "" {
		t.Fatalf("upstream Authorization = %q, want stripped on non-OAuth server", got)
	}
}

// ---- errPolicyUnavailable base ------------------------------------------

func TestErrPolicyUnavailableIsBaseError(t *testing.T) {
	// Verify that errPolicyUnavailable is a distinct error value that can be
	// identified by callers checking the policy unavailable condition.
	if errPolicyUnavailable == nil {
		t.Fatal("errPolicyUnavailable is nil")
	}
	if errPolicyUnavailable.Error() == "" {
		t.Fatal("errPolicyUnavailable.Error() is empty")
	}
}
