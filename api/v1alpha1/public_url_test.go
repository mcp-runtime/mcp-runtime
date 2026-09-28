package v1alpha1

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMCPServerDefaultDerivesOAuthAudience(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		spec    MCPServerSpec
		options MCPServerDefaultOptions
		want    string
	}{
		{
			name:    "path-based server on the operator default host over tls",
			spec:    MCPServerSpec{Auth: &AuthConfig{}},
			options: MCPServerDefaultOptions{DefaultIngressHost: "mcp.example.com", DefaultIngressTLS: true},
			want:    "https://mcp.example.com/buddy/mcp",
		},
		{
			name: "custom public path prefix",
			spec: MCPServerSpec{
				PublicPathPrefix: "/tools/buddy/",
				Auth:             &AuthConfig{},
			},
			options: MCPServerDefaultOptions{DefaultIngressHost: "mcp.example.com", DefaultIngressTLS: true},
			want:    "https://mcp.example.com/tools/buddy/mcp",
		},
		{
			name: "explicit ingress host wins over the operator default",
			spec: MCPServerSpec{
				IngressHost:        "buddy.example.com",
				IngressAnnotations: map[string]string{traefikRouterTLSAnnotation: "true"},
				Auth:               &AuthConfig{},
			},
			options: MCPServerDefaultOptions{DefaultIngressHost: "mcp.example.com"},
			want:    "https://buddy.example.com/buddy/mcp",
		},
		{
			name:    "plain http without tls",
			spec:    MCPServerSpec{Auth: &AuthConfig{}},
			options: MCPServerDefaultOptions{DefaultIngressHost: "localhost:18080"},
			want:    "http://localhost:18080/buddy/mcp",
		},
		{
			name: "explicit audience is kept",
			spec: MCPServerSpec{Auth: &AuthConfig{
				Audience: "https://canonical.example.com/buddy/mcp",
			}},
			options: MCPServerDefaultOptions{DefaultIngressHost: "mcp.example.com", DefaultIngressTLS: true},
			want:    "https://canonical.example.com/buddy/mcp",
		},
		{
			name:    "no known host leaves audience unset",
			spec:    MCPServerSpec{Auth: &AuthConfig{}},
			options: MCPServerDefaultOptions{DefaultIngressTLS: true},
			want:    "",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server := &MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "buddy"}, Spec: testCase.spec}
			server.Spec.Image = "example.com/buddy"
			server.DefaultWithOptions(testCase.options)
			server.ResolveDerivedAuth(testCase.options)
			if got := server.Spec.Auth.Audience; got != testCase.want {
				t.Fatalf("auth.audience = %q, want %q", got, testCase.want)
			}
		})
	}
}

// Admission defaulting must not persist derived OAuth values, or a later host,
// path, TLS, or platform-domain change would leave a stale copy in spec.
func TestMCPServerDefaultDoesNotPersistDerivedAuth(t *testing.T) {
	server := &MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "buddy"},
		Spec:       MCPServerSpec{Image: "example.com/buddy", Auth: &AuthConfig{}},
	}
	server.DefaultWithOptions(MCPServerDefaultOptions{
		DefaultIngressHost:    "mcp.example.com",
		DefaultIngressTLS:     true,
		DefaultOAuthIssuerURL: "https://auth.example.com/mcp-auth",
	})
	if server.Spec.Auth.Audience != "" || server.Spec.Auth.IssuerURL != "" {
		t.Fatalf("auth = %+v, want audience and issuer left for reconcile-time derivation", server.Spec.Auth)
	}
	if err := server.validate(); err != nil {
		t.Fatalf("admission must accept an OAuth server whose audience is derived later: %v", err)
	}
}

func TestMCPServerValidateUnderivableOAuthAudience(t *testing.T) {
	server := &MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "buddy"},
		Spec: MCPServerSpec{
			Image: "example.com/buddy",
			Auth:  &AuthConfig{IssuerURL: "https://auth.example.com/mcp-auth"},
		},
	}
	server.DefaultWithOptions(MCPServerDefaultOptions{})
	server.ResolveDerivedAuth(MCPServerDefaultOptions{})

	err := server.ValidateResolvedAuth()
	if err == nil {
		t.Fatal("expected validation error when the audience cannot be derived")
	}
	if !strings.Contains(err.Error(), "auth.audience") || !strings.Contains(err.Error(), "MCP_DEFAULT_INGRESS_HOST") {
		t.Fatalf("expected an auth.audience error naming how to derive it, got %v", err)
	}
}

func TestProtectedResourceMetadataURL(t *testing.T) {
	for resource, want := range map[string]string{
		"https://mcp.example.com/buddy/mcp":  "https://mcp.example.com/.well-known/oauth-protected-resource/buddy/mcp",
		"https://mcp.example.com/buddy/mcp/": "https://mcp.example.com/.well-known/oauth-protected-resource/buddy/mcp",
		"http://localhost:18080":             "http://localhost:18080/.well-known/oauth-protected-resource",
		"":                                   "",
		"/buddy/mcp":                         "",
	} {
		if got := ProtectedResourceMetadataURL(resource); got != want {
			t.Errorf("ProtectedResourceMetadataURL(%q) = %q, want %q", resource, got, want)
		}
	}
}
