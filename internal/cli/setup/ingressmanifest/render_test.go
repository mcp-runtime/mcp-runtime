package ingressmanifest_test

import (
	"strings"
	"testing"

	"mcp-runtime/internal/cli/setup/ingressmanifest"
)

const (
	testPlatformNS      = "mcp-platform"
	testObservabilityNS = "mcp-observability"
)

func assertNoPrometheusRoute(t *testing.T, manifest, context string) {
	t.Helper()
	for _, unwanted := range []string{
		"- path: /prometheus\n",
		"name: prometheus",
		"number: 9090",
	} {
		if strings.Contains(manifest, unwanted) {
			t.Fatalf("%s must not contain %q:\n%s", context, unwanted, manifest)
		}
	}
}

func TestRenderPlatformUIIngressNoTLS(t *testing.T) {
	got := ingressmanifest.RenderPlatformUIIngress("platform.example.com", "", false, testPlatformNS, testObservabilityNS)
	mustContain := []string{
		"name: " + ingressmanifest.PlatformIngressName,
		"name: " + ingressmanifest.PlatformObservabilityIngressName,
		"namespace: " + testPlatformNS,
		"namespace: " + testObservabilityNS,
		"traefik.ingress.kubernetes.io/router.entrypoints: web",
		"traefik.ingress.kubernetes.io/router.middlewares: platform-admin-auth@file",
		`- host: "platform.example.com"`,
		"- path: /api/v1/auth\n",
		"- path: /api/v1/stats\n",
		"- path: /grafana\n",
		"- path: /\n",
		"name: mcp-platform-api",
		"name: mcp-analytics-api",
		"name: mcp-runtime-api",
		"name: mcp-ui",
		"name: grafana",
		"number: 8082",
		"number: 3000",
	}
	for _, want := range mustContain {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in manifest:\n%s", want, got)
		}
	}
	mustNotContain := []string{
		"name: " + ingressmanifest.PlatformHTTPRedirectIngressName,
		"- path: /api\n",
		"name: mcp-api",
	}
	for _, unwanted := range mustNotContain {
		if strings.Contains(got, unwanted) {
			t.Fatalf("manifest must not contain %q:\n%s", unwanted, got)
		}
	}
	assertNoPrometheusRoute(t, got, "manifest")
	if strings.Contains(got, "tls:") {
		t.Fatalf("did not expect a TLS block when issuer is empty:\n%s", got)
	}
}

func TestRenderPlatformUIIngressApiBeforeRoot(t *testing.T) {
	got := ingressmanifest.RenderPlatformUIIngress("platform.example.com", "", false, testPlatformNS, testObservabilityNS)
	pushIdx := strings.Index(got, "- path: /api/v1/runtime/registry/push")
	authIdx := strings.Index(got, "- path: /api/v1/auth\n")
	rootIdx := strings.Index(got, "- path: /\n")
	if pushIdx < 0 || authIdx < 0 || rootIdx < 0 {
		t.Fatalf("missing registry push, /api/v1/auth, or / paths:\n%s", got)
	}
	if pushIdx > authIdx || authIdx > rootIdx {
		t.Fatalf("/api/v1/runtime/registry/push must be listed before /api/v1/auth and / catch-all:\n%s", got)
	}
}

func TestRenderPlatformUIIngressWithTLS(t *testing.T) {
	got := ingressmanifest.RenderPlatformUIIngress("platform.mcpruntime.org", "letsencrypt-prod", true, testPlatformNS, testObservabilityNS)
	mustContain := []string{
		"traefik.ingress.kubernetes.io/router.entrypoints: websecure",
		"cert-manager.io/cluster-issuer: letsencrypt-prod",
		"tls:",
		`- "platform.mcpruntime.org"`,
		"secretName: " + ingressmanifest.PlatformTLSSecretName,
		`- host: "platform.mcpruntime.org"`,
		"name: " + ingressmanifest.PlatformObservabilityIngressName,
		"name: " + ingressmanifest.PlatformHTTPRedirectIngressName,
	}
	for _, want := range mustContain {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in manifest:\n%s", want, got)
		}
	}
	if count := strings.Count(got, "cert-manager.io/cluster-issuer:"); count != 1 {
		t.Fatalf("expected exactly one cert-manager annotation, got %d:\n%s", count, got)
	}
	if count := strings.Count(got, "secretName:"); count != 1 {
		t.Fatalf("expected exactly one TLS secret, got %d:\n%s", count, got)
	}
}

