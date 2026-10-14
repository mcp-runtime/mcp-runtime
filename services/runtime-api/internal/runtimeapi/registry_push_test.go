package runtimeapi

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"mcp-runtime/pkg/publishscope"
)

func TestRegistryPushAuthContextTenantRequiresTeamScope(t *testing.T) {
	t.Setenv("PLATFORM_MODE", "org")
	p := principal{
		Role: roleUser,
		Teams: []principalTeam{{
			Slug:      "acme",
			Namespace: "mcp-team-acme",
			Role:      "owner",
		}},
	}
	_, _, err := registryPushAuthContext("registry.example.com/beta/demo:v1", publishscope.Tenant, p)
	if err == nil {
		t.Fatal("expected forbidden team scope")
	}
}

func TestRegistryPushAuthContextTenantAllowsOwnedTeam(t *testing.T) {
	t.Setenv("PLATFORM_MODE", "org")
	p := principal{
		Role: roleUser,
		Teams: []principalTeam{{
			Slug:      "acme",
			Namespace: "mcp-team-acme",
			Role:      "owner",
		}},
	}
	namespace, teamSlug, err := registryPushAuthContext("registry.example.com/acme/demo:v1", publishscope.Tenant, p)
	if err != nil {
		t.Fatalf("registryPushAuthContext() error = %v", err)
	}
	if namespace != "mcp-team-acme" || teamSlug != "acme" {
		t.Fatalf("namespace=%q teamSlug=%q", namespace, teamSlug)
	}
}

func TestRegistryPushAuthContextEmptyScopeAllowsAdminUnscopedRepo(t *testing.T) {
	t.Setenv("PLATFORM_MODE", "org")
	namespace, teamSlug, err := registryPushAuthContext("registry.example.com/demo:v1", "", principal{Role: roleAdmin})
	if err != nil {
		t.Fatalf("registryPushAuthContext() error = %v", err)
	}
	if namespace != "" || teamSlug != "" {
		t.Fatalf("namespace=%q teamSlug=%q", namespace, teamSlug)
	}
}

func TestRegistryPushAuthContextTenantStillRequiresScopedRepo(t *testing.T) {
	t.Setenv("PLATFORM_MODE", "org")
	if _, _, err := registryPushAuthContext("registry.example.com/demo:v1", publishscope.Tenant, principal{Role: roleAdmin}); err == nil {
		t.Fatal("expected tenant scope to require a team-scoped repository")
	}
}

func TestRegistryPushAuthContextPublicRequiresCatalogWrite(t *testing.T) {
	t.Setenv("PLATFORM_MODE", "org")
	p := principal{Role: roleUser}
	if _, _, err := registryPushAuthContext("registry.example.com/public/demo:v1", publishscope.Public, p); err == nil {
		t.Fatal("expected public catalog write rejection in org mode")
	}
}

func TestRegistryPushAuthContextPublicAllowsWhenCatalogWritable(t *testing.T) {
	t.Setenv("PLATFORM_MODE", "public")
	p := principal{Role: roleUser, AllowedNamespaces: []string{defaultPublicCatalogNamespace}}
	namespace, teamSlug, err := registryPushAuthContext("registry.example.com/public/demo:v1", publishscope.Public, p)
	if err != nil {
		t.Fatalf("registryPushAuthContext() error = %v", err)
	}
	if namespace != defaultPublicCatalogNamespace || teamSlug != "" {
		t.Fatalf("namespace=%q teamSlug=%q", namespace, teamSlug)
	}
}

func TestValidateDeployImageRejectsCrossTeamPushTarget(t *testing.T) {
	t.Setenv("MCP_REGISTRY_INGRESS_HOST", "registry.example.com")
	p := principal{
		Role: roleUser,
		Teams: []principalTeam{{
			Slug:      "acme",
			Namespace: "mcp-team-acme",
			Role:      "owner",
		}},
	}
	target := "registry.example.com/beta/demo:v1"
	namespace, teamSlug, err := registryPushAuthContext(target, publishscope.Tenant, p)
	if err == nil {
		t.Fatalf("expected auth context error, got namespace=%q teamSlug=%q", namespace, teamSlug)
	}
	if err := ValidateDeployImage(target, "mcp-team-acme", "acme", p.Role); err == nil {
		t.Fatal("expected deploy image validation failure for cross-team repo")
	}
}

func TestReadRegistryPushRequestCleansUpTempFileOnDuplicateUpload(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv(registryPushTempDirEnv, tmpDir)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("target", "registry.example.com/acme/demo:v1"); err != nil {
		t.Fatalf("WriteField() error = %v", err)
	}
	part, err := writer.CreateFormFile("image_tar", "demo.tar")
	if err != nil {
		t.Fatalf("CreateFormFile() error = %v", err)
	}
	if _, err := part.Write([]byte("first")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	part2, err := writer.CreateFormFile("image_tar", "demo2.tar")
	if err != nil {
		t.Fatalf("CreateFormFile() duplicate error = %v", err)
	}
	if _, err := part2.Write([]byte("second")); err != nil {
		t.Fatalf("Write() duplicate error = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/registry/push", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if _, err := readRegistryPushRequest(req); err == nil {
		t.Fatal("expected duplicate upload error")
	}

	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no temp files, found %d", len(entries))
	}
}

func TestReadRegistryPushRequestRequiresMetadataBeforeImageUpload(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv(registryPushTempDirEnv, tmpDir)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("image_tar", "demo.tar")
	if err != nil {
		t.Fatalf("CreateFormFile() error = %v", err)
	}
	if _, err := part.Write([]byte("first")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := writer.WriteField("target", "registry.example.com/acme/demo:v1"); err != nil {
		t.Fatalf("WriteField(target) error = %v", err)
	}
	if err := writer.WriteField("scope", string(publishscope.Tenant)); err != nil {
		t.Fatalf("WriteField(scope) error = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/registry/push", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if _, err := readRegistryPushRequest(req); err == nil {
		t.Fatal("expected metadata ordering error")
	}

	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no temp files, found %d", len(entries))
	}
}
