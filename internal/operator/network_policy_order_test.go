package operator

import (
	"context"
	"errors"
	"testing"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
)

func TestIngressPolicyFailurePreventsServingWorkloadCreation(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{mcpv1alpha1.AddToScheme, corev1.AddToScheme, appsv1.AddToScheme, networkingv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	server := &mcpv1alpha1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "server", Namespace: "team", UID: "current"}, Spec: mcpv1alpha1.MCPServerSpec{
		Image: "example.com/server:test", Port: 8088, Gateway: &mcpv1alpha1.GatewayConfig{Enabled: mcpv1alpha1.BoolPtr(false)},
	}}
	blocked := errors.New("network policy write rejected")
	workloadCreated := false
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(server).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, object client.Object, options ...client.CreateOption) error {
			if _, ok := object.(*networkingv1.NetworkPolicy); ok {
				return blocked
			}
			if _, ok := object.(*appsv1.Deployment); ok {
				workloadCreated = true
			}
			return c.Create(ctx, object, options...)
		},
	}).Build()
	r := &MCPServerReconciler{Client: c, Scheme: scheme}
	if err := r.reconcileResources(context.Background(), server, logr.Discard()); !errors.Is(err, blocked) {
		t.Fatalf("expected policy rejection, got %v", err)
	}
	if workloadCreated {
		t.Fatal("serving workload was created before its ingress policy")
	}
}
