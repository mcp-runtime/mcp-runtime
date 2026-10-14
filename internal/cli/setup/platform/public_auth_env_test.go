package platform

import (
	"strings"
	"testing"

	setupplan "mcp-runtime/internal/cli/setup/plan"
)

func clearPublicAuthEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"GOOGLE_CLIENT_ID",
		"MCP_GOOGLE_CLIENT_ID",
		"OIDC_ISSUER",
		"MCP_OIDC_ISSUER",
		"OIDC_AUDIENCE",
		"MCP_OIDC_AUDIENCE",
		"OIDC_JWKS_URL",
		"MCP_OIDC_JWKS_URL",
	} {
		t.Setenv(key, "")
	}
}

func TestValidatePublicPlatformAuthEnvRequiresLoginConfigForPublicTLS(t *testing.T) {
	clearPublicAuthEnv(t)

	err := ValidatePublicPlatformAuthEnv(setupplan.PlatformModePublic, true, false)
	if err == nil {
		t.Fatal("expected missing public auth env to fail")
	}
	if !strings.Contains(err.Error(), "GOOGLE_CLIENT_ID") || !strings.Contains(err.Error(), "OIDC_ISSUER") {
		t.Fatalf("expected actionable env message, got %v", err)
	}
}

func TestValidatePublicPlatformAuthEnvAllowsGoogleClientID(t *testing.T) {
	clearPublicAuthEnv(t)
	t.Setenv("MCP_GOOGLE_CLIENT_ID", "client.apps.googleusercontent.com")

	if err := ValidatePublicPlatformAuthEnv(setupplan.PlatformModePublic, true, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidatePublicPlatformAuthConfigAllowsExistingGoogleClientID(t *testing.T) {
	clearPublicAuthEnv(t)

	err := ValidatePublicPlatformAuthConfig(setupplan.PlatformModePublic, true, false, map[string]string{
		"GOOGLE_CLIENT_ID": "client.apps.googleusercontent.com",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidatePublicPlatformAuthEnvAllowsCompleteOIDCConfig(t *testing.T) {
	clearPublicAuthEnv(t)
	t.Setenv("OIDC_ISSUER", "https://issuer.example.com")
	t.Setenv("OIDC_AUDIENCE", "mcp-runtime")
	t.Setenv("OIDC_JWKS_URL", "https://issuer.example.com/.well-known/jwks.json")

	if err := ValidatePublicPlatformAuthEnv(setupplan.PlatformModePublic, true, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidatePublicPlatformAuthConfigAllowsMergedOIDCConfig(t *testing.T) {
	clearPublicAuthEnv(t)
	t.Setenv("OIDC_AUDIENCE", "mcp-runtime")

	err := ValidatePublicPlatformAuthConfig(setupplan.PlatformModePublic, true, false, map[string]string{
		"OIDC_ISSUER":   "https://issuer.example.com",
		"OIDC_JWKS_URL": "https://issuer.example.com/.well-known/jwks.json",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidatePublicPlatformAuthEnvSkipsNonPublicTLSModes(t *testing.T) {
	clearPublicAuthEnv(t)

	cases := []struct {
		name         string
		platformMode string
		tlsEnabled   bool
		testMode     bool
	}{
		{name: "tenant tls", platformMode: setupplan.PlatformModeOrg, tlsEnabled: true},
		{name: "public without tls", platformMode: setupplan.PlatformModePublic, tlsEnabled: false},
		{name: "public tls test mode", platformMode: setupplan.PlatformModePublic, tlsEnabled: true, testMode: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidatePublicPlatformAuthEnv(tc.platformMode, tc.tlsEnabled, tc.testMode); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
