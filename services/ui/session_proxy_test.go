package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSessionProxyWriteAllowlist(t *testing.T) {
	cases := []struct {
		method string
		path   string
		want   bool
	}{
		{http.MethodPatch, "/runtime/grants/mcp-servers/demo", true},
		{http.MethodDelete, "/runtime/sessions/mcp-servers/demo", true},
		{http.MethodPost, "/runtime/teams", true},
		{http.MethodPost, "/runtime/teams/acme/users", true},
		{http.MethodPost, "/runtime/teams/acme/agents", true},
		{http.MethodPatch, "/runtime/agents/agt_01arz3ndektsv4rrffq69g5fav", true},
		{http.MethodPost, "/runtime/agents/agt_01arz3ndektsv4rrffq69g5fav/deactivate", true},
		{http.MethodPost, "/runtime/grants/mcp-team-a/cross-team/revoke-sessions", true},
		{http.MethodPut, "/runtime/teams/acme/members/user-1", true},
		{http.MethodPost, "/runtime/actions/restart", true},
		{http.MethodPatch, "/runtime/grants/mcp-servers/demo/extra", false},
		{http.MethodPost, "/runtime/teams/acme/other", false},
		{http.MethodPost, "/runtime/agents/agt_1/delete", false},
		{http.MethodPost, "/runtime/servers", false},
		{http.MethodDelete, "/runtime/servers/mcp-servers/demo", true},
		{http.MethodPatch, "/runtime/servers/mcp-servers/demo", false},
		{http.MethodDelete, "/runtime/servers/mcp-servers/demo/extra", false},
	}
	for _, tc := range cases {
		if got := sessionProxyWriteAllowed(tc.method, tc.path); got != tc.want {
			t.Errorf("sessionProxyWriteAllowed(%q, %q) = %v, want %v", tc.method, tc.path, got, tc.want)
		}
	}
}

func TestSessionProxyRequiresCSRFForWritesAndForwardsBody(t *testing.T) {
	var gotMethod, gotAuth, gotCSRF, gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAuth = r.Header.Get("authorization")
		gotCSRF = r.Header.Get(csrfHeaderName)
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(upstream.Close)

	proxy := newTestSessionProxy(t, upstream.URL)
	sess := createTestSession(t, proxy.store, uiSession{UpstreamAuthHeader: "Bearer session-token"})

	withoutToken := httptest.NewRequest(http.MethodPatch, "/api/ui/v1/runtime/grants/mcp-servers/demo", bytes.NewBufferString(`{"disabled":true}`))
	withoutToken.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, withoutToken)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status = %d, want %d", rec.Code, http.StatusForbidden)
	}

	withToken := httptest.NewRequest(http.MethodPatch, "/api/ui/v1/runtime/grants/mcp-servers/demo", bytes.NewBufferString(`{"disabled":true}`))
	withToken.Host = "ui.example.com"
	withToken.Header.Set("origin", "http://ui.example.com")
	withToken.Header.Set(csrfHeaderName, sess.CSRFToken)
	withToken.Header.Set("authorization", "Bearer attacker-token")
	withToken.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	rec = httptest.NewRecorder()
	proxy.ServeHTTP(rec, withToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid CSRF status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if gotMethod != http.MethodPatch || gotAuth != "Bearer session-token" || gotBody != `{"disabled":true}` {
		t.Fatalf("upstream request = method %q auth %q body %q", gotMethod, gotAuth, gotBody)
	}
	if gotCSRF != "" {
		t.Fatalf("CSRF token leaked upstream: %q", gotCSRF)
	}
}

func TestParseRuntimeUpstream(t *testing.T) {
	if _, err := parseRuntimeUpstream(""); err == nil {
		t.Fatal("empty upstream should fail")
	}
	if _, err := parseRuntimeUpstream("ftp://runtime.example"); err == nil {
		t.Fatal("non-http upstream should fail")
	}
	if _, err := parseRuntimeUpstream("http://user:pass@runtime.example"); err == nil {
		t.Fatal("userinfo upstream should fail")
	}
	got, err := parseRuntimeUpstream("http://mcp-runtime-api.mcp-sentinel.svc.cluster.local:8084")
	if err != nil {
		t.Fatalf("parseRuntimeUpstream() error = %v", err)
	}
	if got.Scheme != "http" || got.Host != "mcp-runtime-api.mcp-sentinel.svc.cluster.local:8084" {
		t.Fatalf("parsed upstream = %s", got)
	}
}

