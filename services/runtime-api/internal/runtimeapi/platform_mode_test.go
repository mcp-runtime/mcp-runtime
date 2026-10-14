package runtimeapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDefaultCatalogNamespaceForModeUsesCatalogOverrideInDefaultOrgMode(t *testing.T) {
	t.Setenv("PLATFORM_MODE", "org")
	t.Setenv("PLATFORM_CATALOG_NAMESPACE", "custom-catalog")

	if got := defaultCatalogNamespaceForMode(); got != "custom-catalog" {
		t.Fatalf("default catalog namespace = %q, want custom-catalog", got)
	}
}

func TestDefaultCatalogNamespaceForModeUsesCatalogOverrideInSharedMode(t *testing.T) {
	t.Setenv("PLATFORM_MODE", "org")
	t.Setenv("PLATFORM_CATALOG_NAMESPACE", "custom-catalog")

	if got := defaultCatalogNamespaceForMode(); got != "custom-catalog" {
		t.Fatalf("default catalog namespace = %q, want custom-catalog", got)
	}
}

func TestPlatformModeUsesStrictValues(t *testing.T) {
	t.Setenv("PLATFORM_MODE", "tenent")

	if got := PlatformMode(); got != platformModeOrg {
		t.Fatalf("platform mode = %q, want org fallback", got)
	}
}

func TestCatalogNamespaceEntriesIncludeSharedCatalogInTenantMode(t *testing.T) {
	t.Setenv("PLATFORM_MODE", "org")

	got := appendCatalogNamespaceEntries(nil)
	if len(got) != 2 {
		t.Fatalf("catalog namespaces = %#v, want shared and org catalog entries", got)
	}
	if got[0]["namespace"] != sharedCatalogNamespace {
		t.Fatalf("namespace = %#v, want %q", got[0]["namespace"], sharedCatalogNamespace)
	}
	if got[0]["scope"] != "shared" {
		t.Fatalf("scope = %#v, want shared", got[0]["scope"])
	}
	if got[0]["is_shared"] != true {
		t.Fatalf("is_shared = %#v, want true", got[0]["is_shared"])
	}
}

func TestSharedCatalogEntryStaysSharedInPublicMode(t *testing.T) {
	t.Setenv("PLATFORM_MODE", "public")

	got := catalogNamespaceEntry(sharedCatalogNamespace)
	if got["scope"] != "shared" {
		t.Fatalf("scope = %#v, want shared", got["scope"])
	}
	if got["is_public"] != false {
		t.Fatalf("is_public = %#v, want false", got["is_public"])
	}
}

func TestPublicModePrincipalCanReadCatalogAndOwnedNamespaces(t *testing.T) {
	t.Setenv("PLATFORM_MODE", "public")
	p := principal{
		Role:      roleUser,
		Subject:   "user-1",
		Namespace: "user-1",
		AllowedNamespaces: []string{
			"user-1",
		},
	}
	if !principalCanReadNamespace(p, defaultPublicCatalogNamespace) {
		t.Fatalf("expected user to read public catalog namespace %q", defaultPublicCatalogNamespace)
	}
	if !principalCanReadNamespace(p, "user-1") {
		t.Fatal("expected user to read owned namespace in public mode")
	}
	if principalCanReadNamespace(p, "other-user") {
		t.Fatal("did not expect user to read unrelated namespace")
	}
}

func TestPublicCatalogFallbackAllowsCatalogGETAndInjectsNamespace(t *testing.T) {
	t.Setenv("PLATFORM_MODE", "public")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/servers", nil)
	p, ok := PublicCatalogFallback(req)
	if !ok {
		t.Fatal("expected public catalog fallback")
	}
	if p.AuthType != "public_catalog" {
		t.Fatalf("auth type = %q, want public_catalog", p.AuthType)
	}
	if got := req.URL.Query().Get("namespace"); got != defaultPublicCatalogNamespace {
		t.Fatalf("namespace query = %q, want %q", got, defaultPublicCatalogNamespace)
	}
}

func TestPublicCatalogFallbackRejectsNonCatalogOrMutatingRequests(t *testing.T) {
	t.Setenv("PLATFORM_MODE", "public")

	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/api/v1/runtime/servers", nil),
		httptest.NewRequest(http.MethodGet, "/api/v1/runtime/server-events", nil),
	} {
		if _, ok := PublicCatalogFallback(req); ok {
			t.Fatalf("unexpected public catalog fallback for %s %s", req.Method, req.URL.Path)
		}
	}
}

func TestPublicCatalogFallbackDisabledOutsidePublicMode(t *testing.T) {
	t.Setenv("PLATFORM_MODE", "org")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/servers", nil)
	if _, ok := PublicCatalogFallback(req); ok {
		t.Fatal("unexpected public catalog fallback in org mode")
	}
}
