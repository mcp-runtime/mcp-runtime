package runtimeapi

import (
	"context"
	"encoding/base64"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	"mcp-runtime/pkg/certauth"
	"mcp-runtime/pkg/identity"
	"mcp-runtime/pkg/k8sclient"
)

func TestValidateAdapterCSRUsesCertauth(t *testing.T) {
	t.Parallel()
	const expected = "spiffe://example.org/ns/team-a/session/session-1"
	_, csrPEM, _, err := certauth.BuildSessionCSR("example.org", "team-a", "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := certauth.ValidateCSRPEM(string(csrPEM), expected); err != nil {
		t.Fatalf("ValidateCSRPEM() error = %v", err)
	}
	if _, err := certauth.ValidateCSRPEM(string(csrPEM), strings.Replace(expected, "session-1", "session-2", 1)); err == nil {
		t.Fatal("ValidateCSRPEM() accepted a CSR for another session")
	}
}

func TestIssueSessionCertificateSubmitsPEMRequest(t *testing.T) {
	// cert-manager's admission webhook rejects a CertificateRequest whose
	// spec.request is not a PEM-encoded CSR, so the submitted request must be
	// PEM (base64 of the PEM block), not raw DER.
	t.Setenv("MCP_MTLS_CLUSTER_ISSUER", "mcp-runtime-ca")

	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{certificateRequestGVR: "CertificateRequestList"})
	var capturedRequest string
	dyn.PrependReactor("create", "certificaterequests", func(action clienttesting.Action) (bool, runtime.Object, error) {
		obj := action.(clienttesting.CreateAction).GetObject().(*unstructured.Unstructured)
		capturedRequest, _, _ = unstructured.NestedString(obj.Object, "spec", "request")
		return false, nil, nil
	})

	svc := &AccessService{k8sClients: &k8sclient.Clients{Dynamic: dyn}}

	const trust, ns, sess = "cluster.local", "mcp-servers", "sess-1"
	_, csrPEM, _, err := certauth.BuildSessionCSR(trust, ns, sess)
	if err != nil {
		t.Fatalf("BuildSessionCSR: %v", err)
	}
	csrDER, err := certauth.ValidateCSRPEM(string(csrPEM), identity.SessionSPIFFEID(trust, ns, sess))
	if err != nil {
		t.Fatalf("ValidateCSRPEM: %v", err)
	}

	// The wait step errors out (no accessMgr / no signer), but the create — and
	// thus our capture — happens first, which is all this test asserts.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _, _ = svc.issueSessionCertificateDER(ctx, ns, sess, csrDER, identity.SessionSPIFFEID(trust, ns, sess), time.Hour)

	if capturedRequest == "" {
		t.Fatal("no CertificateRequest was created")
	}
	decoded, err := base64.StdEncoding.DecodeString(capturedRequest)
	if err != nil {
		t.Fatalf("spec.request is not valid base64: %v", err)
	}
	block, _ := pem.Decode(decoded)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		t.Fatalf("spec.request must be a PEM CERTIFICATE REQUEST, got: %q", string(decoded))
	}
}

func TestAdapterCertificateRequestFailure(t *testing.T) {
	t.Parallel()
	request := &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{
			"conditions": []any{map[string]any{
				"type":    "Ready",
				"status":  "False",
				"reason":  "Denied",
				"message": "issuer rejected the request",
			}},
		},
	}}

	err := adapterCertificateRequestFailure(request)
	if err == nil {
		t.Fatal("expected failed CertificateRequest condition to return an error")
	}
	if !strings.Contains(err.Error(), "Denied") || !strings.Contains(err.Error(), "issuer rejected the request") {
		t.Fatalf("unexpected error: %v", err)
	}
}
