package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSessionProxyUserWriteAllowlist(t *testing.T) {
	cases := []struct {
		method string
		path   string
		want   bool
	}{
		{method: http.MethodPost, path: "/user/api-keys", want: true},
		{method: http.MethodDelete, path: "/user/api-keys/uk_abc", want: true},
		// Wrong method on an allowlisted path.
		{method: http.MethodDelete, path: "/user/api-keys", want: false},
		{method: http.MethodPost, path: "/user/api-keys/uk_abc", want: false},
		{method: http.MethodPut, path: "/user/api-keys", want: false},
		{method: http.MethodPatch, path: "/user/api-keys", want: false},
		// Read-allowlisted paths are not writable, except the one explicit
		// exception: retiring a server (DELETE only - never create/update).
		{method: http.MethodPost, path: "/runtime/servers", want: false},
		{method: http.MethodDelete, path: "/runtime/servers/ns/name", want: true},
		{method: http.MethodPatch, path: "/runtime/servers/ns/name", want: false},
		{method: http.MethodDelete, path: "/runtime/servers/ns/name/extra", want: false},
		{method: http.MethodPost, path: "/admin/operations", want: false},
		// Nested traversal beyond one segment is denied.
		{method: http.MethodDelete, path: "/user/api-keys/uk_abc/extra", want: false},
		{method: http.MethodDelete, path: "/user/api-keys/", want: false},
	}
	for _, tc := range cases {
		if got := sessionProxyWriteAllowed(tc.method, tc.path); got != tc.want {
			t.Fatalf("sessionProxyWriteAllowed(%s, %q) = %v, want %v", tc.method, tc.path, got, tc.want)
		}
	}
}

func TestSessionProxyWriteRequiresCSRFToken(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("upstream must not be called without a valid CSRF token")
	}))
	t.Cleanup(upstream.Close)
	proxy := newTestSessionProxy(t, upstream.URL)
	sess := createTestSession(t, proxy.store, uiSession{UpstreamAuthHeader: "Bearer session-token"})

	for _, token := range []string{"", "wrong-token", sess.CSRFToken + "x"} {
		req := httptest.NewRequest(http.MethodPost, "/api/ui/v1/user/api-keys", strings.NewReader(`{"name":"k"}`))
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
		if token != "" {
			req.Header.Set(CSRFHeaderName, token)
		}
		rec := httptest.NewRecorder()
		proxy.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("token %q: status = %d, want %d", token, rec.Code, http.StatusForbidden)
		}
		if strings.Contains(rec.Body.String(), sess.CSRFToken) {
			t.Fatal("rejection body leaked the session CSRF token")
		}
	}
}

