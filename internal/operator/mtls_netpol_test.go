package operator

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
)

func TestReconcileMTLSNetworkPolicy(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		mcpv1alpha1.AddToScheme, corev1.AddToScheme, networkingv1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			t.Fatalf("add to scheme: %v", err)
		}
	}

	newServer := func() *mcpv1alpha1.MCPServer {
		return &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "secure-server", Namespace: "mcp-servers"},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image:   "example.com/secure-server",
				Gateway: &mcpv1alpha1.GatewayConfig{Enabled: mcpv1alpha1.BoolPtr(true), Port: 8091},
				Auth:    &mcpv1alpha1.AuthConfig{},
			},
		}
	}
	key := types.NamespacedName{Name: "secure-server-mtls-gateway", Namespace: "mcp-servers"}

	t.Run("created and locks the gateway port to traefik for mtls", func(t *testing.T) {
		server := newServer()
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(server).Build()
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test", Client: client, Scheme: scheme, AdapterCertificatesEnabled: true, AdapterTrustDomain: "example.org", MTLSClusterIssuer: "mcp-runtime-ca"}

		if err := r.reconcileMTLSNetworkPolicy(context.Background(), server); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		var np networkingv1.NetworkPolicy
		if err := client.Get(context.Background(), key, &np); err != nil {
			t.Fatalf("expected NetworkPolicy: %v", err)
		}
		if got := np.Spec.PodSelector.MatchLabels["app"]; got != "secure-server" {
			t.Fatalf("podSelector app = %q, want secure-server", got)
		}
		if len(np.Spec.Ingress) != 2 {
			t.Fatalf("ingress rules = %d, want 2 (gateway + metrics)", len(np.Spec.Ingress))
		}
		gw := np.Spec.Ingress[0]
		if len(gw.From) != 1 ||
			gw.From[0].PodSelector == nil || gw.From[0].PodSelector.MatchLabels["app"] != "traefik" ||
			gw.From[0].NamespaceSelector == nil || gw.From[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "traefik" {
			t.Fatalf("gateway rule from = %#v, want traefik ns+pod", gw.From)
		}
		if len(gw.Ports) != 1 || gw.Ports[0].Port == nil || *gw.Ports[0].Port != intstr.FromString("gateway") {
			t.Fatalf("gateway port = %#v, want named gateway listener", gw.Ports)
		}
		// A failed candidate must not change policy to its numeric port and
		// block the retained serving pod's listener.
		server.Spec.Gateway.Port = 8101
		if err := r.reconcileMTLSNetworkPolicy(context.Background(), server); err != nil {
			t.Fatalf("reconcile port transition: %v", err)
		}
		if err := client.Get(context.Background(), key, &np); err != nil {
			t.Fatalf("get policy after port transition: %v", err)
		}
		if got := np.Spec.Ingress[0].Ports; len(got) != 1 || got[0].Port == nil || *got[0].Port != intstr.FromString("gateway") {
			t.Fatalf("port transition replaced per-pod listener with candidate port: %#v", got)
		}
		metrics := np.Spec.Ingress[1]
		if len(metrics.From) != 0 {
			t.Fatalf("metrics rule must be open (no From), got %#v", metrics.From)
		}
		if len(metrics.Ports) != 1 || metrics.Ports[0].Port.IntValue() != DefaultGatewayMetricsPort {
			t.Fatalf("metrics port = %#v, want %d", metrics.Ports, DefaultGatewayMetricsPort)
		}
	})

	t.Run("absent when adapter certificates are disabled", func(t *testing.T) {
		server := newServer()
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(server).Build()
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test", Client: client, Scheme: scheme}

		if err := r.reconcileMTLSNetworkPolicy(context.Background(), server); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		var np networkingv1.NetworkPolicy
		if err := client.Get(context.Background(), key, &np); !apierrors.IsNotFound(err) {
			t.Fatalf("expected NotFound with adapter certificates disabled, got %v", err)
		}
	})
}
