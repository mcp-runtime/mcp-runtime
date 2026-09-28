package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"mcp-runtime/pkg/events"
	policypkg "mcp-runtime/pkg/policy"
)

func TestHandleProxyOAuthProtectedResourceMetadata(t *testing.T) {
	issuer := newTestJWTIssuer(t)
	upstreamCalled := false
	proxy := newTestGatewayServer(t, oauthPolicy(issuer.url), func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalled = true
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "http://proxy.example.com/.well-known/oauth-protected-resource/mcp", nil)
	recorder := httptest.NewRecorder()

	proxy.handleGateway(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if upstreamCalled {
		t.Fatal("metadata request should not reach upstream")
	}

	var payload struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	// The advertised resource is auth.audience verbatim, not a value derived
	// from the request, so a client that echoes it back as the RFC 8707
	// resource parameter gets a token this gateway will accept.
	if payload.Resource != "http://proxy.example.com/mcp" {
		t.Fatalf("resource = %q, want %q", payload.Resource, "http://proxy.example.com/mcp")
	}
	if len(payload.AuthorizationServers) != 1 || payload.AuthorizationServers[0] != issuer.url {
		t.Fatalf("authorization_servers = %#v, want [%q]", payload.AuthorizationServers, issuer.url)
	}
}

func TestHandleProxyNonOAuthMetadataExplainsAdapter(t *testing.T) {
	upstreamCalled := false
	proxy := newTestGatewayServer(t, headerPolicy(), func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalled = true
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "http://proxy.example.com/.well-known/oauth-protected-resource/mcp", nil)
	recorder := httptest.NewRecorder()

	proxy.handleGateway(recorder, req)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
	if upstreamCalled {
		t.Fatal("metadata request should not reach upstream")
	}
	if got := recorder.Header().Get("Www-Authenticate"); got != "" {
		t.Fatalf("WWW-Authenticate = %q, want empty for header-mode policy", got)
	}

	var payload struct {
		Error           string `json:"error"`
		Message         string `json:"message"`
		AdapterRequired bool   `json:"adapter_required"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if payload.Error != "oauth_not_enabled" {
		t.Fatalf("error = %q, want oauth_not_enabled", payload.Error)
	}
	if !payload.AdapterRequired || !strings.Contains(payload.Message, "mcp-runtime adapter") {
		t.Fatalf("payload = %#v, want adapter guidance", payload)
	}
}

func TestHandleProxyHeaderModeMissingIdentityExplainsAdapter(t *testing.T) {
	upstreamCalled := false
	proxy := newTestGatewayServer(t, headerPolicy(), func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalled = true
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "http://proxy.example.com/mcp", strings.NewReader(`{"method":"tools/call","params":{"name":"echo"}}`))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	proxy.handleGateway(recorder, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
	if upstreamCalled {
		t.Fatal("missing identity request should not reach upstream")
	}
	if got := recorder.Header().Get("Www-Authenticate"); got != "" {
		t.Fatalf("WWW-Authenticate = %q, want empty for header-mode policy", got)
	}

	var payload struct {
		Error           string   `json:"error"`
		Message         string   `json:"message"`
		AdapterRequired bool     `json:"adapter_required"`
		RequiredHeaders []string `json:"required_headers"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if payload.Error != "missing_identity" {
		t.Fatalf("error = %q, want missing_identity", payload.Error)
	}
	if !payload.AdapterRequired || !strings.Contains(payload.Message, "mcp-runtime adapter") {
		t.Fatalf("payload = %#v, want adapter guidance", payload)
	}
	if len(payload.RequiredHeaders) != 4 {
		t.Fatalf("required_headers = %#v, want four governance headers", payload.RequiredHeaders)
	}
}

func TestHandleProxyOAuthChallengesWithoutBearer(t *testing.T) {
	issuer := newTestJWTIssuer(t)
	upstreamCalled := false
	proxy := newTestGatewayServer(t, oauthPolicy(issuer.url), func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalled = true
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "http://proxy.example.com/mcp", strings.NewReader(`{"method":"tools/call","params":{"name":"echo"}}`))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	proxy.handleGateway(recorder, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
	if upstreamCalled {
		t.Fatal("unauthenticated request should not reach upstream")
	}
	if got := recorder.Header().Get("Www-Authenticate"); !strings.Contains(got, `resource_metadata="http://proxy.example.com/.well-known/oauth-protected-resource/mcp"`) {
		t.Fatalf("WWW-Authenticate = %q, missing resource metadata URL", got)
	}

	var payload map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if payload["error"] != "missing_bearer_token" {
		t.Fatalf("error = %q, want %q", payload["error"], "missing_bearer_token")
	}
}

func TestHandleProxyOAuthChallengeUsesAudienceMetadataURL(t *testing.T) {
	issuer := newTestJWTIssuer(t)
	policy := oauthPolicy(issuer.url)
	policy.Auth.Audience = "https://public.example.com/proxy/mcp"
	proxy := newTestGatewayServer(t, policy, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	externalBaseURL, err := parseExternalBaseURL("https://public.example.com/proxy")
	if err != nil {
		t.Fatalf("parseExternalBaseURL() error = %v", err)
	}
	proxy.externalBaseURL = externalBaseURL

	req := httptest.NewRequest(http.MethodPost, "http://proxy.example.com/mcp", strings.NewReader(`{"method":"tools/call","params":{"name":"echo"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Host", "evil.example.com")
	req.Header.Set("X-Forwarded-Proto", "https")
	recorder := httptest.NewRecorder()

	proxy.handleGateway(recorder, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
	if got := recorder.Header().Get("Www-Authenticate"); !strings.Contains(got, `resource_metadata="https://public.example.com/.well-known/oauth-protected-resource/proxy/mcp"`) {
		t.Fatalf("WWW-Authenticate = %q, missing external resource metadata URL", got)
	}
}

func TestOAuthTeamIDSelectsPolicyTeamFromTeamIDsClaim(t *testing.T) {
	claims := jwt.MapClaims{"team_ids": []any{"team-other", "team-acme"}}
	policy := oauthPolicy("https://issuer.example.com")
	policy.Server.TeamID = "team-acme"
	if got := oauthTeamID(claims, policy); got != "team-acme" {
		t.Fatalf("oauthTeamID() = %q, want team-acme", got)
	}
}

func TestRewriteOAuthEndpointUsesInternalIssuer(t *testing.T) {
	got, err := rewriteOAuthEndpoint(
		"https://public.example.com/oauth/jwks.json",
		"https://public.example.com/oauth",
		"http://mcp-auth-server.mcp-sentinel.svc.cluster.local:8080/mcp-auth",
	)
	if err != nil {
		t.Fatalf("rewriteOAuthEndpoint() error = %v", err)
	}
	want := "http://mcp-auth-server.mcp-sentinel.svc.cluster.local:8080/mcp-auth/jwks.json"
	if got != want {
		t.Fatalf("rewriteOAuthEndpoint() = %q, want %q", got, want)
	}
}

func TestFetchAuthServerMetadataFallsBackToConfiguredIssuer(t *testing.T) {
	var publicIssuerURL string
	publicIssuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(authServerMetadata{Issuer: publicIssuerURL, JWKSURI: publicIssuerURL + "/keys"})
	}))
	t.Cleanup(publicIssuer.Close)
	publicIssuerURL = publicIssuer.URL

	internalIssuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"issuer":"http://localhost:18080/oauth","jwks_uri":"http://localhost:18080/oauth/keys"}`)
	}))
	t.Cleanup(internalIssuer.Close)

	server := &gatewayServer{
		httpClient:             publicIssuer.Client(),
		oauthInternalIssuerURL: internalIssuer.URL,
	}
	metadata, usingInternal, err := server.fetchAuthServerMetadataWithFallback(context.Background(), publicIssuer.URL)
	if err != nil {
		t.Fatalf("fetchAuthServerMetadataWithFallback() error = %v", err)
	}
	if usingInternal {
		t.Fatal("fetchAuthServerMetadataWithFallback() used mismatched internal metadata")
	}
	if metadata.Issuer != publicIssuer.URL {
		t.Fatalf("metadata issuer = %q, want %s", metadata.Issuer, publicIssuer.URL)
	}
}

func TestHandleProxyOAuthValidatesJWTAndAppliesIdentityHeaders(t *testing.T) {
	issuer := newTestJWTIssuer(t)

	var upstreamHeaders http.Header
	proxy := newTestGatewayServer(t, oauthPolicy(issuer.url), func(w http.ResponseWriter, r *http.Request) {
		upstreamHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	})

	token := issuer.sign(t, jwt.MapClaims{
		"iss":     issuer.url,
		"aud":     "http://proxy.example.com/mcp",
		"sub":     "human-1",
		"azp":     "client-1",
		"team_id": "team-acme",
		"sid":     "session-1",
		"exp":     time.Now().Add(time.Hour).Unix(),
		"nbf":     time.Now().Add(-time.Minute).Unix(),
	})

	req := httptest.NewRequest(http.MethodPost, "http://proxy.example.com/mcp", strings.NewReader(`{"method":"tools/call","params":{"name":"echo"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()

	proxy.handleGateway(recorder, req)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
	if upstreamHeaders == nil {
		t.Fatal("expected upstream request")
	}
	if got := upstreamHeaders.Get(defaultHumanHeader); got != "human-1" {
		t.Fatalf("%s = %q, want %q", defaultHumanHeader, got, "human-1")
	}
	if got := upstreamHeaders.Get(defaultAgentHeader); got != "client-1" {
		t.Fatalf("%s = %q, want %q", defaultAgentHeader, got, "client-1")
	}
	if got := upstreamHeaders.Get(defaultTeamHeader); got != "team-acme" {
		t.Fatalf("%s = %q, want %q", defaultTeamHeader, got, "team-acme")
	}
	if got := upstreamHeaders.Get(defaultSessionHeader); got != "session-1" {
		t.Fatalf("%s = %q, want %q", defaultSessionHeader, got, "session-1")
	}
	if got := upstreamHeaders.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q, want inbound bearer token stripped", got)
	}
}

func TestHandleProxyOAuthRejectsWrongAudience(t *testing.T) {
	issuer := newTestJWTIssuer(t)
	proxy := newTestGatewayServer(t, oauthPolicy(issuer.url), func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("wrong-audience request reached upstream")
	})

	token := issuer.sign(t, jwt.MapClaims{
		"iss": issuer.url,
		"aud": "https://other.example.com/mcp",
		"sub": "human-1",
		"azp": "client-1",
		"sid": "session-1",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	req := httptest.NewRequest(http.MethodPost, "http://proxy.example.com/mcp", strings.NewReader(`{"method":"tools/call","params":{"name":"echo"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()

	proxy.handleGateway(recorder, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
	if got := recorder.Header().Get("Www-Authenticate"); !strings.Contains(got, `error="invalid_token"`) {
		t.Fatalf("WWW-Authenticate = %q, missing invalid_token", got)
	}
}

// A policy with auth.mode oauth but no audience is a misconfiguration the
// gateway must not paper over by deriving the audience from the request: a
// derived audience is only as trustworthy as the forwarded host headers behind
// it, and it would not match the resource advertised in the metadata document.
func TestHandleProxyOAuthMissingAudienceFailsClosed(t *testing.T) {
	issuer := newTestJWTIssuer(t)
	policy := oauthPolicy(issuer.url)
	policy.Auth.Audience = ""
	proxy := newTestGatewayServer(t, policy, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("request reached upstream with no configured audience")
	})

	token := issuer.sign(t, jwt.MapClaims{
		"iss": issuer.url,
		"aud": "http://proxy.example.com/mcp",
		"sub": "human-1",
		"azp": "client-1",
		"sid": "session-1",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	req := httptest.NewRequest(http.MethodPost, "http://proxy.example.com/mcp", strings.NewReader(`{"method":"tools/call","params":{"name":"echo"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()

	proxy.handleGateway(recorder, req)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
}

// The resource in protected resource metadata and the audience the gateway
// validates must be the same string, or a client that follows the metadata is
// rejected with an opaque invalid_token it cannot act on.
func TestOAuthMetadataResourceMatchesValidatedAudience(t *testing.T) {
	issuer := newTestJWTIssuer(t)
	policy := oauthPolicy(issuer.url)
	policy.Auth.Audience = "https://public.example.com/team/mcp"
	proxy := newTestGatewayServer(t, policy, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "http://proxy.example.com/.well-known/oauth-protected-resource/mcp", nil)
	recorder := httptest.NewRecorder()
	proxy.handleGateway(recorder, req)

	var payload struct {
		Resource string `json:"resource"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if payload.Resource != policy.Auth.Audience {
		t.Fatalf("resource = %q, want %q", payload.Resource, policy.Auth.Audience)
	}

	// A token minted for exactly that advertised resource is accepted.
	token := issuer.sign(t, jwt.MapClaims{
		"iss": issuer.url,
		"aud": payload.Resource,
		"sub": "human-1",
		"azp": "client-1",
		"sid": "session-1",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	call := httptest.NewRequest(http.MethodPost, "http://proxy.example.com/mcp", strings.NewReader(`{"method":"tools/call","params":{"name":"echo"}}`))
	call.Header.Set("Content-Type", "application/json")
	call.Header.Set("Authorization", "Bearer "+token)
	callRecorder := httptest.NewRecorder()
	proxy.handleGateway(callRecorder, call)

	if callRecorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", callRecorder.Code, http.StatusNoContent)
	}
}

// Metadata for an oauth policy with no audience must not advertise a resource
// identifier that token validation would then reject.
func TestOAuthMetadataMissingAudienceFailsClosed(t *testing.T) {
	issuer := newTestJWTIssuer(t)
	policy := oauthPolicy(issuer.url)
	policy.Auth.Audience = ""
	proxy := newTestGatewayServer(t, policy, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("metadata request reached upstream")
	})

	req := httptest.NewRequest(http.MethodGet, "http://proxy.example.com/.well-known/oauth-protected-resource/mcp", nil)
	recorder := httptest.NewRecorder()
	proxy.handleGateway(recorder, req)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
}

func TestHandleProxyOAuthPolicyDenialOmitsInsufficientScopeChallenge(t *testing.T) {
	issuer := newTestJWTIssuer(t)
	policy := oauthPolicy(issuer.url)
	policy.Grants[0].ToolRules[0].Decision = "deny"
	proxy := newTestGatewayServer(t, policy, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("denied request reached upstream")
	})

	token := issuer.sign(t, jwt.MapClaims{
		"iss": issuer.url,
		"aud": "http://proxy.example.com/mcp",
		"sub": "human-1",
		"azp": "client-1",
		"sid": "session-1",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	req := httptest.NewRequest(http.MethodPost, "http://proxy.example.com/mcp", strings.NewReader(`{"method":"tools/call","params":{"name":"echo"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()

	proxy.handleGateway(recorder, req)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	if challenge := recorder.Header().Get("Www-Authenticate"); challenge != "" {
		t.Fatalf("WWW-Authenticate = %q, want empty for policy denial", challenge)
	}
}

func TestApplyIdentityHeadersClearsSpoofedValues(t *testing.T) {
	t.Parallel()

	proxy := &gatewayServer{
		defaultHumanHeader:   defaultHumanHeader,
		defaultAgentHeader:   defaultAgentHeader,
		defaultTeamHeader:    defaultTeamHeader,
		defaultSessionHeader: defaultSessionHeader,
	}
	req := httptest.NewRequest(http.MethodGet, "http://proxy.example.com/mcp", nil)
	req.Header.Set(defaultHumanHeader, "spoofed-human")
	req.Header.Set(defaultAgentHeader, "spoofed-agent")
	req.Header.Set(defaultTeamHeader, "spoofed-team")
	req.Header.Set(defaultSessionHeader, "spoofed-session")

	proxy.applyIdentityHeaders(req, oauthPolicy("https://issuer.example.com"), identityContext{
		HumanID: "human-1",
		TeamID:  "team-acme",
	})

	if got := req.Header.Get(defaultHumanHeader); got != "human-1" {
		t.Fatalf("%s = %q, want %q", defaultHumanHeader, got, "human-1")
	}
	if got := req.Header.Get(defaultAgentHeader); got != "" {
		t.Fatalf("%s = %q, want empty", defaultAgentHeader, got)
	}
	if got := req.Header.Get(defaultTeamHeader); got != "team-acme" {
		t.Fatalf("%s = %q, want %q", defaultTeamHeader, got, "team-acme")
	}
	if got := req.Header.Get(defaultSessionHeader); got != "" {
		t.Fatalf("%s = %q, want empty", defaultSessionHeader, got)
	}
}

func TestAuthenticateOAuthIgnoresClientGovernanceHeaders(t *testing.T) {
	issuer := newTestJWTIssuer(t)
	proxy := newTestGatewayServer(t, oauthPolicy(issuer.url), func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(defaultSessionHeader); got != "session-1" {
			t.Fatalf("upstream %s = %q, want session-1 from JWT sid (not client spoof)", defaultSessionHeader, got)
		}
		if got := r.Header.Get(defaultHumanHeader); got != "human-1" {
			t.Fatalf("upstream %s = %q, want human-1", defaultHumanHeader, got)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	token := issuer.sign(t, jwt.MapClaims{
		"iss":     issuer.url,
		"aud":     "http://proxy.example.com/mcp",
		"sub":     "human-1",
		"azp":     "client-1",
		"team_id": "team-acme",
		"sid":     "session-1",
		"exp":     time.Now().Add(time.Hour).Unix(),
		"nbf":     time.Now().Add(-time.Minute).Unix(),
	})

	req := httptest.NewRequest(http.MethodPost, "http://proxy.example.com/mcp", strings.NewReader(`{"method":"tools/call","params":{"name":"echo"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(defaultHumanHeader, "spoofed-human")
	req.Header.Set(defaultAgentHeader, "spoofed-agent")
	req.Header.Set(defaultTeamHeader, "spoofed-team")
	req.Header.Set(defaultSessionHeader, "spoofed-session")

	recorder := httptest.NewRecorder()
	proxy.handleGateway(recorder, req)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s, want %d", recorder.Code, recorder.Body.String(), http.StatusNoContent)
	}
}

func TestAuthenticateOAuthDoesNotTakeSessionFromHeadersWithoutSID(t *testing.T) {
	issuer := newTestJWTIssuer(t)
	policy := oauthPolicy(issuer.url)
	policy.Session.Required = false
	var gotSession string
	proxy := newTestGatewayServer(t, policy, func(w http.ResponseWriter, r *http.Request) {
		gotSession = r.Header.Get(defaultSessionHeader)
		w.WriteHeader(http.StatusNoContent)
	})

	token := issuer.sign(t, jwt.MapClaims{
		"iss": issuer.url,
		"aud": "http://proxy.example.com/mcp",
		"sub": "human-1",
		"azp": "client-1",
		"exp": time.Now().Add(time.Hour).Unix(),
		"nbf": time.Now().Add(-time.Minute).Unix(),
	})

	req := httptest.NewRequest(http.MethodPost, "http://proxy.example.com/mcp", strings.NewReader(`{"method":"tools/call","params":{"name":"echo"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(defaultSessionHeader, "spoofed-session-only")

	recorder := httptest.NewRecorder()
	proxy.handleGateway(recorder, req)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s, want %d", recorder.Code, recorder.Body.String(), http.StatusNoContent)
	}
	if gotSession != "" {
		t.Fatalf("session reached upstream as %q; OAuth must not use client governance headers", gotSession)
	}
}

func TestApplyUpstreamTokenClearsHeaderWhenTokenMissing(t *testing.T) {
	t.Parallel()

	proxy := &gatewayServer{}
	req := httptest.NewRequest(http.MethodGet, "http://proxy.example.com/mcp", nil)
	req.Header.Set("Authorization", "Bearer spoofed-token")

	proxy.applyUpstreamToken(req, oauthPolicy("https://issuer.example.com"), "")

	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q, want empty", got)
	}
}

func TestApplyUpstreamTokenNeverForwardsOAuthBearer(t *testing.T) {
	t.Parallel()

	proxy := &gatewayServer{}
	policy := oauthPolicy("https://issuer.example.com")
	policy.Session.UpstreamTokenHeader = "X-Upstream-Authorization"
	req := httptest.NewRequest(http.MethodGet, "http://proxy.example.com/mcp", nil)
	req.Header.Set("Authorization", "Bearer inbound-token")
	req.Header.Set("X-Upstream-Authorization", "Bearer spoofed-upstream-token")

	proxy.applyUpstreamToken(req, policy, "inbound-token")

	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q, want empty", got)
	}
	if got := req.Header.Get("X-Upstream-Authorization"); got != "" {
		t.Fatalf("X-Upstream-Authorization = %q, want empty", got)
	}
}

func TestAuthServerMetadataCandidatesUseMCPPathOrder(t *testing.T) {
	want := []string{
		"https://auth.example.com/.well-known/oauth-authorization-server/tenant1",
		"https://auth.example.com/.well-known/openid-configuration/tenant1",
		"https://auth.example.com/tenant1/.well-known/openid-configuration",
	}
	got := authServerMetadataCandidates("https://auth.example.com/tenant1")
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("candidates = %#v, want %#v", got, want)
	}
}

func TestAuthServerMetadataCandidatesUseRootOrder(t *testing.T) {
	want := []string{
		"https://auth.example.com/.well-known/oauth-authorization-server",
		"https://auth.example.com/.well-known/openid-configuration",
	}
	got := authServerMetadataCandidates("https://auth.example.com")
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("candidates = %#v, want %#v", got, want)
	}
}

func TestHandleProxyRewritesUpstreamHostAndForwardedHeaders(t *testing.T) {
	t.Parallel()

	var upstreamHost string
	var upstreamHeaders http.Header
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHost = r.Host
		upstreamHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstreamServer.Close)

	target, err := url.Parse(upstreamServer.URL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}

	proxy := &gatewayServer{
		proxy:                 newUpstreamReverseProxy(target),
		httpClient:            &http.Client{Timeout: 2 * time.Second},
		defaultHumanHeader:    defaultHumanHeader,
		defaultAgentHeader:    defaultAgentHeader,
		defaultTeamHeader:     defaultTeamHeader,
		defaultSessionHeader:  defaultSessionHeader,
		defaultPolicyMode:     defaultPolicyMode,
		defaultPolicyDecision: defaultPolicyDecision,
		defaultPolicyVersion:  "test-policy",
		oauthProviders:        map[string]*oauthProvider{},
	}

	req := httptest.NewRequest(http.MethodGet, "http://policy.example.local/mcp", nil)
	req.Host = "policy.example.local"
	recorder := httptest.NewRecorder()

	proxy.handleGateway(recorder, req)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
	if upstreamHost != target.Host {
		t.Fatalf("upstream host = %q, want %q", upstreamHost, target.Host)
	}
	if got := upstreamHeaders.Get("X-Forwarded-Host"); got != "policy.example.local" {
		t.Fatalf("X-Forwarded-Host = %q, want %q", got, "policy.example.local")
	}
	if got := upstreamHeaders.Get("X-Forwarded-Proto"); got != "http" {
		t.Fatalf("X-Forwarded-Proto = %q, want %q", got, "http")
	}
	if got := upstreamHeaders.Get("X-Forwarded-For"); got != "192.0.2.1" {
		t.Fatalf("X-Forwarded-For = %q, want %q", got, "192.0.2.1")
	}
}

func TestUpstreamResponseHeaderTimeoutReturnsStructured504(t *testing.T) {
	t.Parallel()

	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(200 * time.Millisecond):
		}
	}))
	t.Cleanup(upstreamServer.Close)

	target, err := url.Parse(upstreamServer.URL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}

	proxy := newUpstreamReverseProxyWithTimeout(target, 20*time.Millisecond)
	proxy.ErrorHandler = handleUpstreamError
	recorder := httptest.NewRecorder()
	proxy.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://proxy.example.com/mcp", nil))

	if recorder.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusGatewayTimeout, recorder.Body.String())
	}
	var payload map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error response: %v; body=%s", err, recorder.Body.String())
	}
	if payload["error"] != "upstream_timeout" {
		t.Fatalf("error code = %q, want upstream_timeout", payload["error"])
	}
}

func TestInspectRPCRequestAcceptsChunkedBody(t *testing.T) {
	t.Parallel()

	payload := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo"}}`
	req := httptest.NewRequest(http.MethodPost, "http://proxy.example.com/mcp", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = -1

	inspection := inspectRPCRequest(req)
	if inspection.Indeterminate {
		t.Fatalf("inspection = %#v, want determinate request", inspection)
	}
	if !inspection.ToolCall {
		t.Fatalf("inspection.ToolCall = %v, want true", inspection.ToolCall)
	}
	if inspection.Method != "tools/call" {
		t.Fatalf("inspection.Method = %q, want %q", inspection.Method, "tools/call")
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(body) != payload {
		t.Fatalf("request body = %q, want %q", string(body), payload)
	}
}

func TestAbsoluteRequestURLUsesRequestHost(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req.Host = "proxy.example.com"

	if got := absoluteRequestURL(req, "/mcp"); got != "http://proxy.example.com/mcp" {
		t.Fatalf("absoluteRequestURL() = %q, want %q", got, "http://proxy.example.com/mcp")
	}
}

func TestTrimRequestPathPrefixMatchesOnlyPathBoundaries(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		value  string
		prefix string
		want   string
		ok     bool
	}{
		{name: "exact match", value: "/mcp", prefix: "/mcp", want: "", ok: true},
		{name: "child path", value: "/mcp/tools", prefix: "/mcp", want: "/tools", ok: true},
		{name: "segment prefix only", value: "/mcp-tools", prefix: "/mcp", want: "/mcp-tools", ok: false},
		{name: "unrelated path", value: "/health", prefix: "/mcp", want: "/health", ok: false},
		{name: "trailing slash in config", value: "/mcp/tools", prefix: "/mcp/", want: "/tools", ok: true},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, ok := trimRequestPathPrefix(tc.value, tc.prefix)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("trimRequestPathPrefix(%q, %q) = (%q, %v), want (%q, %v)", tc.value, tc.prefix, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestResolveBaseURLPathPreservesBaseSubpath(t *testing.T) {
	t.Parallel()

	base, err := parseExternalBaseURL("https://public.example.com/proxy")
	if err != nil {
		t.Fatalf("parseExternalBaseURL() error = %v", err)
	}

	if got := resolveBaseURLPath(base, "/.well-known/oauth-protected-resource/mcp"); got != "https://public.example.com/proxy/.well-known/oauth-protected-resource/mcp" {
		t.Fatalf("resolveBaseURLPath() = %q, want %q", got, "https://public.example.com/proxy/.well-known/oauth-protected-resource/mcp")
	}
}

func TestAuditPayloadDoesNotPersistRawQueryString(t *testing.T) {
	t.Parallel()

	proxy := &gatewayServer{
		serverName:           "example-server",
		serverNamespace:      "mcp-servers",
		clusterName:          "kind",
		defaultPolicyVersion: "test-policy",
	}
	req := httptest.NewRequest(http.MethodGet, "http://proxy.example.com/mcp?code=secret&state=opaque", nil)

	payload := proxy.auditPayload(
		req,
		"/mcp",
		"",
		"",
		identityContext{HumanID: "human-1"},
		nil,
		policypkg.Decision{Allowed: true, Reason: "allowed", PolicyVersion: "test-policy"},
		http.StatusOK,
		12,
		34,
	)

	if _, exists := payload["query"]; exists {
		t.Fatalf("audit payload unexpectedly retained query string: %#v", payload)
	}
}

func TestAuditPayloadIncludesLatencyMetadata(t *testing.T) {
	t.Parallel()

	proxy := &gatewayServer{
		serverName:           "example-server",
		serverNamespace:      "mcp-servers",
		clusterName:          "kind",
		defaultPolicyVersion: "test-policy",
	}
	req := httptest.NewRequest(http.MethodPost, "http://proxy.example.com/mcp", strings.NewReader(`{"jsonrpc":"2.0"}`))
	req.ContentLength = int64(len(`{"jsonrpc":"2.0"}`))

	payload := proxy.auditPayload(
		req,
		"/mcp",
		"tools/call",
		"echo",
		identityContext{
			HumanID:   "human-1",
			AgentID:   "agent-1",
			TeamID:    "team-acme",
			SessionID: "session-1",
		},
		&policypkg.Document{Server: policypkg.Server{TeamID: "team-owner"}},
		policypkg.Decision{Allowed: true, Reason: "allowed", PolicyVersion: "test-policy"},
		http.StatusAccepted,
		27,
		91,
	)

	latencyMs, ok := payload["latency_ms"].(int64)
	if !ok {
		t.Fatalf("latency_ms type = %T, want int64", payload["latency_ms"])
	}
	if latencyMs != 27 {
		t.Fatalf("latency_ms = %d, want 27", latencyMs)
	}
	if got := payload["method"]; got != http.MethodPost {
		t.Fatalf("method = %#v, want %q", got, http.MethodPost)
	}
	if got := payload["path"]; got != "/mcp" {
		t.Fatalf("path = %#v, want %q", got, "/mcp")
	}
	if got := payload["status"]; got != http.StatusAccepted {
		t.Fatalf("status = %#v, want %d", got, http.StatusAccepted)
	}
	if got := payload["rpc_method"]; got != "tools/call" {
		t.Fatalf("rpc_method = %#v, want %q", got, "tools/call")
	}
	if got := payload["tool_name"]; got != "echo" {
		t.Fatalf("tool_name = %#v, want %q", got, "echo")
	}
	if got := payload["bytes_in"]; got != req.ContentLength {
		t.Fatalf("bytes_in = %#v, want %d", got, req.ContentLength)
	}
	if got := payload["bytes_out"]; got != 91 {
		t.Fatalf("bytes_out = %#v, want %d", got, 91)
	}
	if got := payload["subject_team_id"]; got != "team-acme" {
		t.Fatalf("subject_team_id = %#v, want team-acme", got)
	}
	if got := payload["resource_team_id"]; got != "team-owner" {
		t.Fatalf("resource_team_id = %#v, want team-owner", got)
	}
}

func TestGatewayMetricsNotExposedOnMainListener(t *testing.T) {
	t.Parallel()

	registry := prometheus.NewRegistry()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}

	proxy := &gatewayServer{
		proxy:                 newUpstreamReverseProxy(target),
		metrics:               newGatewayMetrics(registry),
		httpClient:            &http.Client{Timeout: 2 * time.Second},
		defaultHumanHeader:    defaultHumanHeader,
		defaultAgentHeader:    defaultAgentHeader,
		defaultTeamHeader:     defaultTeamHeader,
		defaultSessionHeader:  defaultSessionHeader,
		defaultPolicyMode:     defaultPolicyMode,
		defaultPolicyDecision: defaultPolicyDecision,
		defaultPolicyVersion:  "test-policy",
		oauthProviders:        map[string]*oauthProvider{},
	}
	proxy.snapshotPolicy(policySnapshot{Policy: headerPolicy()})

	mux := http.NewServeMux()
	mux.HandleFunc("/", proxy.handleGateway)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	mux.ServeHTTP(recorder, request)

	if strings.Contains(recorder.Body.String(), "mcp_gateway_requests_total") {
		t.Fatalf("main listener exposed prometheus metrics: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestGatewayMetricsAvailableOnMetricsPort(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
	})

	registry := prometheus.NewRegistry()
	metrics := newGatewayMetrics(registry)
	metrics.recordRequest(
		gatewayMetricScope{Namespace: "mcp-servers", Server: "demo", Cluster: "kind", TeamID: "team-acme"},
		httptest.NewRequest(http.MethodPost, "http://proxy.example.com/mcp", nil),
		"tools/call",
		policypkg.Decision{Allowed: true, Status: http.StatusOK, Reason: "allowed", PolicyVersion: "test-policy"},
		http.StatusOK,
		time.Millisecond,
		0,
		0,
	)

	metricsServer := &http.Server{
		Handler: serviceutilMetricsHandler(registry),
	}
	go func() {
		_ = metricsServer.Serve(listener)
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = metricsServer.Shutdown(ctx)
	})

	resp, err := http.Get("http://" + listener.Addr().String() + "/metrics")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if !strings.Contains(string(body), "mcp_gateway_requests_total") {
		t.Fatalf("metrics body missing gateway counter: %s", body)
	}
}

func serviceutilMetricsHandler(registry *prometheus.Registry) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	return mux
}

func TestGatewayMetricsRecordRequestsPolicyDecisionsAndBytes(t *testing.T) {
	t.Parallel()

	registry := prometheus.NewRegistry()
	policy := &policypkg.Document{
		Server: policypkg.Server{
			Name:      "demo",
			Namespace: "mcp-servers",
			TeamID:    "team-acme",
			Cluster:   "kind",
		},
		Auth: &policypkg.Auth{
			Mode:            "header",
			HumanIDHeader:   defaultHumanHeader,
			AgentIDHeader:   defaultAgentHeader,
			TeamIDHeader:    defaultTeamHeader,
			SessionIDHeader: defaultSessionHeader,
		},
		Policy: &policypkg.Config{
			Mode:            "allow-list",
			DefaultDecision: "deny",
			PolicyVersion:   "test-policy",
		},
	}
	proxy := newTestGatewayServer(t, policy, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	proxy.metrics = newGatewayMetrics(registry)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo"}}`
	req := httptest.NewRequest(http.MethodPost, "http://proxy.example.com/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(defaultHumanHeader, "human-1")
	recorder := httptest.NewRecorder()

	proxy.handleGateway(recorder, req)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
	}

	requestLabels := []string{"mcp-servers", "demo", "kind", "team-acme", http.MethodPost, "tools/call", "deny", "403"}
	if got := testutil.ToFloat64(proxy.metrics.requestsTotal.WithLabelValues(requestLabels...)); got != 1 {
		t.Fatalf("requests total = %v, want 1", got)
	}
	if got := testutil.ToFloat64(proxy.metrics.policyDecisionsTotal.WithLabelValues("mcp-servers", "demo", "kind", "team-acme", "deny", "no_matching_grant", "tools/call")); got != 1 {
		t.Fatalf("policy decisions total = %v, want 1", got)
	}
	if got := testutil.ToFloat64(proxy.metrics.requestBytesTotal.WithLabelValues(requestLabels...)); got != float64(len(body)) {
		t.Fatalf("request bytes total = %v, want %d", got, len(body))
	}
	if got := testutil.ToFloat64(proxy.metrics.responseBytesTotal.WithLabelValues(requestLabels...)); got == 0 {
		t.Fatal("response bytes total = 0, want denied response bytes")
	}
	if got := testutil.ToFloat64(proxy.metrics.inflightRequests.WithLabelValues("mcp-servers", "demo", "kind", "team-acme")); got != 0 {
		t.Fatalf("inflight requests = %v, want 0 after request completion", got)
	}
	if metrics := gatherMetricsText(t, registry); !strings.Contains(metrics, "mcp_gateway_request_duration_seconds_bucket") {
		t.Fatalf("duration histogram was not exposed:\n%s", metrics)
	}
}

func TestGatewayMetricsRecordPolicyReloadResults(t *testing.T) {
	t.Parallel()

	registry := prometheus.NewRegistry()
	policyFile := filepath.Join(t.TempDir(), "policy.json")
	writeStampedPolicy(t, policyFile, func(doc *policypkg.Document) {
		doc.Server.TeamID = "team-acme"
		doc.Server.Cluster = "kind"
		doc.Auth = &policypkg.Auth{Mode: "header"}
		doc.Policy = &policypkg.Config{
			Mode:            "observe",
			DefaultDecision: "deny",
			PolicyVersion:   "test-policy",
		}
	})
	proxy := &gatewayServer{
		metrics:               newGatewayMetrics(registry),
		policyFile:            policyFile,
		serverName:            "demo",
		serverNamespace:       "mcp-servers",
		clusterName:           "kind",
		defaultHumanHeader:    defaultHumanHeader,
		defaultAgentHeader:    defaultAgentHeader,
		defaultTeamHeader:     defaultTeamHeader,
		defaultSessionHeader:  defaultSessionHeader,
		defaultPolicyMode:     defaultPolicyMode,
		defaultPolicyDecision: defaultPolicyDecision,
		defaultPolicyVersion:  "test-policy",
	}

	if err := proxy.reloadPolicy(); err != nil {
		t.Fatalf("reloadPolicy() error = %v", err)
	}
	if got := testutil.ToFloat64(proxy.metrics.policyReloadsTotal.WithLabelValues("mcp-servers", "demo", "kind", "team-acme", "success")); got != 1 {
		t.Fatalf("successful policy reloads = %v, want 1", got)
	}
	if got := testutil.ToFloat64(proxy.metrics.policyLastReload.WithLabelValues("mcp-servers", "demo", "kind", "team-acme")); got == 0 {
		t.Fatal("policy last reload timestamp = 0, want non-zero")
	}

	if err := os.WriteFile(policyFile, []byte(`{`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := proxy.reloadPolicy(); err == nil {
		t.Fatal("reloadPolicy() error = nil, want malformed policy error")
	}
	if got := testutil.ToFloat64(proxy.metrics.policyReloadsTotal.WithLabelValues("mcp-servers", "demo", "kind", "team-acme", "error")); got != 1 {
		t.Fatalf("failed policy reloads = %v, want 1", got)
	}
}

func TestGatewayMetricMethodsUseBoundedLabels(t *testing.T) {
	t.Parallel()

	if got := metricHTTPMethod("CUSTOM-" + strings.Repeat("x", 80)); got != "OTHER" {
		t.Fatalf("metricHTTPMethod() = %q, want OTHER", got)
	}
	if got := metricRPCMethod("attacker/" + strings.Repeat("x", 80)); got != "other" {
		t.Fatalf("metricRPCMethod() = %q, want other", got)
	}
	if got := metricRPCMethod("tools/call"); got != "tools/call" {
		t.Fatalf("metricRPCMethod(tools/call) = %q", got)
	}
}

func TestAuditPayloadIncludesToolRiskLevel(t *testing.T) {
	t.Parallel()

	proxy := &gatewayServer{
		serverName:           "example-server",
		serverNamespace:      "mcp-servers",
		defaultPolicyVersion: "test-policy",
	}
	req := httptest.NewRequest(http.MethodPost, "http://proxy.example.com/mcp", strings.NewReader(`{"jsonrpc":"2.0"}`))
	policy := &policypkg.Document{
		Tools: []policypkg.Tool{{
			Name:          "refund_invoice",
			RequiredTrust: "high",
			SideEffect:    "destructive",
		}},
	}

	payload := proxy.auditPayload(
		req,
		"/mcp",
		"tools/call",
		"refund_invoice",
		identityContext{HumanID: "human-1"},
		policy,
		policypkg.Decision{Allowed: true, Reason: "allowed", PolicyVersion: "test-policy"},
		http.StatusOK,
		1,
		2,
	)

	if got := payload["risk_level"]; got != "high" {
		t.Fatalf("risk_level = %#v, want high", got)
	}
}

func TestStartPolicyCacheRequiresConfiguredPolicyFile(t *testing.T) {
	t.Parallel()

	proxy := &gatewayServer{
		policyFile:            filepath.Join(t.TempDir(), "missing-policy.json"),
		defaultHumanHeader:    defaultHumanHeader,
		defaultAgentHeader:    defaultAgentHeader,
		defaultTeamHeader:     defaultTeamHeader,
		defaultSessionHeader:  defaultSessionHeader,
		defaultPolicyMode:     defaultPolicyMode,
		defaultPolicyDecision: defaultPolicyDecision,
		defaultPolicyVersion:  "test-policy",
	}

	if err := proxy.startPolicyCache(); err == nil {
		t.Fatal("startPolicyCache() error = nil, want missing policy file error")
	}
}

func TestEmitIfEnabledDropsWhenQueueIsFull(t *testing.T) {
	t.Parallel()

	registry := prometheus.NewRegistry()
	proxy := &gatewayServer{
		analyticsURL:   "http://analytics.example.com",
		analyticsQueue: make(chan analyticsEvent, 1),
		metrics:        newGatewayMetrics(registry),
	}
	proxy.analyticsQueue <- analyticsEvent{Envelope: events.Envelope{Source: "existing"}}

	done := make(chan struct{})
	go func() {
		proxy.emitIfEnabled(context.Background(), events.Envelope{Source: "dropped"})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("emitIfEnabled() blocked with a full queue")
	}

	select {
	case event := <-proxy.analyticsQueue:
		if event.Envelope.Source != "existing" {
			t.Fatalf("analytics queue head = %#v, want existing event to remain", event)
		}
	default:
		t.Fatal("analytics queue unexpectedly drained")
	}
	if got := proxy.analyticsDropped.Load(); got != 1 {
		t.Fatalf("analytics dropped count = %d, want 1", got)
	}
	if got := testutil.ToFloat64(proxy.metrics.analyticsDropsTotal); got != 1 {
		t.Fatalf("analytics drop metric = %v, want 1", got)
	}
}

func TestEmitIfEnabledDropsWhenDispatcherClosed(t *testing.T) {
	t.Parallel()

	registry := prometheus.NewRegistry()
	proxy := &gatewayServer{
		analyticsURL:    "http://analytics.example.com",
		analyticsClosed: true,
		metrics:         newGatewayMetrics(registry),
	}

	proxy.emitIfEnabled(context.Background(), events.Envelope{Source: "closed", EventType: "mcp.request"})

	if got := proxy.analyticsDropped.Load(); got != 1 {
		t.Fatalf("analytics dropped count = %d, want 1", got)
	}
	if got := testutil.ToFloat64(proxy.metrics.analyticsDropsTotal); got != 1 {
		t.Fatalf("analytics drop metric = %v, want 1", got)
	}
}

func TestShouldLogAnalyticsDropSamples(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		dropped uint64
		want    bool
	}{
		{dropped: 0, want: false},
		{dropped: 1, want: true},
		{dropped: 2, want: true},
		{dropped: 3, want: false},
		{dropped: 4, want: true},
		{dropped: 7, want: false},
		{dropped: 8, want: true},
	} {
		if got := shouldLogAnalyticsDrop(tc.dropped); got != tc.want {
			t.Fatalf("shouldLogAnalyticsDrop(%d) = %v, want %v", tc.dropped, got, tc.want)
		}
	}
}

func TestStopAnalyticsDispatcherDrainsQueue(t *testing.T) {
	t.Parallel()

	var received int32
	ingest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		atomic.AddInt32(&received, 1)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(ingest.Close)

	proxy := &gatewayServer{
		analyticsURL: ingest.URL,
		httpClient:   ingest.Client(),
	}
	proxy.startAnalyticsDispatcher()
	for i := 0; i < 3; i++ {
		proxy.emitIfEnabled(context.Background(), events.Envelope{Source: "proxy", EventType: "mcp.request"})
	}

	proxy.stopAnalyticsDispatcher()

	if got := atomic.LoadInt32(&received); got != 3 {
		t.Fatalf("received analytics events = %d, want 3", got)
	}
}

func TestAnalyticsDispatcherPropagatesTraceContext(t *testing.T) {
	previous := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTextMapPropagator(previous)
	})

	traceparents := make(chan string, 1)
	ingest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		traceparents <- r.Header.Get("traceparent")
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(ingest.Close)

	traceID := trace.TraceID{16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1}
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     trace.SpanID{1, 3, 5, 7, 9, 11, 13, 15},
		TraceFlags: trace.FlagsSampled,
	}))
	proxy := &gatewayServer{
		analyticsURL: ingest.URL,
		httpClient:   ingest.Client(),
	}
	proxy.startAnalyticsDispatcher()
	proxy.emitIfEnabled(ctx, events.Envelope{Source: "proxy", EventType: "mcp.request"})
	proxy.stopAnalyticsDispatcher()

	select {
	case traceparent := <-traceparents:
		if !strings.Contains(traceparent, traceID.String()) {
			t.Fatalf("traceparent = %q, want trace ID %s", traceparent, traceID)
		}
	default:
		t.Fatal("ingest did not receive analytics request")
	}
}

func TestHandleGatewayDoesNotAuditNonRPCRequests(t *testing.T) {
	t.Parallel()

	var analyticsHits int32
	ingest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		atomic.AddInt32(&analyticsHits, 1)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(ingest.Close)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}

	proxy := &gatewayServer{
		proxy:                 newUpstreamReverseProxy(target),
		httpClient:            ingest.Client(),
		analyticsURL:          ingest.URL,
		defaultHumanHeader:    defaultHumanHeader,
		defaultAgentHeader:    defaultAgentHeader,
		defaultTeamHeader:     defaultTeamHeader,
		defaultSessionHeader:  defaultSessionHeader,
		defaultPolicyMode:     defaultPolicyMode,
		defaultPolicyDecision: defaultPolicyDecision,
		defaultPolicyVersion:  "test-policy",
		oauthProviders:        map[string]*oauthProvider{},
	}
	proxy.startAnalyticsDispatcher()

	// GET request (health probe, OAuth discovery, etc.) — must not produce an audit event.
	req := httptest.NewRequest(http.MethodGet, "http://gateway.example.local/health", nil)
	proxy.handleGateway(httptest.NewRecorder(), req)

	proxy.stopAnalyticsDispatcher()

	if got := atomic.LoadInt32(&analyticsHits); got != 0 {
		t.Fatalf("analytics events emitted for non-RPC GET = %d, want 0", got)
	}
}

func TestHandleGatewayDoesNotAuditDeniedNonRPCRequests(t *testing.T) {
	t.Parallel()

	var analyticsHits int32
	ingest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		atomic.AddInt32(&analyticsHits, 1)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(ingest.Close)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}

	proxy := &gatewayServer{
		proxy:                 newUpstreamReverseProxy(target),
		httpClient:            ingest.Client(),
		analyticsURL:          ingest.URL,
		defaultHumanHeader:    defaultHumanHeader,
		defaultAgentHeader:    defaultAgentHeader,
		defaultTeamHeader:     defaultTeamHeader,
		defaultSessionHeader:  defaultSessionHeader,
		defaultPolicyMode:     defaultPolicyMode,
		defaultPolicyDecision: defaultPolicyDecision,
		defaultPolicyVersion:  "test-policy",
		oauthProviders:        map[string]*oauthProvider{},
	}
	proxy.startAnalyticsDispatcher()

	// POST with non-JSON content-type is denied (Indeterminate) but has no rpcMethod —
	// writeDeniedResponse must not emit an audit event for this case.
	body := `not json`
	req := httptest.NewRequest(http.MethodPost, "http://gateway.example.local/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "text/plain")
	req.ContentLength = int64(len(body))
	proxy.handleGateway(httptest.NewRecorder(), req)

	proxy.stopAnalyticsDispatcher()

	if got := atomic.LoadInt32(&analyticsHits); got != 0 {
		t.Fatalf("analytics events emitted for denied non-RPC request = %d, want 0", got)
	}
}

func TestHandleGatewayAuditsDeniedJSONRPCAttempts(t *testing.T) {
	t.Parallel()

	var analyticsHits int32
	ingest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		atomic.AddInt32(&analyticsHits, 1)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(ingest.Close)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}

	proxy := &gatewayServer{
		proxy:                 newUpstreamReverseProxy(target),
		httpClient:            ingest.Client(),
		analyticsURL:          ingest.URL,
		defaultHumanHeader:    defaultHumanHeader,
		defaultAgentHeader:    defaultAgentHeader,
		defaultTeamHeader:     defaultTeamHeader,
		defaultSessionHeader:  defaultSessionHeader,
		defaultPolicyMode:     defaultPolicyMode,
		defaultPolicyDecision: defaultPolicyDecision,
		defaultPolicyVersion:  "test-policy",
		oauthProviders:        map[string]*oauthProvider{},
	}
	proxy.startAnalyticsDispatcher()

	// POST with application/json but malformed body — this is a genuine MCP client
	// attempt (rpc_inspection_failed) and must produce an audit event.
	body := `not valid json`
	req := httptest.NewRequest(http.MethodPost, "http://gateway.example.local/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(body))
	proxy.handleGateway(httptest.NewRecorder(), req)

	proxy.stopAnalyticsDispatcher()

	if got := atomic.LoadInt32(&analyticsHits); got != 1 {
		t.Fatalf("analytics events emitted for denied JSON-RPC attempt = %d, want 1", got)
	}
}

func TestHandleGatewayDoesNotAuditLiveInventoryRequests(t *testing.T) {
	t.Parallel()

	var analyticsHits int32
	ingest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		atomic.AddInt32(&analyticsHits, 1)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(ingest.Close)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}

	proxy := &gatewayServer{
		proxy:                 newUpstreamReverseProxy(target),
		httpClient:            ingest.Client(),
		analyticsURL:          ingest.URL,
		defaultHumanHeader:    defaultHumanHeader,
		defaultAgentHeader:    defaultAgentHeader,
		defaultTeamHeader:     defaultTeamHeader,
		defaultSessionHeader:  defaultSessionHeader,
		defaultPolicyMode:     defaultPolicyMode,
		defaultPolicyDecision: defaultPolicyDecision,
		defaultPolicyVersion:  "test-policy",
		oauthProviders:        map[string]*oauthProvider{},
	}
	proxy.startAnalyticsDispatcher()

	// A valid JSON-RPC request from the live-inventory probe must NOT emit an
	// analytics event — it is an internal platform service call, not user traffic.
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	req := httptest.NewRequest(http.MethodPost, "http://gateway.example.local/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(defaultAgentHeader, "mcp-runtime-live-inventory")
	req.Header.Set(defaultHumanHeader, "mcp-runtime-api")
	req.ContentLength = int64(len(body))
	proxy.handleGateway(httptest.NewRecorder(), req)

	proxy.stopAnalyticsDispatcher()

	if got := atomic.LoadInt32(&analyticsHits); got != 0 {
		t.Fatalf("analytics events emitted for live-inventory request = %d, want 0", got)
	}
}

func TestHandleGatewayAuditsRPCRequests(t *testing.T) {
	t.Parallel()

	var analyticsHits int32
	ingest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		atomic.AddInt32(&analyticsHits, 1)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(ingest.Close)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}

	proxy := &gatewayServer{
		proxy:                 newUpstreamReverseProxy(target),
		httpClient:            ingest.Client(),
		analyticsURL:          ingest.URL,
		defaultHumanHeader:    defaultHumanHeader,
		defaultAgentHeader:    defaultAgentHeader,
		defaultTeamHeader:     defaultTeamHeader,
		defaultSessionHeader:  defaultSessionHeader,
		defaultPolicyMode:     defaultPolicyMode,
		defaultPolicyDecision: defaultPolicyDecision,
		defaultPolicyVersion:  "test-policy",
		oauthProviders:        map[string]*oauthProvider{},
	}
	proxy.startAnalyticsDispatcher()

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo"}}`
	req := httptest.NewRequest(http.MethodPost, "http://gateway.example.local/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(body))
	proxy.handleGateway(httptest.NewRecorder(), req)

	proxy.stopAnalyticsDispatcher()

	if got := atomic.LoadInt32(&analyticsHits); got != 1 {
		t.Fatalf("analytics events emitted for RPC request = %d, want 1", got)
	}
}

type testJWTIssuer struct {
	privateKey *rsa.PrivateKey
	server     *httptest.Server
	url        string
}

func newTestJWTIssuer(t *testing.T) *testJWTIssuer {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	issuer := &testJWTIssuer{privateKey: privateKey}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":   issuer.url,
			"jwks_uri": issuer.server.URL + "/keys",
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{rsaJWK(&privateKey.PublicKey)},
		})
	})

	issuer.server = httptest.NewServer(mux)
	issuer.url = issuer.server.URL
	t.Cleanup(issuer.server.Close)
	return issuer
}

func (i *testJWTIssuer) sign(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = "test-key"
	signed, err := token.SignedString(i.privateKey)
	if err != nil {
		t.Fatalf("SignedString() error = %v", err)
	}
	return signed
}

func newTestGatewayServer(t *testing.T, policy *policypkg.Document, upstream http.HandlerFunc) *gatewayServer {
	t.Helper()

	upstreamServer := httptest.NewServer(upstream)
	t.Cleanup(upstreamServer.Close)

	target, err := url.Parse(upstreamServer.URL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	reverseProxy := newUpstreamReverseProxy(target)
	reverseProxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		t.Fatalf("gateway error: %v", err)
	}

	server := &gatewayServer{
		proxy:                 reverseProxy,
		httpClient:            &http.Client{Timeout: 2 * time.Second},
		defaultHumanHeader:    defaultHumanHeader,
		defaultAgentHeader:    defaultAgentHeader,
		defaultSessionHeader:  defaultSessionHeader,
		defaultPolicyMode:     defaultPolicyMode,
		defaultPolicyDecision: defaultPolicyDecision,
		defaultPolicyVersion:  "test-policy",
		oauthProviders:        map[string]*oauthProvider{},
	}
	server.snapshotPolicy(policySnapshot{Policy: policy, Revision: policy.Revision, LoadedAt: time.Now(), Ready: true})
	return server
}

func gatherMetricsText(t *testing.T, registry *prometheus.Registry) string {
	t.Helper()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	promhttp.HandlerFor(registry, promhttp.HandlerOpts{}).ServeHTTP(recorder, request)
	return recorder.Body.String()
}

func oauthPolicy(issuerURL string) *policypkg.Document {
	return &policypkg.Document{
		Auth: &policypkg.Auth{
			Mode:            "oauth",
			HumanIDHeader:   defaultHumanHeader,
			AgentIDHeader:   defaultAgentHeader,
			TeamIDHeader:    defaultTeamHeader,
			SessionIDHeader: defaultSessionHeader,
			TokenHeader:     "Authorization",
			IssuerURL:       issuerURL,
			Audience:        "http://proxy.example.com/mcp",
		},
		Policy: &policypkg.Config{
			Mode:            "allow-list",
			DefaultDecision: "deny",
			PolicyVersion:   "test-policy",
		},
		Session: &policypkg.Session{
			Required:            true,
			UpstreamTokenHeader: "Authorization",
		},
		Tools: []policypkg.Tool{
			{Name: "echo", RequiredTrust: "low", SideEffect: "read"},
		},
		Grants: []policypkg.Grant{
			{
				Name:               "grant-1",
				HumanID:            "human-1",
				AgentID:            "client-1",
				MaxTrust:           "high",
				AllowedSideEffects: []string{"read"},
				ToolRules:          []policypkg.ToolAccess{{Name: "echo", Decision: "allow"}},
			},
		},
		Sessions: []policypkg.Binding{
			{
				Name:           "session-1",
				HumanID:        "human-1",
				AgentID:        "client-1",
				ConsentedTrust: "high",
			},
		},
	}
}

func headerPolicy() *policypkg.Document {
	return &policypkg.Document{
		Auth: &policypkg.Auth{
			Mode:            "header",
			HumanIDHeader:   defaultHumanHeader,
			AgentIDHeader:   defaultAgentHeader,
			TeamIDHeader:    defaultTeamHeader,
			SessionIDHeader: defaultSessionHeader,
		},
		Policy: &policypkg.Config{
			Mode:            "allow-list",
			DefaultDecision: "deny",
			PolicyVersion:   "test-policy",
		},
		Session: &policypkg.Session{Required: true},
		Tools: []policypkg.Tool{
			{Name: "echo", RequiredTrust: "low", SideEffect: "read"},
		},
		Grants: []policypkg.Grant{
			{
				Name:               "grant-1",
				HumanID:            "human-1",
				AgentID:            "client-1",
				TeamID:             "team-acme",
				MaxTrust:           "high",
				AllowedSideEffects: []string{"read"},
				ToolRules:          []policypkg.ToolAccess{{Name: "echo", Decision: "allow"}},
			},
		},
		Sessions: []policypkg.Binding{
			{
				Name:           "session-1",
				HumanID:        "human-1",
				AgentID:        "client-1",
				TeamID:         "team-acme",
				ConsentedTrust: "high",
			},
		},
	}
}

func rsaJWK(publicKey *rsa.PublicKey) map[string]string {
	return map[string]string{
		"kty": "RSA",
		"alg": "RS256",
		"use": "sig",
		"kid": "test-key",
		"n":   base64.RawURLEncoding.EncodeToString(publicKey.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(publicKey.E)).Bytes()),
	}
}
