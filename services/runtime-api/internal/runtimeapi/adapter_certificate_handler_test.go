package runtimeapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	mcpaccess "mcp-runtime/pkg/access"
	"mcp-runtime/pkg/certauth"
	"mcp-runtime/pkg/k8sclient"
)

// A revoked session must not get a fresh certificate, even though the gateway
// would reject its calls anyway: issuing one extends a revoked identity.
func TestHandleAdapterCertificateRefusesRevokedSession(t *testing.T) {
	t.Setenv("MCP_MTLS_CLUSTER_ISSUER", "mcp-runtime-ca")
	scheme := runtime.NewScheme()
	if err := mcpv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	expires := metav1.NewTime(time.Now().Add(time.Hour))
	session := &mcpv1alpha1.MCPAgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: "adapter-1", Namespace: "mcp-team-acme"},
		Spec: mcpv1alpha1.MCPAgentSessionSpec{
			ServerRef: mcpv1alpha1.ServerReference{Name: "demo", Namespace: "mcp-team-acme"},
			Subject:   mcpv1alpha1.SubjectRef{HumanID: "user-123", AgentID: "ops-agent"},
			ExpiresAt: &expires,
			Revoked:   true,
		},
	}
	dyn := dynamicfake.NewSimpleDynamicClient(scheme, session)
	svc := &AccessService{k8sClients: &k8sclient.Clients{Dynamic: dyn}, accessMgr: mcpaccess.NewManager(dyn, nil)}

	_, csrPEM, _, err := certauth.BuildSessionCSR("cluster.local", "mcp-team-acme", "adapter-1")
	if err != nil {
		t.Fatalf("BuildSessionCSR: %v", err)
	}
	body, _ := json.Marshal(map[string]string{"namespace": "mcp-team-acme", "session": "adapter-1", "csr": string(csrPEM)})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/adapter/certificates", bytes.NewReader(body))
	req = req.WithContext(withPrincipal(req.Context(), principal{Subject: "user-123", Role: roleUser}))
	rec := httptest.NewRecorder()

	svc.HandleAdapterCertificate(rec, req)

	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "revoked") {
		t.Fatalf("status = %d body = %s, want 403 revoked", rec.Code, rec.Body.String())
	}
}
