package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func newTestSAAuth(t *testing.T, username string, authenticated bool, audiences []string) (*serviceAccountAuthenticator, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer ingest-own" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		var req tokenReviewRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if len(req.Spec.Audiences) != 1 || req.Spec.Audiences[0] != "mcp-ingest" {
			t.Errorf("audiences = %v", req.Spec.Audiences)
		}
		var resp tokenReviewResponse
		resp.Status.Authenticated = authenticated
		resp.Status.Audiences = audiences
		resp.Status.User.Username = username
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(api.Close)
	return &serviceAccountAuthenticator{
		apiURL:         api.URL,
		audience:       "mcp-ingest",
		serviceAccount: "mcp-workload",
		client:         api.Client(),
		readToken:      func() (string, error) { return "ingest-own", nil },
		now:            time.Now,
		cache:          map[string]time.Time{},
	}, &calls
}

func TestServiceAccountAuthenticate(t *testing.T) {
	t.Parallel()
	ok := []string{"mcp-ingest"}
	tests := []struct {
		name          string
		username      string
		authenticated bool
		audiences     []string
		want          bool
	}{
		{"workload SA in any namespace", "system:serviceaccount:mcp-servers:mcp-workload", true, ok, true},
		{"workload SA in team namespace", "system:serviceaccount:mcp-team-ait:mcp-workload", true, ok, true},
		{"other SA name", "system:serviceaccount:mcp-servers:default", true, ok, false},
		{"not authenticated", "system:serviceaccount:mcp-servers:mcp-workload", false, ok, false},
		{"wrong audience", "system:serviceaccount:mcp-servers:mcp-workload", true, []string{"api"}, false},
		{"human user", "alice", true, ok, false},
		{"malformed", "system:serviceaccount:mcp-workload", true, ok, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, _ := newTestSAAuth(t, tc.username, tc.authenticated, tc.audiences)
			if got := a.Authenticate(context.Background(), "tok"); got != tc.want {
				t.Fatalf("Authenticate = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestServiceAccountAuthenticateCachesSuccess(t *testing.T) {
	t.Parallel()
	a, calls := newTestSAAuth(t, "system:serviceaccount:mcp-servers:mcp-workload", true, []string{"mcp-ingest"})
	for i := 0; i < 3; i++ {
		if !a.Authenticate(context.Background(), "tok") {
			t.Fatal("expected authenticated")
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("tokenreview calls = %d, want 1", calls.Load())
	}
	if a.Authenticate(context.Background(), "") {
		t.Fatal("empty token must not authenticate")
	}
}

func TestAuthAcceptsServiceAccountToken(t *testing.T) {
	t.Parallel()
	a, _ := newTestSAAuth(t, "system:serviceaccount:mcp-servers:mcp-workload", true, []string{"mcp-ingest"})
	s := &ingestServer{saAuth: a, apiKeys: map[string]struct{}{"k": {}}}
	h := s.auth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))

	for _, tc := range []struct {
		name   string
		header map[string]string
		want   int
	}{
		{"bearer SA token", map[string]string{"Authorization": "Bearer tok"}, http.StatusNoContent},
		{"api key still works", map[string]string{"x-api-key": "k"}, http.StatusNoContent},
		{"no credentials", nil, http.StatusUnauthorized},
	} {
		req := httptest.NewRequest(http.MethodPost, "/events", nil)
		for k, v := range tc.header {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("%s: status = %d, want %d", tc.name, rec.Code, tc.want)
		}
	}
}
