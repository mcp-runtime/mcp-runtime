package main

import (
	"context"
	"github.com/golang-jwt/jwt/v4"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"mcp-analytics-api/internal/analytics"
	"mcp-runtime/pkg/authzmatrix"
	clickhousepkg "mcp-runtime/pkg/clickhouse"
	"mcp-runtime/pkg/platformauth"
)

type matrixEventStub struct{}

func TestAttributedEventQueriesRemainAdminOnly(t *testing.T) {
	srv := &server{authentic: platformauth.Authenticator{Secret: []byte("test-secret"), Audience: platformauth.AudienceAnalytics, ServiceAPIKeys: map[string]struct{}{"test-user": {}, "test-admin": {}}, AdminAPIKeys: map[string]struct{}{"test-admin": {}}}, events: analytics.NewHandler(matrixEventStub{})}
	mux := http.NewServeMux()
	srv.registerRoutes(mux)
	for _, tt := range []struct {
		key    string
		status int
	}{{"", 401}, {"test-user", 403}, {"test-admin", 200}} {
		req := httptest.NewRequest("GET", "/api/v1/events?matched_grant=grant&matched_grant_namespace=tenant&matched_session=session&matched_session_namespace=tenant&since=2026-10-07T12:00:00Z&until=2026-10-14T12:00:00Z", nil)
		if tt.key != "" {
			req.Header.Set("x-api-key", tt.key)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != tt.status {
			t.Fatalf("key=%q status=%d body=%s", tt.key, rec.Code, rec.Body.String())
		}
	}
}

func (matrixEventStub) QueryEvents(_ context.Context, limit, _ int) ([]clickhousepkg.EventRow, error) {
	return make([]clickhousepkg.EventRow, limit), nil
}
func (matrixEventStub) QueryStats(context.Context) (uint64, error) { return 0, nil }
func (matrixEventStub) QuerySources(context.Context) ([]clickhousepkg.SourceStat, error) {
	return nil, nil
}
func (matrixEventStub) QueryEventTypes(context.Context) ([]clickhousepkg.EventTypeStat, error) {
	return nil, nil
}
func (matrixEventStub) QueryEventsFiltered(context.Context, clickhousepkg.EventFilters) ([]clickhousepkg.EventRow, error) {
	return nil, nil
}

func TestAuthzMatrixRows(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "security", "authz-matrix.json")
	rows, err := authzmatrix.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	rows = authzmatrix.Filter(rows, "analytics-api")
	if len(rows) == 0 {
		t.Fatal("expected analytics-api authz rows")
	}

	srv := &server{
		authentic: platformauth.Authenticator{
			Secret:         []byte("test-secret"),
			Audience:       platformauth.AudienceAnalytics,
			ServiceAPIKeys: map[string]struct{}{"test-user": {}, "test-admin": {}},
			AdminAPIKeys:   map[string]struct{}{"test-admin": {}},
		},
		events: analytics.NewHandler(matrixEventStub{}),
	}
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
	rows = authzmatrix.Filter(rows, "analytics-api")

	required := []string{
		"/health",
		"/api/v1/events",
		"/api/v1/stats",
		"/api/v1/sources",
		"/api/v1/event-types",
		"/api/v1/analytics/usage",
		"/api/v1/user/analytics/usage",
	}
	for _, pattern := range required {
		found := false
		for _, row := range rows {
			if row.Path == pattern {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing authz-matrix coverage for analytics-api route %q", pattern)
		}
	}
}
