package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestConfigDoesNotExposeAPIKey(t *testing.T) {
	mux, err := newMux("/api", "http://127.0.0.1:1", "secret", "api-secret", "")
	if err != nil {
		t.Fatalf("newMux() error = %v", err)
	}

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/config.js", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	if strings.Contains(body, "MCP_API_KEY") || strings.Contains(body, "secret") {
		t.Fatalf("config.js exposed API key material: %q", body)
	}
	if !strings.Contains(body, "MCP_API_BASE") {
		t.Fatalf("config.js missing API base: %q", body)
	}
}

func TestConfigExposesPlatformMode(t *testing.T) {
	t.Setenv("PLATFORM_MODE", "org")
	mux, err := newMux("/api", "http://127.0.0.1:1", "secret", "api-secret", "")
	if err != nil {
		t.Fatalf("newMux() error = %v", err)
	}

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/config.js", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `window.MCP_PLATFORM_MODE = "org"`) {
		t.Fatalf("config.js missing platform mode: %q", body)
	}
}

func TestConfigExposesGoogleClientIDAlias(t *testing.T) {
	t.Setenv("GOOGLE_CLIENT_ID", "")
	t.Setenv("MCP_GOOGLE_CLIENT_ID", "alias-client.apps.googleusercontent.com")
	mux, err := newMux("/api", "http://127.0.0.1:1", "secret", "api-secret", "")
	if err != nil {
		t.Fatalf("newMux() error = %v", err)
	}

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/config.js", nil))

	if !strings.Contains(recorder.Body.String(), `window.MCP_GOOGLE_CLIENT_ID = "alias-client.apps.googleusercontent.com"`) {
		t.Fatalf("config.js missing MCP_GOOGLE_CLIENT_ID alias: %q", recorder.Body.String())
	}
}

func readStaticAsset(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			t.Skipf("static asset %s not found; run the frontend build first", path)
		}
		t.Fatalf("read %s: %v", path, err)
	}
	return body
}

func TestStaticShellLoadsReactBundle(t *testing.T) {
	htmlBody := readStaticAsset(t, "static/index.html")
	html := string(htmlBody)
	for _, want := range []string{
		`<div id="root"></div>`,
		`type="module"`,
		`/assets/`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("react shell missing %q", want)
		}
	}
}

// TestLegacyDashboardAssetsAreGone guards against the legacy static bundle
// being reintroduced - every workflow it served has an accepted React route
// before the legacy bundle can return.
func TestLegacyDashboardAssetsAreGone(t *testing.T) {
	if _, err := os.Stat("static/legacy"); !os.IsNotExist(err) {
		t.Fatalf("static/legacy should have been removed, stat error = %v", err)
	}
}

func TestSecurityHeadersAllowConfiguredExternalAssets(t *testing.T) {
	mux, err := newMux("/api", "http://127.0.0.1:1", "secret", "api-secret", "")
	if err != nil {
		t.Fatalf("newMux() error = %v", err)
	}
	handler := securityHeadersMiddleware(mux)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	csp := recorder.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "https://fonts.googleapis.com") {
		t.Fatalf("CSP should allow stylesheet font origin, got %q", csp)
	}
	if !strings.Contains(csp, "https://accounts.google.com") {
		t.Fatalf("CSP should allow Google sign-in stylesheet origin, got %q", csp)
	}
	if !strings.Contains(csp, "https://fonts.gstatic.com") {
		t.Fatalf("CSP should allow font file origin, got %q", csp)
	}
	if !strings.Contains(csp, "frame-src https://accounts.google.com") {
		t.Fatalf("CSP should allow the Google sign-in iframe, got %q", csp)
	}
	if strings.Contains(csp, "frame-src 'self'") {
		t.Fatalf("CSP should not allow same-origin framing (legacy iframe removed), got %q", csp)
	}
}

