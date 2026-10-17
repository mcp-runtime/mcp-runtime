package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
		{"other SA name", "system:serviceaccount:mcp-servers:default", true, ok, false},
		{"not authenticated", "system:serviceaccount:mcp-servers:mcp-workload", false, ok, false},
		{"wrong audience", "system:serviceaccount:mcp-servers:mcp-workload", true, []string{"api"}, false},
		{"human user", "alice", true, ok, false},
		{"malformed", "system:serviceaccount:mcp-workload", true, ok, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fc := newFakeCluster(t)
			fc.username, fc.authenticated, fc.audiences = tc.username, tc.authenticated, tc.audiences
			a := fc.authenticator()
			if _, got := a.Authenticate(context.Background(), "tok"); got != tc.want {
				t.Fatalf("Authenticate = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestServiceAccountAuthenticateCachesSuccess(t *testing.T) {
	t.Parallel()
	fc := newFakeCluster(t)
	a, calls := fc.authenticator(), &fc.reviewCalls
	for i := 0; i < 3; i++ {
		if _, ok := a.Authenticate(context.Background(), "tok"); !ok {
			t.Fatal("expected authenticated")
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("tokenreview calls = %d, want 1", calls.Load())
	}
	if _, ok := a.Authenticate(context.Background(), ""); ok {
		t.Fatal("empty token must not authenticate")
	}
}

func TestAuthAcceptsServiceAccountToken(t *testing.T) {
	t.Parallel()
	a := newFakeCluster(t).authenticator()
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