func TestSessionProxyUpstreamPathAllowlist(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{in: "/api/ui/v1/runtime/namespaces", want: "/api/v1/runtime/namespaces", ok: true},
		{in: "/api/ui/v1/runtime/servers", want: "/api/v1/runtime/servers", ok: true},
		{in: "/api/ui/v1/runtime/tools", want: "/api/v1/runtime/tools", ok: true},
		{in: "/api/ui/v1/runtime/observability/prometheus/query", want: "/api/v1/runtime/observability/prometheus/query", ok: true},
		{in: "/api/ui/v1/runtime/observability/grafana/dashboard", want: "/api/v1/runtime/observability/grafana/dashboard", ok: true},
		{in: "/api/ui/v1/runtime/servers/", want: "/api/v1/runtime/servers", ok: true},
		{in: "/api/ui/v1/events", want: "/api/v1/events", ok: true},
		{in: "/api/ui/v1/runtime/unknown", ok: false},
		{in: "/api/ui/v1/runtime/adapter/sessions", ok: false},
		{in: "/api/v1/runtime/servers", ok: false},
	}
	for _, tc := range cases {
		got, ok := sessionProxyUpstreamPath(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("sessionProxyUpstreamPath(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestSessionProxyInjectsBearerAndStripsClientAuth(t *testing.T) {
	var gotPath, gotQuery, gotAuth, gotAPIKey, gotCookie, gotSource, gotForwardedHost, gotForwardedProto string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("authorization")
		gotAPIKey = r.Header.Get("x-api-key")
		gotCookie = r.Header.Get("cookie")
		gotSource = r.Header.Get("x-mcp-source")
		gotForwardedHost = r.Header.Get("x-forwarded-host")
		gotForwardedProto = r.Header.Get("x-forwarded-proto")
		w.Header().Set("content-type", "application/json")
		w.Header().Set("x-upstream-trace", "trace-1")
		w.Header().Set("set-cookie", "leaked=1")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"servers":[]}`)
	}))
	t.Cleanup(upstream.Close)

	proxy := newTestSessionProxy(t, upstream.URL)
	sess := createTestSession(t, proxy.store, uiSession{
		Principal:          sessionPrincipal{Role: "user", Email: "user@example.com"},
		UpstreamAuthHeader: "Bearer session-token",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/ui/v1/runtime/servers?namespace=mcp-servers", nil)
	req.Host = "localhost:18080"
	req.Header.Set("x-forwarded-host", "platform.example.test")
	req.Header.Set("x-forwarded-proto", "https")
	req.Header.Set("authorization", "Bearer attacker-token")
	req.Header.Set("x-api-key", "attacker-key")
	req.Header.Set("cookie", sessionCookieName+"="+sess.ID)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})

	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if gotPath != "/api/v1/runtime/servers" {
		t.Fatalf("upstream path = %q", gotPath)
	}
	if gotQuery != "namespace=mcp-servers" {
		t.Fatalf("upstream query = %q", gotQuery)
	}
	if gotAuth != "Bearer session-token" {
		t.Fatalf("upstream authorization = %q", gotAuth)
	}
	if gotAPIKey != "" {
		t.Fatalf("upstream x-api-key = %q, want empty", gotAPIKey)
	}
	if gotCookie != "" {
		t.Fatalf("upstream cookie = %q, want empty", gotCookie)
	}
	if gotSource != "ui" {
		t.Fatalf("x-mcp-source = %q", gotSource)
	}
	if gotForwardedHost != "platform.example.test" || gotForwardedProto != "https" {
		t.Fatalf("public origin headers = %q/%q", gotForwardedHost, gotForwardedProto)
	}
	if rec.Header().Get("content-type") != "application/json" {
		t.Fatalf("content-type = %q", rec.Header().Get("content-type"))
	}
	if rec.Header().Get("x-upstream-trace") != "trace-1" {
		t.Fatalf("missing propagated upstream header")
	}
	if rec.Header().Get("set-cookie") != "" {
		t.Fatalf("set-cookie leaked to browser: %q", rec.Header().Get("set-cookie"))
	}
	body := rec.Body.String()
	if body != `{"servers":[]}` {
		t.Fatalf("body = %q", body)
	}
	if strings.Contains(body, "session-token") || strings.Contains(body, "attacker-token") {
		t.Fatalf("response leaked credential: %q", body)
	}
}

func TestSessionProxyOpensObservabilityDashboardWithPlatformSession(t *testing.T) {
	var gotPath, gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("authorization")
		w.Header().Set("content-type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<html>server observability</html>")
	}))
	t.Cleanup(upstream.Close)

	proxy := newTestSessionProxy(t, upstream.URL)
	sess := createTestSession(t, proxy.store, uiSession{
		Principal:          sessionPrincipal{Role: "user", Email: "user@example.com"},
		UpstreamAuthHeader: "Bearer platform-session-token",
	})
	request := httptest.NewRequest(http.MethodGet, "/api/ui/v1/runtime/observability/grafana/dashboard?namespace=team-a&server=buddy", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	recorder := httptest.NewRecorder()
	proxy.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if gotPath != "/api/v1/runtime/observability/grafana/dashboard" || gotAuth != "Bearer platform-session-token" {
		t.Fatalf("upstream path/auth = %q/%q", gotPath, gotAuth)
	}
	if got := recorder.Header().Get("content-type"); got != "text/html; charset=utf-8" {
		t.Fatalf("content-type = %q", got)
	}
}

func TestSessionProxyInjectsAPIKey(t *testing.T) {
	var gotAuth, gotAPIKey string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("authorization")
		gotAPIKey = r.Header.Get("x-api-key")
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, `{"namespaces":[]}`)
	}))
	t.Cleanup(upstream.Close)

	proxy := newTestSessionProxy(t, upstream.URL)
	sess := createTestSession(t, proxy.store, uiSession{
		Principal:      sessionPrincipal{Role: "admin", AuthType: "ui_api_key"},
		UpstreamAPIKey: "upstream-service-key",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/ui/v1/runtime/namespaces", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if gotAuth != "" {
		t.Fatalf("authorization = %q, want empty", gotAuth)
	}
	if gotAPIKey != "upstream-service-key" {
		t.Fatalf("x-api-key = %q", gotAPIKey)
	}
	if strings.Contains(rec.Body.String(), "upstream-service-key") {
		t.Fatalf("response leaked api key: %q", rec.Body.String())
	}
}

func TestSessionProxyRoutesAnalyticsToAnalyticsUpstream(t *testing.T) {
	var gotPath, gotAuth string
	analytics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("authorization")
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, `{"events":[]}`)
	}))
	t.Cleanup(analytics.Close)
	runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("runtime upstream should not receive analytics request: %s", r.URL.Path)
	}))
	t.Cleanup(runtime.Close)
	runtimeBase, err := parseRuntimeUpstream(runtime.URL)
	if err != nil {
		t.Fatal(err)
	}
	analyticsBase, err := parseRuntimeUpstream(analytics.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := newSessionProxyWithUpstreams(runtimeBase, analyticsBase, newUISessionStore(time.Now))
	sess := createTestSession(t, proxy.store, uiSession{UpstreamAuthHeader: "Bearer analytics-token"})
	req := httptest.NewRequest(http.MethodGet, "/api/ui/v1/events?limit=20", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if gotPath != "/api/v1/events" || gotAuth != "Bearer analytics-token" {
		t.Fatalf("analytics request path/auth = %q/%q", gotPath, gotAuth)
	}
}

func TestSessionProxyPropagatesUpstreamStatus(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":"forbidden"}`)
	}))
	t.Cleanup(upstream.Close)

	proxy := newTestSessionProxy(t, upstream.URL)
	sess := createTestSession(t, proxy.store, uiSession{UpstreamAuthHeader: "Bearer session-token"})
	req := httptest.NewRequest(http.MethodGet, "/api/ui/v1/runtime/tools", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if rec.Body.String() != `{"error":"forbidden"}` {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestSessionProxyUnauthorizedWithoutSession(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("upstream must not be called")
	}))
	t.Cleanup(upstream.Close)

	proxy := newTestSessionProxy(t, upstream.URL)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/ui/v1/runtime/servers", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if !strings.Contains(rec.Body.String(), `"error":"unauthorized"`) {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestSessionProxyUnauthorizedForUnknownCookie(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("upstream must not be called")
	}))
	t.Cleanup(upstream.Close)

	proxy := newTestSessionProxy(t, upstream.URL)
	req := httptest.NewRequest(http.MethodGet, "/api/ui/v1/runtime/servers", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "not-a-session"})
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestSessionProxyUnauthorizedForExpiredSession(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("upstream must not be called")
	}))
	t.Cleanup(upstream.Close)

	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	current := now
	store := newUISessionStore(func() time.Time { return current })
	base, err := parseRuntimeUpstream(upstream.URL)
	if err != nil {
		t.Fatalf("parseRuntimeUpstream() error = %v", err)
	}
	proxy := newSessionProxy(base, store)
	sess, err := store.createSession(context.Background(), uiSession{
		Principal:          sessionPrincipal{Role: "user"},
		UpstreamAuthHeader: "Bearer session-token",
		ExpiresAt:          now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("createSession() error = %v", err)
	}
	current = now.Add(2 * time.Minute)

	req := httptest.NewRequest(http.MethodGet, "/api/ui/v1/runtime/servers", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestSessionProxyRejectsUnknownAndNestedPaths(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("upstream must not be called")
	}))
	t.Cleanup(upstream.Close)
	proxy := newTestSessionProxy(t, upstream.URL)
	sess := createTestSession(t, proxy.store, uiSession{UpstreamAuthHeader: "Bearer session-token"})

	for _, path := range []string{
		"/api/ui/v1/runtime/unknown",
		"/api/ui/v1/runtime/unknown/mcp-servers/demo",
		"/api/ui/v1/runtime/adapter/sessions",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
		rec := httptest.NewRecorder()
		proxy.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want %d", path, rec.Code, http.StatusNotFound)
		}
	}
}

