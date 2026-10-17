package platform

import (
	"strings"
	"testing"
)

const placeholderValue = "changeme" // the value shipped in k8s/02-secrets.yaml.example

func placeholderEnv(t *testing.T, testMode string) {
	t.Helper()
	for _, key := range []string{"MCP_PLATFORM_ADMIN_EMAIL", "MCP_PLATFORM_ADMIN_PASSWORD", "PLATFORM_ADMIN_EMAIL", "PLATFORM_ADMIN_PASSWORD"} {
		t.Setenv(key, "")
	}
	t.Setenv("MCP_RUNTIME_TEST_MODE", testMode)
}

func readerWith(overrides map[string]string) analyticsSecretValueReader {
	return func(_, _, key string) (string, error) { return overrides[key], nil }
}

func TestRenderRejectsPreservedPlaceholderCredentials(t *testing.T) {
	cases := map[string]string{
		"API_KEYS":                  placeholderValue,
		"INGEST_API_KEYS":           placeholderValue,
		"ADMIN_API_KEYS":            "real-admin-key," + placeholderValue,
		"UI_API_KEY":                " ChangeMe ",
		"GRAFANA_ADMIN_PASSWORD":    placeholderValue,
		"POSTGRES_PASSWORD":         placeholderValue,
		"JWT_SECRET":                placeholderValue,
		"INTERNAL_AUTH_TOKEN":       placeholderValue,
		"UI_SESSION_ENCRYPTION_KEY": placeholderValue,
		"PLATFORM_ADMIN_PASSWORD":   placeholderValue,
		"POSTGRES_DSN":              "postgres://mcp_runtime:" + placeholderValue + "@mcp-postgres.mcp-platform.svc.cluster.local:5432/mcp_runtime?sslmode=disable",
		"UI_SESSION_DATABASE_URL":   "postgres://mcp_runtime:" + placeholderValue + "@mcp-postgres.mcp-platform.svc.cluster.local:5432/mcp_runtime?sslmode=disable",
	}
	for key, value := range cases {
		t.Run(key, func(t *testing.T) {
			placeholderEnv(t, "")
			values := map[string]string{key: value}
			if key == "PLATFORM_ADMIN_PASSWORD" {
				values["PLATFORM_ADMIN_EMAIL"] = "admin@example.com"
			}
			manifest, err := renderAnalyticsSecretManifestWithReader(readerWith(values))
			if err == nil {
				t.Fatal("expected placeholder credential to be rejected")
			}
			if manifest != "" {
				t.Fatal("no manifest may be rendered for a rejected credential")
			}
			msg := strings.ToLower(err.Error())
			if strings.Contains(msg, placeholderValue) {
				t.Fatalf("error leaks the credential value: %v", err)
			}
			if !strings.Contains(err.Error(), key) {
				t.Fatalf("error should name key %s: %v", key, err)
			}
		})
	}
}

func TestRenderPlaceholderErrorNeverContainsSecretContext(t *testing.T) {
	placeholderEnv(t, "")
	dsn := "postgres://mcp_runtime:" + placeholderValue + "@host/db"
	_, err := renderAnalyticsSecretManifestWithReader(readerWith(map[string]string{"POSTGRES_DSN": dsn}))
	if err == nil || strings.Contains(err.Error(), dsn) || strings.Contains(err.Error(), placeholderValue) {
		t.Fatalf("error must not echo the DSN or password: %v", err)
	}
}

func TestRenderPreservesValidExistingCredentials(t *testing.T) {
	placeholderEnv(t, "")
	values := map[string]string{
		"API_KEYS":          "api-key-a,api-key-b",
		"JWT_SECRET":        "keep-this-jwt-secret",
		"POSTGRES_PASSWORD": "keep-this-pg-password",
		"POSTGRES_DSN":      "postgres://mcp_runtime:keep-this-pg-password@mcp-postgres:5432/mcp_runtime",
		// A value that merely contains the placeholder text is not the placeholder.
		"INTERNAL_AUTH_TOKEN": "prefix-" + placeholderValue + "-suffix",
	}
	manifest, err := renderAnalyticsSecretManifestWithReader(readerWith(values))
	if err != nil {
		t.Fatal(err)
	}
	data := secretStringDataFromManifest(t, manifest)
	for _, key := range []string{"JWT_SECRET", "POSTGRES_PASSWORD", "POSTGRES_DSN", "INTERNAL_AUTH_TOKEN"} {
		if data[key] != values[key] {
			t.Fatalf("%s was rotated or altered", key)
		}
	}
	if !strings.HasPrefix(data["API_KEYS"], "api-key-a,api-key-b") {
		t.Fatal("API_KEYS was rotated")
	}
}

func TestRenderTestModeExemptsPlaceholderCredentials(t *testing.T) {
	placeholderEnv(t, "1")
	manifest, err := renderAnalyticsSecretManifestWithReader(readerWith(map[string]string{"JWT_SECRET": placeholderValue}))
	if err != nil {
		t.Fatalf("test mode must keep working: %v", err)
	}
	if secretStringDataFromManifest(t, manifest)["JWT_SECRET"] != placeholderValue {
		t.Fatal("test mode must preserve the existing value untouched")
	}
}