func TestCatalogNamespacesForModeScopesPublicEnvToPublicMode(t *testing.T) {
	t.Setenv("PLATFORM_PUBLIC_NAMESPACES", "mcp-servers-public,preview-extra")

	org := catalogNamespacesForMode("org")
	if strings.Join(org, ",") != "mcp-servers-org" {
		t.Fatalf("org namespaces = %v, want [mcp-servers-org]", org)
	}

	public := catalogNamespacesForMode("public")
	if strings.Join(public, ",") != "mcp-servers-public,preview-extra" {
		t.Fatalf("public namespaces = %v, want [mcp-servers-public preview-extra]", public)
	}
}

func TestCatalogNamespaceOverrideIgnoredInTenantMode(t *testing.T) {
	t.Setenv("PLATFORM_CATALOG_NAMESPACE", "custom-catalog")

	if got := defaultCatalogNamespaceForMode("tenant"); got != "" {
		t.Fatalf("tenant default namespace = %q, want empty", got)
	}
	if got := catalogNamespacesForMode("tenant"); len(got) != 0 {
		t.Fatalf("tenant catalog namespaces = %v, want empty", got)
	}
	if got := defaultCatalogNamespaceForMode("org"); got != "custom-catalog" {
		t.Fatalf("org default namespace = %q, want custom-catalog", got)
	}
}

func TestHandleLoginWithOIDCToken(t *testing.T) {
	now := time.Now().UTC()
	previousHook := oidcLoginHook
	oidcLoginHook = func(_ context.Context, upstream, token string) (sessionPrincipal, string, time.Time, error) {
		if upstream != "http://api.example" {
			t.Fatalf("upstream = %q, want http://api.example", upstream)
		}
		if token != "id-token" {
			t.Fatalf("token = %q", token)
		}
		return sessionPrincipal{
			Role:     "user",
			Subject:  "user-123",
			AuthType: "platform_jwt",
		}, "platform-token", now.Add(15 * time.Minute), nil
	}
	defer func() { oidcLoginHook = previousHook }()

	store := newUISessionStore(time.Now)
	login := httptest.NewRecorder()
	handleLogin("", "api-secret", "http://api.example", store).ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(`{"id_token":"id-token"}`)))
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want %d; body=%s", login.Code, http.StatusOK, login.Body.String())
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d, want 1", len(cookies))
	}
	sess, ok := store.get(cookies[0].Value)
	if !ok {
		t.Fatal("expected persisted session")
	}
	if got := sess.UpstreamAuthHeader; got != "Bearer platform-token" {
		t.Fatalf("stored upstream authorization = %q", got)
	}
	if got := sess.UpstreamAuthHeader; strings.Contains(got, "id-token") {
		t.Fatalf("stored upstream authorization leaked raw id token: %q", got)
	}
}

func TestHandleLoginWithOIDCTokenCapsSessionToTokenExpiry(t *testing.T) {
	now := time.Now().UTC().Add(2 * time.Minute)
	previousHook := oidcLoginHook
	oidcLoginHook = func(_ context.Context, upstream, token string) (sessionPrincipal, string, time.Time, error) {
		if upstream != "http://api.example" {
			t.Fatalf("upstream = %q, want http://api.example", upstream)
		}
		if token == "" {
			t.Fatal("token should not be empty")
		}
		return sessionPrincipal{
			Role:     "user",
			Subject:  "user-123",
			AuthType: "platform_jwt",
		}, "platform-token", now.Add(30 * time.Minute), nil
	}
	defer func() { oidcLoginHook = previousHook }()

	store := newUISessionStore(func() time.Time { return now })
	login := httptest.NewRecorder()
	handleLogin("", "api-secret", "http://api.example", store).ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(`{"id_token":"id-token"}`)))
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want %d; body=%s", login.Code, http.StatusOK, login.Body.String())
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d, want 1", len(cookies))
	}
	if cookies[0].MaxAge > int((33 * time.Minute).Seconds()) {
		t.Fatalf("cookie MaxAge = %d, expected <= 33 minutes", cookies[0].MaxAge)
	}
	sess, ok := store.get(cookies[0].Value)
	if !ok {
		t.Fatal("expected persisted session")
	}
	exp := now.Add(30 * time.Minute)
	if sess.ExpiresAt.After(exp.Add(time.Second)) || sess.ExpiresAt.Before(exp.Add(-1*time.Second)) {
		t.Fatalf("session expiry = %s, want %s", sess.ExpiresAt.Format(time.RFC3339), exp.Format(time.RFC3339))
	}
}

