package runtimeapi

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
	mcpaccess "mcp-runtime/pkg/access"
	"mcp-runtime/pkg/certauth"
	"mcp-runtime/pkg/sessionstate"
)

type revocationAfterEnrollmentReadClient struct {
	fakeSessionClient
	gets                    int
	revokedDuringCompletion bool
}

func (c *revocationAfterEnrollmentReadClient) GetSession(_ context.Context, namespace, name string) (sessionstate.Session, bool, error) {
	snapshot := c.row
	c.gets++
	if c.gets == 2 {
		// A completed admin session-revoke lands just after the certificate
		// handler's final read, before its newly introduced annotation update.
		c.row.Binding.Revoked = true
		c.revokedDuringCompletion = true
	}
	return snapshot, snapshot.Namespace == namespace && string(snapshot.Binding.Name) == name, nil
}

func TestAdapterEnrollmentCompletionPreservesConcurrentRevocation(t *testing.T) {
	fx := newAdapterTestFixture(t, adapterTestGrant())
	svc := fx.server.Access()
	dyn := svc.k8sClients.Dynamic.(*dynamicfake.FakeDynamicClient)
	server, err := dyn.Resource(adapterMCPServerGVR).Namespace("mcp-team-acme").Get(t.Context(), "demo", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	server.SetUID("server-uid")
	if _, err := dyn.Resource(adapterMCPServerGVR).Namespace("mcp-team-acme").Update(t.Context(), server, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	session := &mcpaccess.MCPAgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: "review-session", Namespace: "mcp-team-acme", UID: "session-uid", Annotations: map[string]string{
			adapterEnrollmentPendingAnnotation: "true", sessionServerUIDAnnotation: "server-uid",
		}},
		Spec: mcpaccess.MCPAgentSessionSpec{
			ServerRef: mcpaccess.ServerReference{Name: "demo", Namespace: "mcp-team-acme"},
			Subject:   mcpaccess.SubjectRef{HumanID: "user-123", AgentID: "agt_01arz3ndektsv4rrffq69g5fav", TeamID: "team-acme"},
			ExpiresAt: &metav1.Time{Time: time.Now().Add(time.Hour)},
		},
	}
	client := &revocationAfterEnrollmentReadClient{fakeSessionClient: fakeSessionClient{row: sessionToState(session, "server-uid", "team-acme")}}
	svc.accessMgr.WithSessionBackend(&platformSessionBackend{client: client, access: svc.accessMgr})
	_, csrPEM, _, err := certauth.BuildSessionCSR("example.org", session.Namespace, session.Name)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(csrPEM)
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), URIs: csr.URIs, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, template, template, csr.PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	encodedCert := base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	var certificateRequest *unstructured.Unstructured
	dyn.PrependReactor("create", "certificaterequests", func(action clienttesting.Action) (bool, runtime.Object, error) {
		certificateRequest = action.(clienttesting.CreateAction).GetObject().(*unstructured.Unstructured).DeepCopy()
		certificateRequest.SetName("review-certificate")
		certificateRequest.Object["status"] = map[string]any{"certificate": encodedCert}
		return true, certificateRequest, nil
	})
	dyn.PrependReactor("get", "certificaterequests", func(action clienttesting.Action) (bool, runtime.Object, error) {
		return true, certificateRequest, nil
	})
	body, _ := json.Marshal(adapterCertificateRequest{Namespace: session.Namespace, Session: session.Name, CSR: string(csrPEM)})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/adapter/certificates", bytes.NewReader(body))
	req = req.WithContext(withPrincipal(req.Context(), fx.principal))
	out := httptest.NewRecorder()
	svc.HandleAdapterCertificate(out, req)
	if !client.revokedDuringCompletion {
		t.Fatalf("race not reached: status=%d body=%s gets=%d", out.Code, out.Body.String(), client.gets)
	}
	if !client.row.Binding.Revoked {
		t.Fatalf("completed admin revocation was undone by enrollment: HTTP status=%d, final revoked=%t", out.Code, client.row.Binding.Revoked)
	}
	if out.Code < http.StatusBadRequest {
		t.Fatalf("revoked enrollment returned a certificate: status=%d", out.Code)
	}
}
