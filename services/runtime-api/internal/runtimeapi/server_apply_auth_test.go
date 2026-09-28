package runtimeapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
)

// A deploy whose metadata has no auth.audience must not persist one: the
// operator derives audience and issuer on every reconcile
// (MCPServer.ResolveDerivedAuth), so later host/path/domain changes re-derive.
// An update also clears a derived copy an older admission webhook persisted.
func TestRuntimeServerApplyDoesNotPersistDerivedOAuthAudience(t *testing.T) {
	t.Setenv("PLATFORM_MODE", "tenant")
	t.Setenv("PLATFORM_TEAM_TRAEFIK_WATCH", "disabled")
	t.Setenv("MCP_REGISTRY_INGRESS_HOST", "registry.example.com")

	stale := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "buddy", Namespace: "mcp-servers"},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image: "registry.example.com/acme/buddy",
			Auth: &mcpv1alpha1.AuthConfig{
				Audience:  "https://mcp.example.com/buddy/mcp",
				IssuerURL: "https://auth.example.com/mcp-auth",
			},
		},
	}
	server := newRuntimeServerWithMCPServers(t, stale)

	request := httptest.NewRequest(http.MethodPost, "/api/runtime/servers", bytes.NewReader([]byte(`{
		"name": "buddy",
		"namespace": "mcp-servers",
		"update": true,
		"spec": {"image":"registry.example.com/acme/buddy","auth":{}}
	}`)))
	request = request.WithContext(withPrincipal(request.Context(), principal{Role: roleAdmin, Subject: "admin-1"}))
	recorder := httptest.NewRecorder()
	server.HandleRuntimeServers(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}

	persisted, err := server.controlPlane().GetServer(context.Background(), "mcp-servers", "buddy")
	if err != nil {
		t.Fatalf("GetServer: %v", err)
	}
	if persisted.Spec.Auth == nil {
		t.Fatal("auth config was not persisted")
	}
	if persisted.Spec.Auth.Audience != "" || persisted.Spec.Auth.IssuerURL != "" {
		t.Fatalf("persisted auth = %+v, want audience and issuer left for reconcile-time derivation", persisted.Spec.Auth)
	}
}
