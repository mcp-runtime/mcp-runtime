package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestGrafanaPlatformSessionAndOriginBoundary(t *testing.T) {
	t.Setenv("UI_PUBLIC_ORIGIN", "https://platform.example.com")
	requests := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if !strings.HasPrefix(r.URL.Path, "/api/v1/runtime/observability/grafana/") || r.Header.Get("Authorization") != "Bearer platform-session" {
			t.Errorf("incorrect session proxy request: %s %v", r.URL, r.Header)
		}
		for _, header := range []string{"Cookie", "X-WEBAUTH-USER", "X-WEBAUTH-ROLE", "X-Grafana-Org-Id", "X-Forwarded-Host", "X-API-Key"} {
			if r.Header.Get(header) != "" {
				t.Errorf("spoofed header forwarded: %s", header)
			}
		}
		w.Header().Set("Set-Cookie", "grafana_session=foreign")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"results":{}}`)
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	store := newUISessionStore(time.Now)
	sess := createTestSession(t, store, uiSession{UpstreamAuthHeader: "Bearer platform-session"})
	proxy := grafanaSessionProxy(base, store, "/monitoring")
	for _, tc := range []struct {
		name, method, origin string
		cookie               bool
		status               int
	}{
		{"session-read", "GET", "", true, 200},
		{"signed-out", "GET", "", false, 401},
		{"same-origin-query", "POST", "https://platform.example.com", true, 200},
		{"missing-origin", "POST", "", true, 403},
		{"null-origin", "POST", "null", true, 403},
		{"alternate-origin", "POST", "https://evil.example.com", true, 403},
		{"same-site-subdomain", "POST", "https://untrusted.platform.example.com", true, 403},
		{"downgrade-origin", "POST", "http://platform.example.com", true, 403},
		{"userinfo", "POST", "https://attacker@platform.example.com", true, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := requests
			r := httptest.NewRequest(tc.method, "http://evil.example.com/monitoring/api/ds/query", strings.NewReader(`{}`))
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("X-Forwarded-Host", "evil.example.com")
			r.Header.Set("Authorization", "Bearer attacker")
			r.Header.Set("X-API-Key", "attacker")
			r.Header.Set("X-WEBAUTH-USER", "admin")
			r.Header.Set("X-WEBAUTH-ROLE", "Admin")
			r.Header.Set("X-Grafana-Org-Id", "2")
			if tc.cookie {
				r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
			}
			r.AddCookie(&http.Cookie{Name: "grafana_session", Value: "admin"})
			w := httptest.NewRecorder()
			proxy.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.status, w.Body.String())
			}
			if tc.status != 200 && requests != before {
				t.Fatal("unauthenticated or cross-origin request reached runtime")
			}
			if w.Header().Get("Set-Cookie") != "" {
				t.Fatal("independent Grafana login cookie escaped proxy")
			}
		})
	}
	store.delete(sess.ID)
	r := httptest.NewRequest("GET", "/monitoring/", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("logged-out platform session retained Grafana access: %d", w.Code)
	}
}