func TestRenderPlatformObservabilityIngressShape(t *testing.T) {
	got := ingressmanifest.RenderPlatformUIIngress("platform.example.com", "", false, testPlatformNS, testObservabilityNS)
	idx := strings.Index(got, "name: "+ingressmanifest.PlatformObservabilityIngressName)
	if idx < 0 {
		t.Fatalf("expected platform observability ingress:\n%s", got)
	}
	tail := got[idx:]
	mustContain := []string{
		"namespace: " + testObservabilityNS,
		"traefik.ingress.kubernetes.io/router.entrypoints: web",
		"traefik.ingress.kubernetes.io/router.middlewares: platform-admin-auth@file",
		`- host: "platform.example.com"`,
		"- path: /grafana\n",
		"name: grafana",
		"number: 3000",
	}
	for _, want := range mustContain {
		if !strings.Contains(tail, want) {
			t.Fatalf("observability ingress missing %q:\n%s", want, tail)
		}
	}
	assertNoPrometheusRoute(t, tail, "observability ingress")
}

func TestRenderPlatformObservabilityIngressWithTLS(t *testing.T) {
	got := ingressmanifest.RenderPlatformUIIngress("platform.mcpruntime.org", "letsencrypt-prod", true, testPlatformNS, testObservabilityNS)
	idx := strings.Index(got, "name: "+ingressmanifest.PlatformObservabilityIngressName)
	if idx < 0 {
		t.Fatalf("expected platform observability ingress:\n%s", got)
	}
	tail := got[idx:]
	if redirectIdx := strings.Index(tail, "name: "+ingressmanifest.PlatformHTTPRedirectIngressName); redirectIdx >= 0 {
		tail = tail[:redirectIdx]
	}
	mustContain := []string{
		"- path: /grafana\n",
		"name: grafana",
	}
	for _, want := range mustContain {
		if !strings.Contains(tail, want) {
			t.Fatalf("TLS observability ingress missing %q:\n%s", want, tail)
		}
	}
	assertNoPrometheusRoute(t, tail, "TLS observability ingress")
	if strings.Contains(tail, "secretName:") || strings.Contains(tail, "cert-manager.io/cluster-issuer:") {
		t.Fatalf("observability ingress must not own a certificate:\n%s", tail)
	}
}

func TestRenderPlatformUIIngressHTTPRedirectShape(t *testing.T) {
	got := ingressmanifest.RenderPlatformUIIngress("platform.mcpruntime.org", "letsencrypt-prod", true, testPlatformNS, testObservabilityNS)
	idx := strings.Index(got, "name: "+ingressmanifest.PlatformHTTPRedirectIngressName)
	if idx < 0 {
		t.Fatalf("expected HTTP redirect ingress when TLS configured:\n%s", got)
	}
	tail := got[idx:]
	mustContain := []string{
		"- path: /\n",
		"name: mcp-ui",
	}
	for _, want := range mustContain {
		if !strings.Contains(tail, want) {
			t.Fatalf("HTTP redirect ingress missing %q:\n%s", want, tail)
		}
	}
}

func TestRenderPlatformUIIngressProvidedTLSSecret(t *testing.T) {
	got := ingressmanifest.RenderPlatformUIIngress("platform.example.com", "", true, testPlatformNS, testObservabilityNS)
	for _, want := range []string{
		"traefik.ingress.kubernetes.io/router.entrypoints: websecure",
		"secretName: " + ingressmanifest.PlatformTLSSecretName,
		"name: mcp-platform-ui-http",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("provided TLS ingress missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "cert-manager.io/cluster-issuer") {
		t.Fatalf("provided TLS ingress must not request a cert-manager Certificate:\n%s", got)
	}
}
