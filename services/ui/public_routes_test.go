package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublicRoutesPrefixedDashboard(t *testing.T) {
	t.Setenv("UI_PATH_PREFIX", "/platform")
	t.Setenv("UI_DOCS_PATH", "/help")
	t.Setenv("UI_DOCS_URL", "https://docs.example.com/")
	t.Setenv("UI_REGISTRY_PATH", "/images")
	t.Setenv("UI_GRAFANA_PATH", "/monitoring")
	mux, err := newMux("/api/v1", "http://127.0.0.1:1", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	handler := securityHeadersMiddleware(mux)
	request := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		handler.ServeHTTP(r, httptest.NewRequest(http.MethodGet, path, nil))
		return r
	}
	page := request("/platform/")
	if page.Code != 200 || !strings.Contains(page.Body.String(), `<base href="/platform/">`) {
		t.Fatalf("page: %d %s", page.Code, page.Body.String())
	}
	if strings.Contains(page.Body.String(), `src="/assets/`) || strings.Contains(page.Body.String(), `href="/favicon.png"`) {
		t.Fatal("HTML escaped the configured prefix")
	}
	for _, path := range []string{"/platform/favicon.png", "/platform/brand/mcp-runtime-logo.png", "/health", "/platform/auth/status"} {
		if r := request(path); r.Code != 200 {
			t.Fatalf("%s: %d", path, r.Code)
		}
	}
	if r := request("/platform/auth/status"); !strings.Contains(r.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("prefixed session response can be cached")
	}
	config := request("/platform/config.js")
	for _, expected := range []string{`"prefix":"/platform"`, `"docs":"/help"`, `"grafana":"/monitoring"`, `"registry":"/images"`} {
		if !strings.Contains(config.Body.String(), expected) {
			t.Errorf("config missing %s", expected)
		}
	}
	for path, target := range map[string]string{"/platform": "/platform/", "/help": "https://docs.example.com/", "/images": "/platform/#/servers"} {
		r := request(path)
		if r.Code < 300 || r.Code >= 400 || r.Header().Get("Location") != target {
			t.Errorf("%s: %d %q", path, r.Code, r.Header().Get("Location"))
		}
	}
	for _, path := range []string{"/", "/auth/status", "/platform-other/", "/help/unknown"} {
		if r := request(path); r.Code != 404 {
			t.Errorf("%s should not be served: %d", path, r.Code)
		}
	}
	posted := httptest.NewRecorder()
	handler.ServeHTTP(posted, httptest.NewRequest(http.MethodPost, "/help", strings.NewReader("private-body")))
	if posted.Code != http.StatusMethodNotAllowed || posted.Header().Get("Location") != "" {
		t.Fatal("docs route forwarded a POST body to its external destination")
	}
}

func TestPublicRoutesRejectInvalidStartup(t *testing.T) {
	t.Setenv("UI_PATH_PREFIX", "/api")
	if _, err := newMux("/api/v1", "http://127.0.0.1:1", "", "", ""); err == nil {
		t.Fatal("reserved prefix accepted")
	}
}

func TestPrefixedLoginAndSessionProxyPreserveCSRF(t *testing.T) {
	t.Setenv("UI_PATH_PREFIX", "/console")
	mux, err := newMux("/api/v1", "http://127.0.0.1:1", "test-key", "test-key", "test-key")
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRecorder()
	mux.ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/console/auth/login", strings.NewReader(`{"api_key":"test-key"}`)))
	if login.Code != http.StatusOK {
		t.Fatalf("login: %d %s", login.Code, login.Body.String())
	}
	var body struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &body); err != nil || body.CSRFToken == "" {
		t.Fatalf("login token: %v", err)
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly {
		t.Fatal("login lost its HttpOnly session cookie")
	}
	logout := func(token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/console/auth/logout", nil)
		r.AddCookie(cookies[0])
		if token != "" {
			r.Header.Set("X-CSRF-Token", token)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	mutation := httptest.NewRequest(http.MethodPost, "/console/api/ui/v1/runtime/teams", nil)
	mutation.AddCookie(cookies[0])
	denied := httptest.NewRecorder()
	mux.ServeHTTP(denied, mutation)
	if denied.Code != http.StatusForbidden || !strings.Contains(denied.Body.String(), "csrf_failed") {
		t.Fatalf("mutation without CSRF: %d %s", denied.Code, denied.Body.String())
	}
	if w := logout(body.CSRFToken); w.Code != http.StatusOK {
		t.Fatalf("logout with CSRF: %d %s", w.Code, w.Body.String())
	}
}
