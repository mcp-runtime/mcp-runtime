package registry

import (
	"net/http/httptest"
	"testing"

	"mcp-platform-api/internal/apiauth"
)

func TestNewAuthzConfigFromEnv(t *testing.T) {
	t.Setenv("PLATFORM_MODE", "public")
	t.Setenv("MCP_PLATFORM_MODE", "org")
	t.Setenv("PLATFORM_CATALOG_NAMESPACES", "mcp-servers-public,preview-extra")
	t.Setenv("MCP_PLATFORM_CATALOG_NAMESPACES", "ignored-extra")
	t.Setenv("PLATFORM_PUBLIC_NAMESPACE", "mcp-servers-public")
	t.Setenv("PLATFORM_ORG_NAMESPACE", "mcp-servers-org")

	cfg := NewAuthzConfigFromEnv()
	if cfg.mode != "public" {
		t.Fatalf("mode = %q, want public", cfg.mode)
	}
	for _, namespace := range []string{"mcp-servers-public", "mcp-servers-org", "preview-extra"} {
		if _, ok := cfg.catalogNamespaces[namespace]; !ok {
			t.Errorf("catalog namespace %q missing from config", namespace)
		}
	}
	if _, ok := cfg.catalogNamespaces["ignored-extra"]; ok {
		t.Fatal("MCP_PLATFORM_CATALOG_NAMESPACES should not override the primary variable")
	}
}

func TestPrincipalCanAccessRegistryPath(t *testing.T) {
	cfg := &AuthzConfig{
		mode:              "org",
		catalogNamespaces: map[string]struct{}{"mcp-servers-org": {}},
	}
	principal := apiauth.Principal{
		Role:  apiauth.RoleUser,
		Teams: []apiauth.PrincipalTeam{{Slug: "acme", Namespace: "team-acme"}},
	}
	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "team namespace", path: "/v2/team-acme/demo/manifests/latest", want: true},
		{name: "team slug", path: "/v2/acme/demo/manifests/latest", want: true},
		{name: "shared catalog", path: "/v2/mcp-servers-org/demo/manifests/latest", want: true},
		{name: "other namespace", path: "/v2/other-team/demo/manifests/latest"},
		{name: "non repository path", path: "/v2/_catalog"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v1/registry/authz", nil)
			req.Header.Set("X-Forwarded-Uri", tc.path)
			if got := PrincipalCanAccessRegistryPath(principal, req, cfg); got != tc.want {
				t.Fatalf("PrincipalCanAccessRegistryPath() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPrincipalCanAccessRegistryPathRequiresMountSourceAccess(t *testing.T) {
	cfg := &AuthzConfig{mode: "org"}
	principal := apiauth.Principal{
		Role:  apiauth.RoleUser,
		Teams: []apiauth.PrincipalTeam{{Slug: "acme", Namespace: "team-acme"}},
	}
	req := httptest.NewRequest("GET", "/api/v1/registry/authz", nil)
	req.Header.Set("X-Forwarded-Uri", "/v2/team-acme/demo/blobs/uploads?from=other-team/base")
	if PrincipalCanAccessRegistryPath(principal, req, cfg) {
		t.Fatal("access allowed when mount source repository belongs to another team")
	}
}

func TestRegistryForwardedPathPrefersRegistryURI(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/registry/authz", nil)
	req.Header.Set("X-Forwarded-Uri", "/api/v1/registry/authz")
	req.Header.Set("X-Forwarded-URL", "/v2/acme/demo/manifests/latest")

	got := RegistryForwardedPath(req)
	want := "/v2/acme/demo/manifests/latest"
	if got != want {
		t.Fatalf("RegistryForwardedPath() = %q, want %q", got, want)
	}
}

func TestRegistryForwardedPathUsesURIWhenRegistryPath(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/registry/authz", nil)
	req.Header.Set("X-Forwarded-Uri", "/v2/acme/demo/manifests/latest")

	got := RegistryForwardedPath(req)
	want := "/v2/acme/demo/manifests/latest"
	if got != want {
		t.Fatalf("RegistryForwardedPath() = %q, want %q", got, want)
	}
}
