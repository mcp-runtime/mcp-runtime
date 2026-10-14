package platformmode

import (
	"reflect"
	"testing"

	"mcp-runtime/pkg/publishscope"
)

func TestNormalize(t *testing.T) {
	cases := []struct {
		in    string
		want  Mode
		known bool
	}{
		{"", ModeOrg, true},
		{"tenant", ModeOrg, false},
		{"TENANT", ModeOrg, false},
		{"  tenant  ", ModeOrg, false},
		{"org", ModeOrg, true},
		{"Org", ModeOrg, true},
		{"public", ModePublic, true},
		{"PUBLIC", ModePublic, true},
		{"tenent", ModeOrg, false},
		{"random", ModeOrg, false},
	}
	for _, tc := range cases {
		got, ok := Normalize(tc.in)
		if got != tc.want || ok != tc.known {
			t.Errorf("Normalize(%q) = (%v, %v), want (%v, %v)", tc.in, got, ok, tc.want, tc.known)
		}
	}
}

func TestTwoModesAndThreePublishScopes(t *testing.T) {
	if _, ok := Normalize("tenant"); ok {
		t.Fatal("tenant must only be a publish scope")
	}
	if got, ok := Normalize(""); !ok || got != ModeOrg {
		t.Fatalf("default mode=%s known=%t", got, ok)
	}
	if got := ModeOrg.EnabledPublishScopes(); !reflect.DeepEqual(got, []publishscope.Scope{publishscope.Tenant, publishscope.Org}) {
		t.Fatalf("org scopes=%v", got)
	}
	if got := ModePublic.EnabledPublishScopes(); !reflect.DeepEqual(got, []publishscope.Scope{publishscope.Tenant, publishscope.Org, publishscope.Public}) {
		t.Fatalf("public scopes=%v", got)
	}
}

func TestPublicNamespacesNeverIncludeOrganizationOverrides(t *testing.T) {
	t.Setenv("PLATFORM_ORG_NAMESPACE", "private-org")
	t.Setenv("PLATFORM_CATALOG_NAMESPACES", "private-org,mcp-servers-org,preview-extra")
	if got := ModePublic.CatalogNamespacesForScope(publishscope.Public); !reflect.DeepEqual(got, []string{DefaultPublicCatalogNamespace, "preview-extra"}) {
		t.Fatalf("public namespaces=%v", got)
	}
	if got := ModePublic.CatalogNamespacesForScope(publishscope.Org); !reflect.DeepEqual(got, []string{"private-org"}) {
		t.Fatalf("org namespaces=%v", got)
	}
	if scope, ok := ModePublic.CatalogScope("private-org"); !ok || scope != publishscope.Org {
		t.Fatalf("private-org scope=%s ok=%t", scope, ok)
	}
}

func TestFromEnvPrefersPlatformMode(t *testing.T) {
	t.Setenv("PLATFORM_MODE", "org")
	t.Setenv("MCP_PLATFORM_MODE", "public")
	if got := FromEnv(); got != ModeOrg {
		t.Fatalf("FromEnv() = %v, want %v", got, ModeOrg)
	}
}

func TestFromEnvFallsBackToMCPPlatformMode(t *testing.T) {
	t.Setenv("PLATFORM_MODE", "")
	t.Setenv("MCP_PLATFORM_MODE", "public")
	if got := FromEnv(); got != ModePublic {
		t.Fatalf("FromEnv() = %v, want %v", got, ModePublic)
	}
}

func TestFromEnvUnknownFallsBackToTenant(t *testing.T) {
	t.Setenv("PLATFORM_MODE", "weird")
	if got := FromEnv(); got != ModeOrg {
		t.Fatalf("FromEnv() = %v, want %v", got, ModeOrg)
	}
}

func TestSharedCatalogWritable(t *testing.T) {
	cases := map[Mode]bool{
		Mode("invalid"): false,
		ModeOrg:         true,
		ModePublic:      true,
	}
	for mode, want := range cases {
		if got := mode.SharedCatalogWritable(); got != want {
			t.Errorf("(%v).SharedCatalogWritable() = %v, want %v", mode, got, want)
		}
	}
}

func TestPublicCatalogEnabled(t *testing.T) {
	cases := map[Mode]bool{
		Mode("invalid"): false,
		ModeOrg:         false,
		ModePublic:      true,
	}
	for mode, want := range cases {
		if got := mode.PublicCatalogEnabled(); got != want {
			t.Errorf("(%v).PublicCatalogEnabled() = %v, want %v", mode, got, want)
		}
	}
}

func TestDefaultCatalogNamespaceTenant(t *testing.T) {
	t.Setenv("PLATFORM_CATALOG_NAMESPACE", "custom")
	if got := Mode("invalid").DefaultCatalogNamespace(); got != "" {
		t.Fatalf("tenant DefaultCatalogNamespace = %q, want empty", got)
	}
}

