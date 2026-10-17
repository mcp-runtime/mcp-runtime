package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mcp-runtime-api/internal/platforminternal"
	"mcp-runtime-api/internal/runtimeapi"
	"mcp-runtime/pkg/openapi"
	"mcp-runtime/pkg/platformauth"
)

func TestOpenAPISpecLoads(t *testing.T) {
	if _, err := openapi.Load(openAPISpec); err != nil {
		t.Fatalf("Load(openAPISpec) error = %v", err)
	}
}

func TestHealthResponseMatchesOpenAPISpec(t *testing.T) {
	doc, err := openapi.Load(openAPISpec)
	if err != nil {
		t.Fatalf("Load(openAPISpec) error = %v", err)
	}

	mux := http.NewServeMux()
	(&server{}).registerRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/health status = %d, want %d", rec.Code, http.StatusOK)
	}
	if err := openapi.ValidateResponse(doc, http.MethodGet, "/health", rec.Code, rec.Body.Bytes(), "application/json"); err != nil {
		t.Fatalf("ValidateResponse(/health) error = %v", err)
	}
}

func TestPublicProbesDoNotExposeInitializationErrors(t *testing.T) {
	const detail = "internal cluster path and credential diagnostic"
	for _, path := range []string{"/health", "/ready"} {
		mux := http.NewServeMux()
		(&server{runtimeInit: detail}).registerRoutes(mux)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status=%d", path, response.Code)
		}
		if body := response.Body.String(); strings.Contains(body, detail) || strings.Contains(body, "runtime_error") || strings.Contains(body, "runtime_initialized") {
			t.Fatalf("%s exposed initialization details: %s", path, body)
		}
	}
}

func TestInternalAuthResolveUnauthorizedMatchesOpenAPISpec(t *testing.T) {
	doc, err := openapi.Load(openAPISpec)
	if err != nil {
		t.Fatalf("Load(openAPISpec) error = %v", err)
	}

	mux := http.NewServeMux()
	platforminternal.Handler{Token: "internal-token"}.Register(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/internal/auth/resolve", bytes.NewBufferString(`{"api_key":"key"}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("/internal/auth/resolve status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if err := openapi.ValidateResponse(doc, http.MethodPost, "/internal/auth/resolve", rec.Code, rec.Body.Bytes(), "application/json"); err != nil {
		t.Fatalf("ValidateResponse(/internal/auth/resolve) error = %v", err)
	}
}

func TestRuntimeServersUnauthorizedMatchesOpenAPISpec(t *testing.T) {
	doc, err := openapi.Load(openAPISpec)
	if err != nil {
		t.Fatalf("Load(openAPISpec) error = %v", err)
	}

	mux := http.NewServeMux()
	(&server{
		runtime: &runtimeapi.RuntimeServer{},
		authentic: platformauth.Authenticator{
			Secret:   []byte("test-secret"),
			Audience: platformauth.AudienceRuntime,
		},
	}).registerRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/runtime/servers", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("/api/v1/runtime/servers status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if err := openapi.ValidateResponse(doc, http.MethodGet, "/api/v1/runtime/servers", rec.Code, rec.Body.Bytes(), "application/json"); err != nil {
		t.Fatalf("ValidateResponse(/api/v1/runtime/servers) error = %v", err)
	}
}

func TestOpenAPIEndpointServesEmbeddedSpec(t *testing.T) {
	mux := http.NewServeMux()
	(&server{}).registerRoutes(mux)

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