func TestSessionProxyRejectsNonGET(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("upstream must not be called")
	}))
	t.Cleanup(upstream.Close)
	proxy := newTestSessionProxy(t, upstream.URL)
	sess := createTestSession(t, proxy.store, uiSession{UpstreamAuthHeader: "Bearer session-token"})
	req := httptest.NewRequest(http.MethodPost, "/api/ui/v1/runtime/servers", strings.NewReader(`{}`))
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	if rec.Header().Get("allow") != http.MethodGet {
		t.Fatalf("allow = %q", rec.Header().Get("allow"))
	}
}

func TestSessionProxyUpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	proxy := newTestSessionProxy(t, upstream.URL)
	upstream.Close()
	sess := createTestSession(t, proxy.store, uiSession{UpstreamAuthHeader: "Bearer session-token"})
	req := httptest.NewRequest(http.MethodGet, "/api/ui/v1/runtime/servers", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
	if strings.Contains(rec.Body.String(), "session-token") {
		t.Fatalf("error body leaked credential: %q", rec.Body.String())
	}
}

func TestNewMuxRejectsInvalidRuntimeUpstream(t *testing.T) {
	t.Setenv("RUNTIME_UPSTREAM", "ftp://runtime.example")
	if _, err := newMux("/api/v1", "http://127.0.0.1:1", "secret", "api-secret", ""); err == nil {
		t.Fatal("newMux() should reject invalid RUNTIME_UPSTREAM")
	}
}

