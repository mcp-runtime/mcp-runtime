package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSessionProxyOversizedWriteBodyIsRejectedAs413(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = r.Body.Read(make([]byte, 1))
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)
	proxy := newTestSessionProxy(t, upstream.URL)
	sess := createTestSession(t, proxy.store, uiSession{UpstreamAPIKey: "k"})
	req := httptest.NewRequest(http.MethodPost, "/api/ui/v1/user/api-keys", strings.NewReader(strings.Repeat("a", sessionProxyMaxWriteBody+1)))
	req.Header.Set(CSRFHeaderName, sess.CSRFToken)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body=%s", rec.Code, rec.Body.String())
	}
}

// Credential-bearing proxied responses (for example a freshly minted API key)
// must never be cacheable, even when the upstream says otherwise.
func TestSessionProxyResponsesAreNeverCacheable(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=600")
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(`{"key":"secret"}`))
	}))
	t.Cleanup(upstream.Close)
	proxy := newTestSessionProxy(t, upstream.URL)
	sess := createTestSession(t, proxy.store, uiSession{UpstreamAPIKey: "k"})
	req := httptest.NewRequest(http.MethodGet, "/api/ui/v1/user/api-keys", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)
	if got := rec.Header().Values("Cache-Control"); len(got) != 1 || got[0] != "no-store" {
		t.Fatalf("Cache-Control = %q, want exactly [no-store]", got)
	}
}
