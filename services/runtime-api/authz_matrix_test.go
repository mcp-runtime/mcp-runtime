package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"mcp-runtime-api/internal/runtimeapi"
	"mcp-runtime/pkg/authzmatrix"
	"mcp-runtime/pkg/platformauth"
)

func TestAuthzMatrixRows(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "security", "authz-matrix.json")
	rows, err := authzmatrix.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	rows = authzmatrix.Filter(rows, "runtime-api")
	if len(rows) == 0 {
		t.Fatal("expected runtime-api authz rows")
	}

	srv := &server{
		runtime: &runtimeapi.RuntimeServer{},
		authentic: platformauth.Authenticator{
			Secret:         []byte("test-secret"),
			Audience:       platformauth.AudienceRuntime,
			ServiceAPIKeys: map[string]struct{}{"test-user": {}, "test-admin": {}},
			AdminAPIKeys:   map[string]struct{}{"test-admin": {}},
		},
	}
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	keys := map[string]string{
		authzmatrix.RoleUserKey:  "test-user",
		authzmatrix.RoleAdminKey: "test-admin",
	}

	for _, row := range rows {
		row := row
		t.Run(row.Path+"_"+row.Role, func(t *testing.T) {
			req := httptest.NewRequest(row.Method, row.Path, nil)
			authzmatrix.ApplyRole(req, row.Role, keys)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if row.ExpectAuthenticated {
				if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden || rec.Code == http.StatusNotFound {
					t.Fatalf("%s %s role=%s status = %d, want authenticated handler reach body=%s", row.Method, row.Path, row.Role, rec.Code, rec.Body.String())
				}
				return
			}
			if rec.Code != row.Expect {
				t.Fatalf("%s %s role=%s status = %d, want %d body=%s", row.Method, row.Path, row.Role, rec.Code, row.Expect, rec.Body.String())
			}
		})
	}
}

func TestAuthzMatrixCoversRegisteredRoutes(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "security", "authz-matrix.json")
	rows, err := authzmatrix.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	rows = authzmatrix.Filter(rows, "runtime-api")

	required := []string{
		"/health",
		"/api/v1/dashboard/summary",
		"/api/v1/runtime/servers",
		"/api/v1/runtime/tools",
		"/api/v1/runtime/servers/",
		"/api/v1/runtime/server-events",
		"/api/v1/runtime/observability/links",
		"/api/v1/runtime/observability/prometheus/query",
		"/api/v1/runtime/teams",
		"/api/v1/runtime/teams/",
		"/api/v1/runtime/namespaces",
		"/api/v1/runtime/namespaces/",
		"/api/v1/deployments",
		"/api/v1/deployments/",
		"/api/v1/admin/operations",
		"/api/v1/admin/deployments",
		"/api/v1/runtime/grants",
		"/api/v1/runtime/sessions",
		"/api/v1/runtime/adapter/sessions",
		"/api/v1/runtime/adapter/certificates",
		"/api/v1/runtime/registry/push",
		"/api/v1/runtime/components",
		"/api/v1/runtime/policy",
		"/api/v1/runtime/actions/restart",
		"/api/v1/runtime/grants/",
		"/api/v1/runtime/sessions/",
		"/api/v1/user/api-keys",
		"/api/v1/user/api-keys/",
	}

	for _, pattern := range required {
		if !matrixHasRoute(rows, pattern) {
			t.Fatalf("missing authz-matrix coverage for runtime-api route %q", pattern)
		}
	}
}

func matrixHasRoute(rows []authzmatrix.Row, pattern string) bool {
	for _, row := range rows {
		if row.Path == pattern {
			return true
		}
		if strings.HasSuffix(pattern, "/") && strings.HasPrefix(row.Path, pattern) {
			return true
		}
	}
	return false
}