func TestNewMuxRejectsInvalidAnalyticsUpstream(t *testing.T) {
	t.Setenv("ANALYTICS_UPSTREAM", "ftp://analytics.example")
	if _, err := newMux("/api/v1", "http://127.0.0.1:1", "secret", "api-secret", ""); err == nil {
		t.Fatal("newMux() should reject invalid ANALYTICS_UPSTREAM")
	}
}

func TestNewMuxSessionProxyUsesStore(t *testing.T) {
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("authorization")
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, `{"tools":[]}`)
	}))
	t.Cleanup(upstream.Close)
	t.Setenv("RUNTIME_UPSTREAM", upstream.URL)

	mux, err := newMux("/api/v1", "http://127.0.0.1:1", "secret", "api-secret", "")
	if err != nil {
		t.Fatalf("newMux() error = %v", err)
	}
	sess := createTestSession(t, sessions, uiSession{UpstreamAuthHeader: "Bearer mux-token"})
	t.Cleanup(func() { sessions.delete(sess.ID) })

	req := httptest.NewRequest(http.MethodGet, "/api/ui/v1/runtime/tools", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if gotAuth != "Bearer mux-token" {
		t.Fatalf("upstream authorization = %q", gotAuth)
	}
}

func newTestSessionProxy(t *testing.T, upstreamURL string) *sessionProxy {
	t.Helper()
	base, err := parseRuntimeUpstream(upstreamURL)
	if err != nil {
		t.Fatalf("parseRuntimeUpstream() error = %v", err)
	}
	return newSessionProxy(base, newUISessionStore(time.Now))
}

func createTestSession(t *testing.T, store *uiSessionStore, session uiSession) uiSession {
	t.Helper()
	sess, err := store.createSession(context.Background(), session)
	if err != nil {
		t.Fatalf("createSession() error = %v", err)
	}
	return sess
}