func TestLoginOIDCSessionFallsBackToTokenVerificationWhenPlatformStoreUnavailable(t *testing.T) {
	now := time.Now().UTC().Add(2 * time.Minute)
	exp := now.Add(30 * time.Minute)
	payload := fmt.Sprintf(`{"exp":%d}`, exp.Unix())
	idToken := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".sig"

	var paths []string
	previousClient := authHTTPClient
	authHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/api/v1/auth/oidc":
			if r.Method != http.MethodPost {
				t.Fatalf("oidc method = %s, want POST", r.Method)
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read oidc body: %v", err)
			}
			if !strings.Contains(string(body), idToken) {
				t.Fatalf("oidc body = %s, want id token", string(body))
			}
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Header:     http.Header{"content-type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":"platform identity database not configured"}`)),
			}, nil
		case "/api/v1/auth/me":
			if got := r.Header.Get("authorization"); got != "Bearer "+idToken {
				t.Fatalf("fallback authorization = %q, want bearer id token", got)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"content-type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"authenticated":true,"principal":{"role":"user","subject":"user-123","email":"user@example.com"}}`)),
			}, nil
		default:
			t.Fatalf("unexpected request path %q", r.URL.Path)
		}
		return nil, nil
	})}
	t.Cleanup(func() { authHTTPClient = previousClient })

	p, token, expiresAt, err := loginOIDCSession(context.Background(), "http://api.example", idToken)
	if err != nil {
		t.Fatalf("loginOIDCSession() error = %v", err)
	}
	if token != idToken {
		t.Fatalf("token = %q, want original id token", token)
	}
	if p.AuthType != "oidc_jwt" || p.Subject != "user-123" || p.Email != "user@example.com" {
		t.Fatalf("principal = %+v", p)
	}
	if expiresAt.After(exp.Add(time.Second)) || expiresAt.Before(exp.Add(-time.Second)) {
		t.Fatalf("session expiry = %s, want %s", expiresAt.Format(time.RFC3339), exp.Format(time.RFC3339))
	}
	if len(paths) != 2 || paths[0] != "/api/v1/auth/oidc" || paths[1] != "/api/v1/auth/me" {
		t.Fatalf("request paths = %v, want oidc exchange then auth/me fallback", paths)
	}
}

func TestUISessionStateIsEphemeralAcrossStoreRestart(t *testing.T) {
	now := time.Now().UTC()
	previousHook := oidcLoginHook
	oidcLoginHook = func(_ context.Context, upstream, token string) (sessionPrincipal, string, time.Time, error) {
		if upstream != "http://api.example" {
			t.Fatalf("upstream = %q, want http://api.example", upstream)
		}
		if token == "" {
			t.Fatal("token should not be empty")
		}
		return sessionPrincipal{Role: "user", Subject: "user-123", AuthType: "platform_jwt"}, "platform-token", now.Add(10 * time.Minute), nil
	}
	defer func() { oidcLoginHook = previousHook }()

	originalStore := newUISessionStore(func() time.Time { return now })
	login := httptest.NewRecorder()
	handleLogin("", "api-secret", "http://api.example", originalStore).ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(`{"id_token":"id-token"}`)))
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want %d; body=%s", login.Code, http.StatusOK, login.Body.String())
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d, want 1", len(cookies))
	}

	beforeRestart := httptest.NewRecorder()
	beforeReq := httptest.NewRequest(http.MethodGet, "/auth/status", nil)
	beforeReq.AddCookie(cookies[0])
	handleStatus(originalStore).ServeHTTP(beforeRestart, beforeReq)
	if !strings.Contains(beforeRestart.Body.String(), `"authenticated":true`) {
		t.Fatalf("status before restart = %s", beforeRestart.Body.String())
	}

	restartedStore := newUISessionStore(func() time.Time { return now })
	afterRestart := httptest.NewRecorder()
	afterReq := httptest.NewRequest(http.MethodGet, "/auth/status", nil)
	afterReq.AddCookie(cookies[0])
	handleStatus(restartedStore).ServeHTTP(afterRestart, afterReq)
	if !strings.Contains(afterRestart.Body.String(), `"authenticated":false`) {
		t.Fatalf("status after restart = %s", afterRestart.Body.String())
	}
}

