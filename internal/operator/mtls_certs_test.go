package operator

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
)

func mtlsServer() *mcpv1alpha1.MCPServer {
	return &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "secure-server", Namespace: "mcp-servers"},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:       "example.com/secure-server",
			ServicePort: 80,
			Gateway:     &mcpv1alpha1.GatewayConfig{Enabled: true, Port: 8091, Image: "example.com/gw:latest"},
			Auth:        &mcpv1alpha1.AuthConfig{},
		},
	}
}

func TestTraefikProxySPIFFEID(t *testing.T) {
	r := MCPServerReconciler{AdapterTrustDomain: "example.org"}
	if got := r.traefikProxySPIFFEID(mtlsServer()); got != "spiffe://example.org/ns/traefik/sa/traefik" {
		t.Fatalf("traefikProxySPIFFEID = %q", got)
	}
	emptyReconciler := MCPServerReconciler{}
	if got := emptyReconciler.traefikProxySPIFFEID(mtlsServer()); got != "" {
		t.Fatalf("traefikProxySPIFFEID without trust domain = %q, want empty", got)
	}
}

func TestReconcileTraefikClientCertificateRequiresIssuerAndTrustDomain(t *testing.T) {
	// Without platform PKI the reconcile only cleans up stale resources, so it
	// needs a client that reports them as already gone.
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	newClient := func() client.Client { return fake.NewClientBuilder().WithScheme(scheme).Build() }
	t.Run("missing issuer", func(t *testing.T) {
		r := MCPServerReconciler{Client: newClient(), AdapterTrustDomain: "example.org"} // no MTLSClusterIssuer
		err := r.reconcileTraefikClientCertificate(context.Background(), mtlsServer())
		if err != nil {
			t.Fatalf("without configured platform PKI, expected no-op; got %v", err)
		}
	})
	t.Run("missing trust domain", func(t *testing.T) {
		r := MCPServerReconciler{Client: newClient(), MTLSClusterIssuer: "mcp-runtime-ca"}
		err := r.reconcileTraefikClientCertificate(context.Background(), mtlsServer())
		if err != nil {
			t.Fatalf("without configured platform PKI, expected no-op; got %v", err)
		}
	})
}

func TestReconcileMTLSTrustBundle(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	if err := mcpv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	bundleKey := types.NamespacedName{Name: "secure-server-mtls-ca", Namespace: "mcp-servers"}
	gatewaySecret := func() *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "secure-server-gateway-mtls", Namespace: "mcp-servers"},
			Data:       map[string][]byte{"ca.crt": []byte("CA-PEM"), "tls.crt": []byte("LEAF"), "tls.key": []byte("KEY")},
		}
	}

	t.Run("materializes bundle from gateway ca.crt", func(t *testing.T) {
		server := mtlsServer()
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(server, gatewaySecret()).Build()
		r := MCPServerReconciler{Client: client, Scheme: scheme, AdapterCertificatesEnabled: true, AdapterTrustDomain: "example.org", MTLSClusterIssuer: "mcp-runtime-ca"}
		if err := r.reconcileMTLSTrustBundle(context.Background(), server); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		var bundle corev1.Secret
		if err := client.Get(context.Background(), bundleKey, &bundle); err != nil {
			t.Fatalf("expected bundle secret: %v", err)
		}
		if string(bundle.Data["tls.ca"]) != "CA-PEM" || string(bundle.Data["ca.crt"]) != "CA-PEM" {
			t.Fatalf("bundle data = %v, want CA-PEM under tls.ca and ca.crt", bundle.Data)
		}
	})

	t.Run("skips when gateway certificate not yet issued", func(t *testing.T) {
		server := mtlsServer()
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(server).Build()
		r := MCPServerReconciler{Client: client, Scheme: scheme, AdapterCertificatesEnabled: true, AdapterTrustDomain: "example.org", MTLSClusterIssuer: "mcp-runtime-ca"}
		if err := r.reconcileMTLSTrustBundle(context.Background(), server); err != nil {
			t.Fatalf("reconcile should not error when gateway secret absent: %v", err)
		}
		var bundle corev1.Secret
		if err := client.Get(context.Background(), bundleKey, &bundle); !apierrors.IsNotFound(err) {
			t.Fatalf("expected no bundle yet, got %v", err)
		}
	})

}

func TestGatewaySidecarPinsTrustedProxyForMTLS(t *testing.T) {
	r := MCPServerReconciler{AdapterCertificatesEnabled: true, AdapterTrustDomain: "example.org", MTLSClusterIssuer: "mcp-runtime-ca"}
	container, err := r.buildGatewayContainer(mtlsServer())
	if err != nil {
		t.Fatalf("buildGatewayContainer: %v", err)
	}
	env := map[string]string{}
	for _, e := range container.Env {
		env[e.Name] = e.Value
	}
	if env["TRUSTED_PROXY_SPIFFE_ID"] != "spiffe://example.org/ns/traefik/sa/traefik" {
		t.Fatalf("TRUSTED_PROXY_SPIFFE_ID = %q, want the traefik SPIFFE id", env["TRUSTED_PROXY_SPIFFE_ID"])
	}
	if env["TLS_CLIENT_CA_FILE"] == "" {
		t.Fatal("expected TLS_CLIENT_CA_FILE to be set for mtls gateway")
	}
}

// Adapter certificates reroute a gateway server through an IngressRoute and an
// mTLS gateway hop, so they need the explicit opt-in, Traefik, and a gateway;
// platform PKI being present (as in test mode) is not enough.
func TestUsesAdapterCertificatesRequiresOptInTraefikAndGateway(t *testing.T) {
	pki := MCPServerReconciler{AdapterTrustDomain: "example.org", MTLSClusterIssuer: "mcp-runtime-ca"}
	enabled := pki
	enabled.AdapterCertificatesEnabled = true

	if pki.usesAdapterCertificates(mtlsServer()) {
		t.Fatal("platform PKI alone must not enable adapter certificates")
	}
	if !enabled.usesAdapterCertificates(mtlsServer()) {
		t.Fatal("opt-in with platform PKI should enable adapter certificates for a Traefik gateway server")
	}
	withoutOAuth := mtlsServer()
	withoutOAuth.Spec.Auth = nil
	if !enabled.usesAdapterCertificates(withoutOAuth) {
		t.Fatal("adapter certificates must remain available when OAuth is disabled")
	}
	nginx := mtlsServer()
	nginx.Spec.IngressClass = "nginx"
	if enabled.usesAdapterCertificates(nginx) {
		t.Fatal("non-Traefik ingress classes must keep their plain Ingress")
	}
	standalone := mtlsServer()
	standalone.Spec.Gateway.Enabled = false
	if enabled.usesAdapterCertificates(standalone) {
		t.Fatal("a server without a gateway cannot validate the certificate hop")
	}
}

func TestAdapterCertificateEntryPointsFollowOperatorDefault(t *testing.T) {
	if got := (&MCPServerReconciler{}).adapterCertificateEntryPoints(); len(got) != 1 || got[0] != "websecure" {
		t.Fatalf("entryPoints = %v, want [websecure] fallback", got)
	}
	got := (&MCPServerReconciler{DefaultIngressEntryPoints: "web, websecure"}).adapterCertificateEntryPoints()
	if len(got) != 2 || got[0] != "web" || got[1] != "websecure" {
		t.Fatalf("entryPoints = %v, want the operator's configured entrypoints", got)
	}
}