func TestDefaultCatalogNamespacePrecedence(t *testing.T) {
	t.Setenv("PLATFORM_CATALOG_NAMESPACE", "primary")
	t.Setenv("MCP_PLATFORM_CATALOG_NAMESPACE", "secondary")
	t.Setenv("PLATFORM_ORG_NAMESPACE", "org-override")
	if got := ModeOrg.DefaultCatalogNamespace(); got != "primary" {
		t.Fatalf("org DefaultCatalogNamespace = %q, want primary", got)
	}
}

func TestDefaultCatalogNamespaceModeOverrides(t *testing.T) {
	t.Setenv("PLATFORM_ORG_NAMESPACE", "org-only")
	t.Setenv("PLATFORM_PUBLIC_NAMESPACE", "public-only")
	if got := ModeOrg.DefaultCatalogNamespace(); got != "org-only" {
		t.Fatalf("org DefaultCatalogNamespace = %q, want org-only", got)
	}
	if got := ModePublic.DefaultCatalogNamespace(); got != "public-only" {
		t.Fatalf("public DefaultCatalogNamespace = %q, want public-only", got)
	}
}

func TestDefaultCatalogNamespaceBuiltinDefaults(t *testing.T) {
	if got := ModeOrg.DefaultCatalogNamespace(); got != DefaultOrgCatalogNamespace {
		t.Fatalf("org DefaultCatalogNamespace = %q, want %q", got, DefaultOrgCatalogNamespace)
	}
	if got := ModePublic.DefaultCatalogNamespace(); got != DefaultPublicCatalogNamespace {
		t.Fatalf("public DefaultCatalogNamespace = %q, want %q", got, DefaultPublicCatalogNamespace)
	}
}

func TestCatalogNamespacesTenantIsEmpty(t *testing.T) {
	t.Setenv("PLATFORM_CATALOG_NAMESPACES", "ignored")
	if got := Mode("invalid").CatalogNamespaces(); got != nil {
		t.Fatalf("tenant CatalogNamespaces = %v, want nil", got)
	}
}

func TestCatalogNamespacesDedupes(t *testing.T) {
	t.Setenv("PLATFORM_CATALOG_NAMESPACES", "mcp-servers-org, extra, mcp-servers-org , other")
	got := ModeOrg.CatalogNamespaces()
	want := []string{"mcp-servers-org", "extra", "other"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CatalogNamespaces = %v, want %v", got, want)
	}
}

func TestCatalogNamespacesPublicFallbacks(t *testing.T) {
	t.Setenv("PLATFORM_CATALOG_NAMESPACES", "")
	t.Setenv("MCP_PLATFORM_CATALOG_NAMESPACES", "")
	t.Setenv("PLATFORM_PUBLIC_NAMESPACES", "mcp-servers-public, preview-extra")
	got := ModePublic.CatalogNamespaces()
	want := []string{"mcp-servers-public", "preview-extra", DefaultOrgCatalogNamespace}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("public CatalogNamespaces = %v, want %v", got, want)
	}
}

func TestCatalogNamespacesSharedListWinsOverPublicFallback(t *testing.T) {
	// PLATFORM_CATALOG_NAMESPACES is the cross-mode override and must beat
	// the public-mode-only env. The UI and API both agree on this order so
	// they cannot disagree about what is a valid catalog namespace.
	t.Setenv("PLATFORM_CATALOG_NAMESPACES", "shared-extra")
	t.Setenv("PLATFORM_PUBLIC_NAMESPACES", "public-extra")
	got := ModePublic.CatalogNamespaces()
	want := []string{DefaultPublicCatalogNamespace, "shared-extra", DefaultOrgCatalogNamespace}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("public CatalogNamespaces = %v, want %v", got, want)
	}
}

func TestIsCatalogNamespace(t *testing.T) {
	t.Setenv("PLATFORM_CATALOG_NAMESPACES", "extra-namespace")
	cases := []struct {
		mode Mode
		ns   string
		want bool
	}{
		{Mode("invalid"), "mcp-servers-org", false},
		{ModeOrg, "mcp-servers-org", true},
		{ModeOrg, "extra-namespace", true},
		{ModeOrg, "unknown", false},
		{ModeOrg, "", false},
		{ModePublic, "mcp-servers-public", true},
		{ModePublic, "  mcp-servers-public  ", true},
	}
	for _, tc := range cases {
		if got := tc.mode.IsCatalogNamespace(tc.ns); got != tc.want {
			t.Errorf("(%v).IsCatalogNamespace(%q) = %v, want %v", tc.mode, tc.ns, got, tc.want)
		}
	}
}
