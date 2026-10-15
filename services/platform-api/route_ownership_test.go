package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRouteOwnershipV1Only(t *testing.T) {
	mux := http.NewServeMux()
	(&apiServer{}).registerRoutes(mux)

	for _, path := range []string{
		"/api/auth/login",
		"/api/auth/me",
		"/api/registry/authz",
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("legacy path %s status = %d, want %d", path, rec.Code, http.StatusNotFound)
		}
	}

	for _, path := range []string{
		"/api/v1/runtime/servers",
		"/api/v1/stats",
		"/api/v1/deployments",
		"/internal/gateway/v1/sessions",
		"/internal/gateway/v1/sessions/changes",
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("foreign path %s status = %d, want %d", path, rec.Code, http.StatusNotFound)
		}
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil))
	if rec.Code == http.StatusNotFound {
		t.Fatalf("owned path /api/v1/auth/me should be registered")
	}
}
