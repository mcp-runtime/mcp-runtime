package benchmark

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	operator "mcp-runtime/internal/operator"
)

func BenchmarkReconcile(b *testing.B) {
	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = networkingv1.AddToScheme(scheme)

	replicas := int32(1)
	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "bench-server", Namespace: "default"},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:        "test-image",
			ImageTag:     "latest",
			Port:         8088,
			ServicePort:  80,
			Replicas:     &replicas,
			IngressHost:  "example.com",
			IngressPath:  "/bench-server/mcp",
			IngressClass: "traefik",
		},
	}

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mcpServer).Build()
	// Gateway defaults on when Spec.Gateway is omitted; the reconciler needs a
	// proxy image the same way production sets MCP_GATEWAY_PROXY_IMAGE.
	reconciler := &operator.MCPServerReconciler{
		Client:            client,
		Scheme:            scheme,
		GatewayProxyImage: "example.com/mcp-gateway:bench",
	}
	ctx := log.IntoContext(context.Background(), logr.Discard())
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "bench-server", Namespace: "default"}}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := reconciler.Reconcile(ctx, req); err != nil {
			b.Fatalf("reconcile failed: %v", err)
		}
	}
}
