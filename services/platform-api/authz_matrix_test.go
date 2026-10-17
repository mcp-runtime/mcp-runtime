package main

import (
	"github.com/golang-jwt/jwt/v4"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcp-runtime/pkg/authzmatrix"
	"mcp-runtime/pkg/platformauth"
)

func TestAuthzMatrixRows(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "security", "authz-matrix.json")
	rows, err := authzmatrix.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	rows = authzmatrix.Filter(rows, "platform-api")
	if len(rows) == 0 {
		t.Fatal("expected platform-api authz rows")
	}

	srv := &apiServer{
		apiKeys:      map[string]struct{}{"test-user": {}, "test-admin": {}},
		adminAPIKeys: map[string]struct{}{"test-admin": {}},
	}
	testAuthenticator(srv)
	srv.authentic.Secret = []byte("test-secret")

	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	keys := map[string]string{
		authzmatrix.RoleUserKey:   "test-user",
		authzmatrix.RoleAdminKey:  "test-admin",
		authzmatrix.RoleIngestKey: "test-ingest",
	}

	for role, audiences := range map[string][]string{
		"platform-jwt":       {platformauth.AudiencePlatform},
		"runtime-jwt":        {platformauth.AudienceRuntime},
		"analytics-jwt":      {platformauth.AudienceAnalytics},
		"multi-audience-jwt": {platformauth.AudiencePlatform, platformauth.AudienceRuntime, platformauth.AudienceAnalytics},
	} {
		token, err := platformauth.Sign([]byte("test-secret"), platformauth.Principal{Subject: "matrix-user", Role: platformauth.RoleAdmin}, time.Minute, audiences)
		if err != nil {
			t.Fatal(err)
		}
		keys[role] = token
	}
	expired := platformauth.ClaimsFromPrincipal(platformauth.Principal{Subject: "matrix-user", Role: platformauth.RoleAdmin})
	expired.Issuer = platformauth.Issuer
	expired.Audience = jwt.ClaimStrings{srv.authentic.Audience}
	expired.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Minute))
	keys["expired-jwt"], err = jwt.NewWithClaims(jwt.SigningMethodHS256, expired).SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatal(err)
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
	rows = authzmatrix.Filter(rows, "platform-api")

	required := []string{
		"/health",
		"/api/v1/registry/authz",
		"/api/v1/auth/login",
		"/api/v1/auth/oidc",
		"/api/v1/auth/signup",
		"/api/v1/users",
		"/api/v1/auth/me",
		"/api/v1/user/registry-credentials",
		"/api/v1/user/registry-credentials/",
		"/api/v1/user/activity/image-publish",
		"/api/v1/admin/namespaces",
		"/api/v1/admin/audit",
	}

	for _, pattern := range required {
		if !matrixHasPlatformRoute(rows, pattern) {
			t.Fatalf("missing authz-matrix coverage for platform-api route %q", pattern)
		}
	}
}

func matrixHasPlatformRoute(rows []authzmatrix.Row, pattern string) bool {
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