func TestHandleLoginWithPassword(t *testing.T) {
	previousHook := passwordLoginHook
	expiresAt := time.Now().Add(15 * time.Minute)
	passwordLoginHook = func(_ context.Context, upstream, email, password string) (sessionPrincipal, string, time.Time, error) {
		if upstream != "http://api.example" {
			t.Fatalf("upstream = %q, want http://api.example", upstream)
		}
		if email != "admin@example.com" || password != "test-password" {
			t.Fatalf("credentials = %q/%q", email, password)
		}
		return sessionPrincipal{
			Role:     "admin",
			Subject:  "user-1",
			Email:    "admin@example.com",
			AuthType: "platform_jwt",
		}, "platform-token", expiresAt, nil
	}
	defer func() { passwordLoginHook = previousHook }()

	store := newUISessionStore(time.Now)
	login := httptest.NewRecorder()
	handleLogin("", "api-secret", "http://api.example", store).ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(`{"email":"admin@example.com","password":"test-password"}`)))
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want %d; body=%s", login.Code, http.StatusOK, login.Body.String())
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d, want 1", len(cookies))
	}
	sess, ok := store.get(cookies[0].Value)
	if !ok {
		t.Fatal("password login did not store a session")
	}
	if sess.ExpiresAt.After(expiresAt.Add(time.Second)) || sess.ExpiresAt.Before(expiresAt.Add(-time.Second)) {
		t.Fatalf("session expiry = %s, want token expiry %s", sess.ExpiresAt, expiresAt)
	}
}

func TestLoginClientIDUsesForwardedFor(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
	req.RemoteAddr = "10.0.0.2:12345"
	req.Header.Set("x-forwarded-for", "203.0.113.10, 10.0.0.2")

	if got := loginClientID(req); got != "203.0.113.10" {
		t.Fatalf("loginClientID() = %q, want forwarded client", got)
	}
}

func TestLoginClientIDEmptyForwardedForFallsBackToRemoteAddr(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
	req.RemoteAddr = "198.51.100.20:54321"
	req.Header.Set("x-forwarded-for", " , ")

	if got := loginClientID(req); got != "198.51.100.20" {
		t.Fatalf("loginClientID() = %q, want remote addr host", got)
	}
}

func TestLoginClientIDUnknownWhenNoAddressAvailable(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
	req.RemoteAddr = ""

	if got := loginClientID(req); got != loginClientUnknownIP {
		t.Fatalf("loginClientID() = %q, want %q", got, loginClientUnknownIP)
	}
}

func TestHandleLoginLocksOutRepeatedFailures(t *testing.T) {
	restore := useLoginAttemptTrackerForTest(t)
	defer restore()

	handler := handleLogin("secret", "api-secret", "http://api.example", newUISessionStore(time.Now))
	for i := 0; i < loginFailureThreshold; i++ {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, loginRequestFrom("198.51.100.10", `{"api_key":"wrong"}`))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d status = %d, want %d", i+1, recorder.Code, http.StatusUnauthorized)
		}
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, loginRequestFrom("198.51.100.10", `{"api_key":"secret"}`))
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("locked-out status = %d, want %d", recorder.Code, http.StatusTooManyRequests)
	}
}

