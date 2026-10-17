package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	uiproxy "mcp-ui/internal/proxy"
)

const csrfHeaderName = uiproxy.CSRFHeaderName

func parseRuntimeUpstream(raw string) (*url.URL, error) { return uiproxy.ParseUpstream(raw) }

func newSessionProxyWithUpstreams(platform, runtime, analytics *url.URL, store *uiSessionStore) *uiproxy.SessionProxy {
	return uiproxy.NewSessionProxy(platform, runtime, analytics, store, sessionProxyHTTPClient)
}
func grafanaSessionProxy(platform, runtime *url.URL, store *uiSessionStore, prefix string) http.Handler {
	return uiproxy.NewGrafanaProxy(platform, runtime, store, prefix, sessionProxyHTTPClient)
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

	mux, err := newMux("/api/v1", testExchangeBase(t).String(), "secret", "api-secret", "")
	if err != nil {
		t.Fatalf("newMux() error = %v", err)
	}
	sess := createTestSession(t, sessions, uiSession{UpstreamAuthHeader: "Bearer mux-token"})
	t.Cleanup(func() { sessions.Delete(sess.ID) })

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

func createTestSession(t *testing.T, store *uiSessionStore, session uiSession) uiSession {
	t.Helper()
	sess, err := store.Create(context.Background(), session)
	if err != nil {
		t.Fatalf("createSession() error = %v", err)
	}
	return sess
}

// Existing proxy-routing tests use opaque fixture credentials. Exchange security
// is tested separately with signed JWTs and the real exchange handler.
func testExchangeBase(t *testing.T) *url.URL {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Authorization", r.Header.Get("Authorization"))
	}))
	t.Cleanup(server.Close)
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestGrafanaUsesItsNoncePolicyWithoutWeakeningConsolePolicy(t *testing.T) {
	for _, policy := range []string{"script-src 'nonce-grafana-response' 'strict-dynamic'; object-src 'none'", ""} {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if policy != "" {
				w.Header().Set("Content-Security-Policy", policy)
			}
			fmt.Fprint(w, `<html><script nonce="grafana-response">window.fixture=true</script></html>`)
		}))
		base, _ := url.Parse(upstream.URL)
		store := newUISessionStore(time.Now)
		sess := createTestSession(t, store, uiSession{UpstreamAuthHeader: "Bearer platform-session"})
		proxy := securityHeadersMiddleware(grafanaSessionProxy(testExchangeBase(t), base, store, "/grafana"))
		r := httptest.NewRequest("GET", "/grafana/", nil)
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
		w := httptest.NewRecorder()
		proxy.ServeHTTP(w, r)
		upstream.Close()
		if policy == "" {
			if w.Code != 502 || strings.Contains(w.Body.String(), "window.fixture") {
				t.Fatalf("HTML without upstream CSP escaped: %d %s", w.Code, w.Body.String())
			}
		} else if w.Code != 200 || len(w.Header().Values("Content-Security-Policy")) != 1 || w.Header().Get("Content-Security-Policy") != policy || w.Header().Get("X-Frame-Options") != "DENY" {
			t.Fatalf("Grafana received cumulative incompatible CSPs: %d %v", w.Code, w.Header().Values("Content-Security-Policy"))
		}
	}
}
