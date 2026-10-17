package main

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mcp-runtime/pkg/platformauth"
)

type exchangeUIPrincipal struct {
	inactive atomic.Bool
	calls    atomic.Int32
}

func (p *exchangeUIPrincipal) PrincipalForUserID(context.Context, string) (platformauth.Principal, error) {
	p.calls.Add(1)
	if p.inactive.Load() {
		return platformauth.Principal{}, sql.ErrNoRows
	}
	return platformauth.Principal{Subject: "ui-user", Role: platformauth.RoleUser}, nil
}

func TestUISessionProxyUsesRealAudienceExchangeAndCSRF(t *testing.T) {
	t.Setenv("UI_PUBLIC_ORIGIN", "http://ui.example")
	secret := []byte("ui-exchange-secret")
	resolver := &exchangeUIPrincipal{}
	platformAuth := platformauth.Authenticator{Secret: secret, Audience: platformauth.AudiencePlatform, Principals: resolver}
	mux := http.NewServeMux()
	for _, target := range []string{platformauth.AudienceRuntime, platformauth.AudienceAnalytics} {
		mux.Handle(platformauth.ExchangePath(target), platformauth.ExchangeHandler{Auth: platformAuth, Target: target})
	}
	platform := httptest.NewServer(mux)
	defer platform.Close()
	var capturedRuntime atomic.Value
	var calls atomic.Int32
	backend := func(target string) *httptest.Server {
		auth := platformauth.Authenticator{Secret: secret, Audience: target, Principals: resolver}
		server := httptest.NewServer(auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			claims, err := platformauth.Verify(secret, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), target)
			if err != nil || len(claims.Audience) != 1 || claims.ExpiresAt.Sub(claims.IssuedAt.Time) > time.Minute {
				t.Error("invalid upstream audience or expiry")
			}
			if target == platformauth.AudienceRuntime {
				capturedRuntime.Store(r.Header.Get("Authorization"))
			}
			if r.Header.Get("Cookie") != "" || r.Header.Get(csrfHeaderName) != "" || r.Header.Get("X-Api-Key") != "" {
				t.Error("browser credentials leaked to backend")
			}
			// The response filter must prevent accidental upstream token exposure.
			w.Header().Set("Authorization", r.Header.Get("Authorization"))
			w.Write([]byte(`{"ok":true}`))
		})))
		t.Cleanup(server.Close)
		return server
	}
	runtime := backend(platformauth.AudienceRuntime)
	analytics := backend(platformauth.AudienceAnalytics)
	platformBase, _ := url.Parse(platform.URL)
	runtimeBase, _ := url.Parse(runtime.URL)
	analyticsBase, _ := url.Parse(analytics.URL)
	store := newUISessionStore(time.Now)
	proxy := newSessionProxyWithUpstreams(platformBase, runtimeBase, analyticsBase, store)
	source, err := platformauth.Sign(secret, platformauth.Principal{Subject: "ui-user", Role: platformauth.RoleAdmin}, 15*time.Minute, []string{platformauth.AudiencePlatform})
	if err != nil {
		t.Fatal(err)
	}
	session := createTestSession(t, store, uiSession{UpstreamAuthHeader: "Bearer " + source})
	request := func(method, path string, csrf bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://ui.example"+path, strings.NewReader(`{}`))
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.ID})
		req.Header.Set("Authorization", "Bearer attacker")
		req.Header.Set("X-Api-Key", "attacker")
		req.Header.Set("X-Forwarded-Uri", "/api/v1/auth/me?audience=platform-api")
		if csrf {
			req.Header.Set("Origin", "http://ui.example")
			req.Header.Set(csrfHeaderName, session.CSRFToken)
		}
		rec := httptest.NewRecorder()
		proxy.ServeHTTP(rec, req)
		return rec
	}
	for _, path := range []string{"/api/ui/v1/runtime/tools", "/api/ui/v1/events"} {
		rec := request(http.MethodGet, path, false)
		if rec.Code != 200 || rec.Header().Get("Authorization") != "" || strings.Contains(rec.Body.String(), source) {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
	grafana := grafanaSessionProxy(platformBase, runtimeBase, store, "/grafana")
	grafanaRequest := func(method string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://ui.example/grafana/api/health", nil)
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.ID})
		req.Header.Set("Authorization", "Bearer attacker")
		rec := httptest.NewRecorder()
		grafana.ServeHTTP(rec, req)
		return rec
	}
	if rec := grafanaRequest(http.MethodGet); rec.Code != 200 || rec.Header().Get("Authorization") != "" {
		t.Fatalf("Grafana runtime exchange status=%d", rec.Code)
	}
	beforeGrafana := resolver.calls.Load()
	if rec := grafanaRequest(http.MethodPost); rec.Code != 403 || resolver.calls.Load() != beforeGrafana {
		t.Fatal("Grafana Origin check must precede exchange")
	}
	before := resolver.calls.Load()
	if rec := request(http.MethodPost, "/api/ui/v1/runtime/teams", false); rec.Code != 403 || resolver.calls.Load() != before {
		t.Fatal("CSRF must be checked before exchange")
	}
	if rec := request(http.MethodPost, "/api/ui/v1/runtime/teams", true); rec.Code != 200 {
		t.Fatalf("CSRF-valid write status=%d", rec.Code)
	}
	// A source bearer cannot authenticate directly to either split backend.
	for _, target := range []string{platformauth.AudienceRuntime, platformauth.AudienceAnalytics} {
		auth := platformauth.Authenticator{Secret: secret, Audience: target}
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+source)
		if _, ok, _ := auth.AuthenticateRequest(req); ok {
			t.Fatalf("platform bearer accepted directly by %s", target)
		}
	}
	for _, target := range []string{platformauth.AudiencePlatform, platformauth.AudienceAnalytics} {
		auth := platformauth.Authenticator{Secret: secret, Audience: target}
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", capturedRuntime.Load().(string))
		if _, ok, _ := auth.AuthenticateRequest(req); ok {
			t.Fatalf("runtime bearer replay accepted by %s", target)
		}
	}
	beforeCalls := calls.Load()
	resolver.inactive.Store(true)
	if rec := request(http.MethodGet, "/api/ui/v1/runtime/tools", false); rec.Code != 401 || calls.Load() != beforeCalls {
		t.Fatalf("deactivated UI session reached backend: status=%d", rec.Code)
	}
	if rec := grafanaRequest(http.MethodGet); rec.Code != 401 || calls.Load() != beforeCalls {
		t.Fatal("deactivated Grafana session reached backend")
	}
}