func TestHandleLoginSuccessResetsFailureCounter(t *testing.T) {
	restore := useLoginAttemptTrackerForTest(t)
	defer restore()

	handler := handleLogin("secret", "api-secret", "http://api.example", newUISessionStore(time.Now))
	for i := 0; i < 2; i++ {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, loginRequestFrom("198.51.100.11", `{"api_key":"wrong"}`))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d status = %d, want %d", i+1, recorder.Code, http.StatusUnauthorized)
		}
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, loginRequestFrom("198.51.100.11", `{"api_key":"secret"}`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("success status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	loginAttempts.mu.Lock()
	defer loginAttempts.mu.Unlock()
	if got := loginAttempts.clients["198.51.100.11"].failures; got != 0 {
		t.Fatalf("failure count after success = %d, want 0", got)
	}
}

func TestLoginAttemptTrackerPrunesIdleClientsPeriodically(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tracker := newLoginAttemptTracker(func() time.Time { return now })

	tracker.recordFailure("client-old")
	now = now.Add(loginAttemptIdleTTL + time.Second)
	tracker.recordFailure("client-new")

	if _, ok := tracker.clients["client-old"]; ok {
		t.Fatal("idle login attempt client was not pruned")
	}
	if _, ok := tracker.clients["client-new"]; !ok {
		t.Fatal("new login attempt client missing")
	}
}

func TestLoginAttemptTrackerDoesNotPruneOnEveryRequest(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tracker := newLoginAttemptTracker(func() time.Time { return now })
	tracker.recordFailure("client")
	firstPrune := tracker.lastPrune

	now = now.Add(loginAttemptPruneInterval / 2)
	tracker.recordFailure("client")

	if !tracker.lastPrune.Equal(firstPrune) {
		t.Fatalf("last prune = %s, want %s", tracker.lastPrune, firstPrune)
	}
}

func TestLoginAttemptTrackerCapsClientsAndPreservesLockedClients(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tracker := newLoginAttemptTracker(func() time.Time { return now })
	tracker.clients["locked"] = &loginClientState{
		lastSeen:    now,
		lockedUntil: now.Add(loginLockoutDuration),
	}

	for i := 0; i < loginAttemptMaxClients; i++ {
		now = now.Add(time.Millisecond)
		if !tracker.allow(fmt.Sprintf("client-%d", i)) {
			t.Fatalf("client-%d should be allowed on first attempt", i)
		}
	}

	if got := len(tracker.clients); got > loginAttemptMaxClients {
		t.Fatalf("login attempt clients = %d, want <= %d", got, loginAttemptMaxClients)
	}
	if _, ok := tracker.clients["locked"]; !ok {
		t.Fatal("active lockout was evicted before unlocked clients")
	}
	if _, ok := tracker.clients["client-0"]; ok {
		t.Fatal("oldest unlocked client was not evicted")
	}
}

func useLoginAttemptTrackerForTest(t *testing.T) func() {
	t.Helper()
	previous := loginAttempts
	loginAttempts = newLoginAttemptTracker(func() time.Time {
		return time.Unix(1_700_000_000, 0)
	})
	return func() {
		loginAttempts = previous
	}
}

func loginRequestFrom(clientID, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(body))
	req.RemoteAddr = "10.0.0.2:12345"
	req.Header.Set("x-forwarded-for", clientID)
	return req
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestSecurityHeadersMiddlewareAlwaysSetsBaselineHeaders(t *testing.T) {
	handler := securityHeadersMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	wantContains := map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "strict-origin-when-cross-origin",
		"Permissions-Policy":      "interest-cohort=()",
		"Content-Security-Policy": "frame-ancestors 'none'",
	}
	for header, fragment := range wantContains {
		got := rec.Header().Get(header)
		if !strings.Contains(got, fragment) {
			t.Fatalf("%s = %q, want substring %q", header, got, fragment)
		}
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self' https://accounts.google.com https://apis.google.com") {
		t.Fatalf("Content-Security-Policy script-src = %q, want external scripts without unsafe-inline", csp)
	}
	if strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
		t.Fatalf("Content-Security-Policy script-src allows unsafe-inline: %q", csp)
	}
	if rec.Header().Get("Strict-Transport-Security") != "" {
		t.Fatalf("HSTS should not be set on plain HTTP, got %q", rec.Header().Get("Strict-Transport-Security"))
	}
}

