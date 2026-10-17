package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// An allowlisted write must reach runtime-api as the same method on the same
// path the allowlist approved. Percent-encoded '?' and '#' decode into the
// request path, so they must not be re-parsed as a query or fragment that
// truncates the path and turns "remove a member" into "delete the team".
func TestSessionProxyEncodedDelimitersCannotRetargetAllowlistedWrite(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, wantPath string
	}{
		{"question mark", http.MethodDelete, "/api/ui/v1/runtime/teams/acme%3F/members/u1", "/api/v1/runtime/teams/acme?/members/u1"},
		{"hash", http.MethodDelete, "/api/ui/v1/runtime/teams/acme%23/members/u1", "/api/v1/runtime/teams/acme#/members/u1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var gotMethod, gotPath string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				gotMethod, gotPath = r.Method, r.URL.Path
				mu.Unlock()
				w.WriteHeader(http.StatusNoContent)
			}))
			t.Cleanup(upstream.Close)
			proxy := newTestSessionProxy(t, upstream.URL)
			sess := createTestSession(t, proxy.store, uiSession{UpstreamAPIKey: "k"})
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.Header.Set(CSRFHeaderName, sess.CSRFToken)
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
			proxy.ServeHTTP(httptest.NewRecorder(), req)
			mu.Lock()
			defer mu.Unlock()
			if gotMethod != tc.method || gotPath != tc.wantPath {
				t.Fatalf("upstream saw %s %q, want %s %q", gotMethod, gotPath, tc.method, tc.wantPath)
			}
			if strings.Count(gotPath, "/") < 5 {
				t.Fatalf("path was truncated: %q", gotPath)
			}
		})
	}
}
