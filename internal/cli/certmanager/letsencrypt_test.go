package certmanager

import (
	"strings"
	"testing"

	"mcp-runtime/internal/cli/core"
)

func TestValidateIngressManifestForACME(t *testing.T) {
	t.Parallel()
	if err := validateIngressManifestForACME("config/ingress/overlays/http"); err == nil {
		t.Fatal("expected error for dev http overlay")
	}
	if err := validateIngressManifestForACME("config/ingress/overlays/prod"); err != nil {
		t.Fatalf("prod overlay should be allowed: %v", err)
	}
	if err := validateIngressManifestForACME(""); err != nil {
		t.Fatalf("empty: %v", err)
	}
}

// TestACMETLSDNSNamesExcludesPlatformHost asserts that the registry-cert SANs
// do NOT include the platform host. The platform Ingress in mcp-platform uses
// cert-manager's ingress-shim to mint its own cert; including the platform
// host in the registry-cert would cause a redundant ACME order on every
// renewal (and the secret in the registry namespace cannot be referenced from
// a different namespace by Kubernetes Ingress anyway).
func TestACMETLSDNSNamesExcludesPlatformHost(t *testing.T) {
	prev := core.DefaultCLIConfig
	t.Cleanup(func() { core.DefaultCLIConfig = prev })
	core.DefaultCLIConfig = &core.CLIConfig{
		RegistryIngressHost: "registry.example.com",
		McpIngressHost:      "mcp.example.com",
		PlatformIngressHost: "platform.example.com",
	}
	names := acmeTLSDNSNames()
	want := map[string]bool{
		"registry.example.com": true,
		"mcp.example.com":      true,
	}
	if len(names) != len(want) {
		t.Fatalf("expected %d hostnames, got %d (%v)", len(want), len(names), names)
	}
	for _, n := range names {
		if !want[n] {
			t.Fatalf("unexpected hostname %q in registry SANs (platform host should be excluded)", n)
		}
	}
}

func TestRenderCertificateUsesRequestedNamespace(t *testing.T) {
	manifest := RenderCertificate("mcp-auth-server-cert", "mcp-auth-server-tls", "mcp-platform", []string{"auth.example.com"}, nil, "letsencrypt-prod")
	for _, want := range []string{
		"name: mcp-auth-server-cert",
		"namespace: mcp-platform",
		"secretName: mcp-auth-server-tls",
		`- "auth.example.com"`,
		"name: letsencrypt-prod",
	} {
		if !strings.Contains(manifest, want) {
			t.Fatalf("certificate manifest missing %q:\n%s", want, manifest)
		}
	}
}