func TestSecurityHeadersMiddlewareSetsHSTSWhenForwardedHTTPS(t *testing.T) {
	handler := securityHeadersMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Strict-Transport-Security"); !strings.Contains(got, "max-age=") {
		t.Fatalf("Strict-Transport-Security = %q, want max-age", got)
	}
}

func TestSecurityHeadersMiddlewareSetsCacheControlOnAPI(t *testing.T) {
	handler := securityHeadersMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/runtime/servers", nil))
	if got := rec.Header().Get("Cache-Control"); got != "no-store, no-cache, must-revalidate" {
		t.Fatalf("Cache-Control on /api = %q, want no-store, no-cache, must-revalidate", got)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth/login", nil))
	if got := rec.Header().Get("Cache-Control"); got != "no-store, no-cache, must-revalidate" {
		t.Fatalf("Cache-Control on /auth/ = %q, want no-store, no-cache, must-revalidate", got)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/styles.css", nil))
	if got := rec.Header().Get("Cache-Control"); got != "" {
		t.Fatalf("Cache-Control on static asset = %q, want empty", got)
	}
}

func TestHTTPSRedirectMiddlewareAutoModeRedirectsPublicHTTP(t *testing.T) {
	handler := httpsRedirectMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("next handler must not be called when redirecting")
	}), "auto")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard?x=1", nil)
	req.Host = "platform.example.com"
	req.Header.Set("X-Forwarded-Proto", "http")
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusPermanentRedirect {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusPermanentRedirect)
	}
	if got := rec.Header().Get("Location"); got != "https://platform.example.com/dashboard?x=1" {
		t.Fatalf("Location = %q", got)
	}
}

func TestHTTPSRedirectMiddlewareAutoModeSkipsLocalhost(t *testing.T) {
	called := false
	handler := httpsRedirectMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
	}), "auto")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "localhost:18080"
	req.Header.Set("X-Forwarded-Proto", "http")
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !called {
		t.Fatalf("expected pass-through for localhost, got status=%d called=%v", rec.Code, called)
	}
}

func TestHTTPSRedirectMiddlewareSkipsAdminCheckForwardAuth(t *testing.T) {
	called := false
	handler := httpsRedirectMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusUnauthorized)
	}), "auto")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/admin-check", nil)
	req.Host = "mcp-ui.mcp-platform.svc.cluster.local:8082"
	req.Header.Set("X-Forwarded-Proto", "http")
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized || !called {
		t.Fatalf("expected forward-auth admin check to bypass HTTPS redirect, got status=%d called=%v", rec.Code, called)
	}
	if got := rec.Header().Get("Location"); got != "" {
		t.Fatalf("Location = %q, want no redirect", got)
	}
}

func TestHTTPSRedirectMiddlewareDisabledMode(t *testing.T) {
	called := false
	handler := httpsRedirectMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
	}), "false")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "platform.example.com"
	req.Header.Set("X-Forwarded-Proto", "http")
	handler.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected pass-through when UI_REQUIRE_HTTPS=false")
	}
}

func TestHTTPSRedirectMiddlewareForcedAliasesNoForwardedProto(t *testing.T) {
	for _, mode := range []string{"true", "on", "1", "yes"} {
		t.Run(mode, func(t *testing.T) {
			handler := httpsRedirectMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				t.Fatal("next handler must not be called when redirecting")
			}), mode)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
			req.Host = "platform.example.com"
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusPermanentRedirect {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusPermanentRedirect)
			}
			if got := rec.Header().Get("Location"); got != "https://platform.example.com/dashboard" {
				t.Fatalf("Location = %q", got)
			}
		})
	}
}

func TestHTTPSRedirectMiddlewarePassesThroughHTTPS(t *testing.T) {
	called := false
	handler := httpsRedirectMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
	}), "auto")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "platform.example.com"
	req.Header.Set("X-Forwarded-Proto", "https")
	handler.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected HTTPS request to pass through")
	}
}

