package operator

import (
	"context"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"strings"
	"testing"
)

func TestBundledOAuthPreservesAdministratorGatewayResources(t *testing.T) {
	scheme := bundledOAuthScheme(t)
	d := bundledAuthDeployment()
	d.Annotations = map[string]string{"mcpruntime.org/auth-gateway-resources": `{"https://auth.example.com/vault-demo/mcp":["tools:read"]}`}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(d, oauthMCPServer("ordinary", "")).Build()
	r := MCPServerReconciler{Client: c, Scheme: scheme, OAuthIssuerURL: testBundledIssuer, DefaultIngressHost: "mcp.example.com", DefaultIngressTLS: true}
	if err := r.reconcileBundledOAuthResources(context.Background()); err != nil {
		t.Fatal(err)
	}
	env := bundledAuthEnv(t, c)
	if !strings.Contains(env["MCP_AUTH_RESOURCES"], "https://auth.example.com/vault-demo/mcp") || !strings.Contains(env["MCP_AUTH_RESOURCES"], "https://mcp.example.com/ordinary/mcp") {
		t.Fatal("administrator gateway registration was erased")
	}
	if !strings.Contains(env["MCP_AUTH_RESOURCE_SCOPES"], "tools:read") {
		t.Fatal("gateway scopes were erased")
	}
}
