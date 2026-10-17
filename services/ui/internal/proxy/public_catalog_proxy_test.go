package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestPublicCatalogProxyForwardsAnonymousReads(t *testing.T) {
	var gotPath, gotAuth, gotAPIKey string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("authorization")
		gotAPIKey = r.Header.Get("x-api-key")
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, `{"servers":[]}`)
	}))
	t.Cleanup(upstream.Close)

	base, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream: %v", err)
	}
	proxy := newPublicCatalogProxy(base, true)

	req := httptest.NewRequest(http.MethodGet, "/api/public/v1/servers", nil)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if gotPath != "/api/v1/runtime/servers" {
		t.Fatalf("upstream path = %q", gotPath)
	}
	if gotAuth != "" || gotAPIKey != "" {
		t.Fatalf("proxy forwarded a credential: authorization=%q x-api-key=%q", gotAuth, gotAPIKey)
	}
}

func TestPublicCatalogProxyRoutesTools(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, `{"tools":[]}`)
	}))
	t.Cleanup(upstream.Close)

	base, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream: %v", err)
	}
	proxy := newPublicCatalogProxy(base, true)

	req := httptest.NewRequest(http.MethodGet, "/api/public/v1/tools", nil)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if gotPath != "/api/v1/runtime/tools" {
		t.Fatalf("upstream path = %q", gotPath)
	}
}

func TestPublicCatalogProxyDisabledOutsidePublicMode(t *testing.T) {
	proxy := newPublicCatalogProxy(mustParseURL(t, "http://unused.invalid"), false)

	req := httptest.NewRequest(http.MethodGet, "/api/public/v1/servers", nil)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when platform mode is not public", rec.Code)
	}
}

func TestPublicCatalogProxyRejectsUnknownPathsAndNonGET(t *testing.T) {
	proxy := newPublicCatalogProxy(mustParseURL(t, "http://unused.invalid"), true)

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/public/v1/namespaces"},
		{http.MethodGet, "/api/public/v1/servers/mcp-servers/demo"},
		{http.MethodPost, "/api/public/v1/servers"},
		{http.MethodDelete, "/api/public/v1/servers"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rec := httptest.NewRecorder()
		proxy.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s: status = %d, want 404", tc.method, tc.path, rec.Code)
		}
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url %q: %v", raw, err)
	}
	return u
}