func TestHandleAdminCheck(t *testing.T) {
	store := newUISessionStore(time.Now)
	adminSess, err := store.createSession(context.Background(), uiSession{
		Principal: sessionPrincipal{Role: "admin", Subject: "admin-1"},
	})
	if err != nil {
		t.Fatalf("createSession admin: %v", err)
	}
	userSess, err := store.createSession(context.Background(), uiSession{
		Principal: sessionPrincipal{Role: "user", Subject: "user-1"},
	})
	if err != nil {
		t.Fatalf("createSession user: %v", err)
	}

	cases := []struct {
		name         string
		adminKeys    string
		apiKeys      string
		cookie       *http.Cookie
		apiKeyHeader string
		legacyAdmin  bool
		want         int
	}{
		{name: "no_credential", apiKeys: "ui-key,admin-key", adminKeys: "admin-key", want: http.StatusUnauthorized},
		{name: "admin_session", apiKeys: "ui-key", cookie: &http.Cookie{Name: sessionCookieName, Value: adminSess.ID}, want: http.StatusNoContent},
		{name: "user_session_rejected", apiKeys: "ui-key", cookie: &http.Cookie{Name: sessionCookieName, Value: userSess.ID}, want: http.StatusUnauthorized},
		{name: "admin_api_key", apiKeys: "ui-key,admin-key", adminKeys: "admin-key", apiKeyHeader: "admin-key", want: http.StatusNoContent},
		{name: "non_admin_api_key_rejected", apiKeys: "ui-key,admin-key", adminKeys: "admin-key", apiKeyHeader: "ui-key", want: http.StatusUnauthorized},
		{name: "admin_unset_rejects_api_key_by_default", apiKeys: "ui-key", apiKeyHeader: "ui-key", want: http.StatusUnauthorized},
		{name: "legacy_fallback_allows_api_key_when_admin_unset", apiKeys: "ui-key", apiKeyHeader: "ui-key", legacyAdmin: true, want: http.StatusNoContent},
		{name: "unknown_api_key", apiKeys: "ui-key", apiKeyHeader: "rando", want: http.StatusUnauthorized},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := handleAdminCheck(store, parseAPIKeyList(tc.apiKeys), parseAPIKeyList(tc.adminKeys), tc.legacyAdmin)
			req := httptest.NewRequest(http.MethodGet, "/auth/admin-check", nil)
			if tc.cookie != nil {
				req.AddCookie(tc.cookie)
			}
			if tc.apiKeyHeader != "" {
				req.Header.Set("x-api-key", tc.apiKeyHeader)
			}
			rec := httptest.NewRecorder()
			h(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestLegacyAdminAPIKeyFallbackEnabledForUIAdminCheck(t *testing.T) {
	t.Setenv("MCP_RUNTIME_TEST_MODE", "")
	t.Setenv("MCP_LEGACY_ADMIN_API_KEY_FALLBACK", "")
	t.Setenv("LEGACY_ADMIN_API_KEY_FALLBACK", "")
	if legacyAdminAPIKeyFallbackEnabled() {
		t.Fatal("legacy fallback should be disabled by default")
	}

	t.Setenv("MCP_RUNTIME_TEST_MODE", "1")
	if !legacyAdminAPIKeyFallbackEnabled() {
		t.Fatal("legacy fallback should be enabled in runtime test mode")
	}

	t.Setenv("MCP_RUNTIME_TEST_MODE", "")
	t.Setenv("MCP_LEGACY_ADMIN_API_KEY_FALLBACK", "true")
	if !legacyAdminAPIKeyFallbackEnabled() {
		t.Fatal("legacy fallback should honor explicit override")
	}
}

func TestSecureCookieCanBeForcedByConfig(t *testing.T) {
	previous := forceSecureCookie
	forceSecureCookie = true
	t.Cleanup(func() {
		forceSecureCookie = previous
	})

	req := httptest.NewRequest(http.MethodGet, "http://platform.example.com/", nil)
	req.Header.Set("X-Forwarded-Proto", "http")

	if !secureCookie(req) {
		t.Fatal("secureCookie() = false, want true when forceSecureCookie=true")
	}
}
