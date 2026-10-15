package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"mcp-runtime/pkg/openapi"
	"mcp-runtime/pkg/sessionstate"
)

func TestOpenAPISpecLoads(t *testing.T) {
	if _, err := openapi.Load(openAPISpec); err != nil {
		t.Fatalf("Load(openAPISpec) error = %v", err)
	}
}

func TestSessionAudienceOpenAPIHasListenerOwnership(t *testing.T) {
	doc, err := openapi.Load(openAPISpec)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ path, audience string }{{sessionstate.GatewayPath, "gateway"}, {sessionstate.GatewayPath + "/changes", "gateway"}, {sessionstate.GatewayPath + "/{namespace}/{name}", "gateway"}, {sessionstate.AccessPath, "runtime-api"}, {sessionstate.AccessPath + "/{namespace}/{name}", "runtime-api"}, {sessionstate.AccessPath + "/revoke", "runtime-api"}} {
		path := doc.Paths.Find(tt.path)
		if path == nil {
			t.Fatalf("missing session route %s", tt.path)
		}
		if path.Extensions["x-audience"] != tt.audience {
			t.Fatalf("path %s audience=%v", tt.path, path.Extensions["x-audience"])
		}
		if path.Extensions["x-listener-port"] == nil {
			t.Fatalf("path %s listener not documented", tt.path)
		}
	}
}

func TestSessionUnauthorizedResponseMatchesOpenAPI(t *testing.T) {
	doc, err := openapi.Load(openAPISpec)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	(&apiServer{}).registerRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, sessionstate.AccessPath, nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", rec.Code)
	}
	if err := openapi.ValidateResponse(doc, http.MethodGet, sessionstate.AccessPath, rec.Code, rec.Body.Bytes(), "application/json"); err != nil {
		t.Fatal(err)
	}
}

func TestHealthResponseMatchesOpenAPISpec(t *testing.T) {
	doc, err := openapi.Load(openAPISpec)
	if err != nil {
		t.Fatalf("Load(openAPISpec) error = %v", err)
	}

	mux := http.NewServeMux()
	(&apiServer{}).registerRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/health status = %d, want %d", rec.Code, http.StatusOK)
	}
	if err := openapi.ValidateResponse(doc, http.MethodGet, "/health", rec.Code, rec.Body.Bytes(), "application/json"); err != nil {
		t.Fatalf("ValidateResponse(/health) error = %v", err)
	}
}

func TestAuthMeUnauthorizedMatchesOpenAPISpec(t *testing.T) {
	doc, err := openapi.Load(openAPISpec)
	if err != nil {
		t.Fatalf("Load(openAPISpec) error = %v", err)
	}

	mux := http.NewServeMux()
	(&apiServer{}).registerRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("/api/v1/auth/me status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if err := openapi.ValidateResponse(doc, http.MethodGet, "/api/v1/auth/me", rec.Code, rec.Body.Bytes(), "application/json"); err != nil {
		t.Fatalf("ValidateResponse(/api/v1/auth/me) error = %v", err)
	}
}

func TestOpenAPIEndpointServesEmbeddedSpec(t *testing.T) {
	mux := http.NewServeMux()
	(&apiServer{}).registerRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/openapi.yaml", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/v1/openapi.yaml status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/yaml" {
		t.Fatalf("Content-Type = %q, want application/yaml", got)
	}
	if len(rec.Body.Bytes()) == 0 {
		t.Fatal("expected non-empty OpenAPI document body")
	}
}
