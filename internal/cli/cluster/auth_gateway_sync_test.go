package cluster

import (
	"encoding/json"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	"mcp-runtime/pkg/authgateway"
)

const syncBase = `{"listen":{"host":"0.0.0.0","port":8090},"authorization_server":{"issuer":"https://auth.example.com/mcp-auth"},"session_binding_key_file":"/etc/mcp-auth-gateway/session.key"}`

func gatewayServer(name, namespace string) mcpv1alpha1.MCPServer {
	return mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: mcpv1alpha1.MCPServerSpec{
			IngressHost: "mcp.example.com", IngressPath: "/" + name + "/mcp",
			Auth: &mcpv1alpha1.AuthConfig{
				Mode: "header", Headers: []string{"X-GitLab-Username", "X-GitLab-PAT-Token"},
				CredentialGateway: &mcpv1alpha1.CredentialGatewayConfig{ValidationPath: "/auth/check"},
			},
		},
	}
}

func TestExtendGatewayConfigDerivesServicesFromPushedServers(t *testing.T) {
	plain := gatewayServer("plain", "mcp-servers")
	plain.Spec.Auth.CredentialGateway = nil // header mode without the gateway block stays out
	oauth := gatewayServer("oauth", "mcp-servers")
	oauth.Spec.Auth = &mcpv1alpha1.AuthConfig{}
	servers := []mcpv1alpha1.MCPServer{gatewayServer("zeta", "mcp-servers"), plain, oauth, gatewayServer("alpha", "mcp-servers")}

	out, err := extendGatewayConfig([]byte(syncBase), servers, "mcp.example.com")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := authgateway.Parse(out)
	if err != nil {
		t.Fatalf("derived configuration is invalid: %v", err)
	}
	if len(cfg.Services) != 2 || cfg.Services[0].ID != "alpha" || cfg.Services[1].ID != "zeta" {
		t.Fatalf("services = %+v, want alpha then zeta only", cfg.Services)
	}
	var doc map[string]any
	_ = json.Unmarshal(out, &doc)
	if doc["session_binding_key_file"] != "/etc/mcp-auth-gateway/session.key" {
		t.Fatal("the base configuration was not preserved")
	}
}

func TestExtendGatewayConfigKeepsExistingServicesAndRejectsConflicts(t *testing.T) {
	base := strings.Replace(syncBase, `"session_binding_key_file"`, `"services":[{"id":"alpha","resource_url":"https://auth.example.com/x/mcp","upstream_url":"https://u.example.com/mcp","validation_url":"https://u.example.com/check","required_scopes":["tools:read"]}],"session_binding_key_file"`, 1)
	out, err := extendGatewayConfig([]byte(base), []mcpv1alpha1.MCPServer{gatewayServer("beta", "mcp-servers")}, "mcp.example.com")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct{ Services []map[string]any }
	_ = json.Unmarshal(out, &doc)
	if len(doc.Services) != 2 {
		t.Fatalf("services = %d, want the administrator's plus the derived one", len(doc.Services))
	}
	_, err = extendGatewayConfig([]byte(syncBase), []mcpv1alpha1.MCPServer{gatewayServer("alpha", "team-a"), gatewayServer("alpha", "team-b")}, "mcp.example.com")
	if err == nil || !strings.Contains(err.Error(), "share the gateway service id") {
		t.Fatalf("same-named servers must conflict, got %v", err)
	}
	if _, err := extendGatewayConfig([]byte(`{"services":[]}`), nil, ""); err == nil {
		t.Fatal("a base without an issuer must be rejected")
	}
	noHost := gatewayServer("alpha", "mcp-servers")
	noHost.Spec.IngressHost = ""
	if _, err := extendGatewayConfig([]byte(syncBase), []mcpv1alpha1.MCPServer{noHost}, ""); err == nil {
		t.Fatal("a server with no public host must be rejected")
	}
	foreign := gatewayServer("alpha", "mcp-servers")
	foreign.Spec.IngressHost = "evil.example"
	if _, err := extendGatewayConfig([]byte(syncBase), []mcpv1alpha1.MCPServer{foreign}, "mcp.example.com"); err == nil || !strings.Contains(err.Error(), "not the platform MCP host") {
		t.Fatalf("a publisher-chosen host must be rejected, got %v", err)
	}
}