func TestSessionProxyWriteWithValidCSRFReachesUpstream(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotBody, gotCSRF, gotContentType string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("authorization")
		gotCSRF = r.Header.Get(CSRFHeaderName)
		gotContentType = r.Header.Get("content-type")
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"key":{"id":"uk_1"},"api_key":"mcpu_secret"}`))
	}))
	t.Cleanup(upstream.Close)
	proxy := newTestSessionProxy(t, upstream.URL)
	sess := createTestSession(t, proxy.store, uiSession{UpstreamAuthHeader: "Bearer session-token"})

	req := httptest.NewRequest(http.MethodPost, "/api/ui/v1/user/api-keys", strings.NewReader(`{"name":"laptop"}`))
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	req.Header.Set(CSRFHeaderName, sess.CSRFToken)
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/user/api-keys" {
		t.Fatalf("upstream got %s %s", gotMethod, gotPath)
	}
	if gotBody != `{"name":"laptop"}` {
		t.Fatalf("upstream body = %q", gotBody)
	}
	if gotAuth != "Bearer session-token" {
		t.Fatalf("upstream authorization = %q", gotAuth)
	}
	if gotContentType != "application/json" {
		t.Fatalf("upstream content-type = %q", gotContentType)
	}
	if gotCSRF != "" {
		t.Fatalf("CSRF token must not be forwarded upstream, got %q", gotCSRF)
	}
	if !strings.Contains(rec.Body.String(), "mcpu_secret") {
		t.Fatalf("one-time key body not propagated: %s", rec.Body.String())
	}
}

func TestSessionProxyDeleteWithValidCSRF(t *testing.T) {
	var gotMethod, gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)
	proxy := newTestSessionProxy(t, upstream.URL)
	sess := createTestSession(t, proxy.store, uiSession{UpstreamAuthHeader: "Bearer session-token"})

	req := httptest.NewRequest(http.MethodDelete, "/api/ui/v1/user/api-keys/uk_abc", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	req.Header.Set(CSRFHeaderName, sess.CSRFToken)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if gotMethod != http.MethodDelete || gotPath != "/api/v1/user/api-keys/uk_abc" {
		t.Fatalf("upstream got %s %s", gotMethod, gotPath)
	}
}

func TestSessionProxyWriteRejectsCrossOrigin(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("upstream must not be called for a cross-origin write")
	}))
	t.Cleanup(upstream.Close)
	proxy := newTestSessionProxy(t, upstream.URL)
	sess := createTestSession(t, proxy.store, uiSession{UpstreamAuthHeader: "Bearer session-token"})

	req := httptest.NewRequest(http.MethodPost, "/api/ui/v1/user/api-keys", strings.NewReader(`{"name":"k"}`))
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	// Even with the correct token, a foreign Origin is refused.
	req.Header.Set(CSRFHeaderName, sess.CSRFToken)
	req.Header.Set("origin", "https://evil.example")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestSessionProxyWriteUnauthenticatedIs401NotCSRF(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("upstream must not be called")
	}))
	t.Cleanup(upstream.Close)
	proxy := newTestSessionProxy(t, upstream.URL)

	req := httptest.NewRequest(http.MethodPost, "/api/ui/v1/user/api-keys", strings.NewReader(`{"name":"k"}`))
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	// A signed-out write must tell the client to sign in, not report forbidden.
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestSessionProxyWriteRejectsNonAllowlistedPath(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("upstream must not be called")
	}))
	t.Cleanup(upstream.Close)
	proxy := newTestSessionProxy(t, upstream.URL)
	sess := createTestSession(t, proxy.store, uiSession{UpstreamAuthHeader: "Bearer session-token"})

	// A read-allowlisted path with a valid token still cannot be written.
	req := httptest.NewRequest(http.MethodPost, "/api/ui/v1/admin/operations", strings.NewReader(`{}`))
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	req.Header.Set(CSRFHeaderName, sess.CSRFToken)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestSessionProxyGetStillNeedsNoCSRFToken(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"servers":[]}`))
	}))
	t.Cleanup(upstream.Close)
	proxy := newTestSessionProxy(t, upstream.URL)
	sess := createTestSession(t, proxy.store, uiSession{UpstreamAuthHeader: "Bearer session-token"})

	req := httptest.NewRequest(http.MethodGet, "/api/ui/v1/runtime/servers", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("reads must not require a CSRF token; status = %d", rec.Code)
	}
}

func TestUnsafeHTTPMethodFailsClosed(t *testing.T) {
	safe := []string{http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace}
	for _, m := range safe {
		if unsafeHTTPMethod(m) {
			t.Fatalf("%s should be treated as safe", m)
		}
	}
	unsafe := []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, "CONNECT", "WEIRD"}
	for _, m := range unsafe {
		if !unsafeHTTPMethod(m) {
			t.Fatalf("%s should be treated as unsafe", m)
		}
	}
}

func TestCSRFTokenValidRejectsEmptySessionToken(t *testing.T) {
	// A session minted before CSRF existed must not be usable for writes.
	if csrfTokenValid(uiSession{}, "anything") {
		t.Fatal("empty session token must never validate")
	}
	if csrfTokenValid(uiSession{CSRFToken: "abc"}, "") {
		t.Fatal("empty presented token must never validate")
	}
	if !csrfTokenValid(uiSession{CSRFToken: "abc"}, "abc") {
		t.Fatal("matching tokens should validate")
	}
}

func TestSameOriginRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "http://dash.example/api/ui/v1/user/api-keys", nil)
	req.Host = "dash.example"

	// Absent Origin is allowed; the token check decides.
	if !sameOriginRequest(req) {
		t.Fatal("missing origin should not fail the origin check")
	}

	req.Header.Set("origin", "http://dash.example")
	if !sameOriginRequest(req) {
		t.Fatal("matching origin should pass")
	}

	req.Header.Set("origin", "http://evil.example")
	if sameOriginRequest(req) {
		t.Fatal("foreign origin must fail")
	}

	req.Header.Set("origin", "not a url")
	if sameOriginRequest(req) {
		t.Fatal("unparseable origin must fail closed")
	}
}

func TestCreateSessionMintsUniqueCSRFTokens(t *testing.T) {
	store := newUISessionStore(time.Now)
	a := createTestSession(t, store, uiSession{UpstreamAuthHeader: "Bearer a"})
	b := createTestSession(t, store, uiSession{UpstreamAuthHeader: "Bearer b"})

	if a.CSRFToken == "" || b.CSRFToken == "" {
		t.Fatal("sessions must be minted with a CSRF token")
	}
	if a.CSRFToken == b.CSRFToken {
		t.Fatal("CSRF tokens must be unique per session")
	}
	if a.CSRFToken == a.ID {
		t.Fatal("CSRF token must not equal the session id")
	}
}
