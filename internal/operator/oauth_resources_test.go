package operator

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
)

const testBundledIssuer = "https://auth.example.com/mcp-auth"

func bundledOAuthScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{mcpv1alpha1.AddToScheme, appsv1.AddToScheme, corev1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatalf("scheme: %v", err)
		}
	}
	return scheme
}

func bundledAuthDeployment() *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: bundledOAuthDeployment, Namespace: bundledOAuthNamespace},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "mcp-auth-server", Env: []corev1.EnvVar{{Name: "MCP_AUTH_RESOURCES", Value: "stale"}}}},
		}}},
	}
}

func oauthMCPServer(name, audience string) *mcpv1alpha1.MCPServer {
	return &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "mcp-servers"},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:            "example.com/" + name,
			PublicPathPrefix: name,
			Auth:             &mcpv1alpha1.AuthConfig{Mode: mcpv1alpha1.AuthModeOAuth, IssuerURL: testBundledIssuer, Audience: audience},
		},
	}
}

func bundledAuthEnv(t *testing.T, c client.Client) map[string]string {
	t.Helper()
	var deployment appsv1.Deployment
	if err := c.Get(context.Background(), types.NamespacedName{Name: bundledOAuthDeployment, Namespace: bundledOAuthNamespace}, &deployment); err != nil {
		t.Fatalf("get auth deployment: %v", err)
	}
	env := map[string]string{}
	for _, e := range deployment.Spec.Template.Spec.Containers[0].Env {
		env[e.Name] = e.Value
	}
	return env
}

func TestReconcileBundledOAuthResourcesPublishesOnlyServedAudiences(t *testing.T) {
	scheme := bundledOAuthScheme(t)
	objects := []client.Object{
		bundledAuthDeployment(),
		// Derived audience: the server's own public URL.
		oauthMCPServer("buddy", ""),
		// Explicit audience that still names the server's own route.
		oauthMCPServer("notes", "https://mcp.example.com/notes/mcp"),
		// Tenant-supplied audiences that must not widen what the issuer mints for.
		oauthMCPServer("external", "https://api.attacker.example/notes/mcp"),
		oauthMCPServer("borrowed", "https://mcp.example.com/notes/mcp"),
	}
	other := oauthMCPServer("other-issuer", "")
	other.Spec.Auth.IssuerURL = "https://login.example.org"
	objects = append(objects, other)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	r := MCPServerReconciler{Client: c, Scheme: scheme, OAuthIssuerURL: testBundledIssuer, DefaultIngressHost: "mcp.example.com", DefaultIngressTLS: true}

	if err := r.reconcileBundledOAuthResources(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	env := bundledAuthEnv(t, c)
	if got, want := env["MCP_AUTH_RESOURCES"], "https://mcp.example.com/buddy/mcp,https://mcp.example.com/notes/mcp"; got != want {
		t.Fatalf("MCP_AUTH_RESOURCES = %q, want %q", got, want)
	}
	if env["MCP_AUTH_RESOURCE"] != "https://mcp.example.com/buddy/mcp" {
		t.Fatalf("MCP_AUTH_RESOURCE = %q", env["MCP_AUTH_RESOURCE"])
	}
}

// Local test mode has no default MCP host; audiences live on the issuer host.
func TestReconcileBundledOAuthResourcesAcceptsIssuerHostWithoutMCPHost(t *testing.T) {
	scheme := bundledOAuthScheme(t)
	issuer := "http://localhost:18080/mcp-auth"
	server := oauthMCPServer("oauth-example-go-2025-11-25", "http://localhost:18080/oauth-example-go-2025-11-25/mcp")
	server.Spec.Auth.IssuerURL = issuer
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(bundledAuthDeployment(), server).Build()
	r := MCPServerReconciler{Client: c, Scheme: scheme, OAuthIssuerURL: issuer}

	if err := r.reconcileBundledOAuthResources(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := bundledAuthEnv(t, c)["MCP_AUTH_RESOURCES"]; got != "http://localhost:18080/oauth-example-go-2025-11-25/mcp" {
		t.Fatalf("MCP_AUTH_RESOURCES = %q", got)
	}
}

func TestReconcileBundledOAuthResourcesClearsRemovedServers(t *testing.T) {
	scheme := bundledOAuthScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(bundledAuthDeployment()).Build()
	r := MCPServerReconciler{Client: c, Scheme: scheme, OAuthIssuerURL: testBundledIssuer, DefaultIngressHost: "mcp.example.com", DefaultIngressTLS: true}

	if err := r.reconcileBundledOAuthResources(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	env := bundledAuthEnv(t, c)
	if env["MCP_AUTH_RESOURCES"] != "" {
		t.Fatalf("MCP_AUTH_RESOURCES = %q, want empty after the last server is gone", env["MCP_AUTH_RESOURCES"])
	}
	if _, ok := env["MCP_AUTH_RESOURCE"]; ok {
		t.Fatal("MCP_AUTH_RESOURCE should be removed when no resources remain")
	}
}

func TestReconcileBundledOAuthResourcesNoopWithoutBundledIssuer(t *testing.T) {
	scheme := bundledOAuthScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(bundledAuthDeployment(), oauthMCPServer("buddy", "")).Build()
	r := MCPServerReconciler{Client: c, Scheme: scheme}

	if err := r.reconcileBundledOAuthResources(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := bundledAuthEnv(t, c)["MCP_AUTH_RESOURCES"]; got != "stale" {
		t.Fatalf("MCP_AUTH_RESOURCES = %q, want untouched without a bundled issuer", got)
	}
}

func TestReconcileBundledOAuthResourcesRejectsHTTPAudienceForHTTPSIssuer(t *testing.T) {
	scheme := bundledOAuthScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(bundledAuthDeployment(), oauthMCPServer("buddy", "")).Build()
	// TLS not detected: the derived audience is http:// while the issuer is https.
	r := MCPServerReconciler{Client: c, Scheme: scheme, OAuthIssuerURL: testBundledIssuer, DefaultIngressHost: "mcp.example.com"}

	if err := r.reconcileBundledOAuthResources(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := bundledAuthEnv(t, c)["MCP_AUTH_RESOURCES"]; got != "" {
		t.Fatalf("MCP_AUTH_RESOURCES = %q, want no http resource on an https issuer", got)
	}
}
