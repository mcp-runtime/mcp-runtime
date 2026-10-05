package operator

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestPortTransitionRetainsLastRouteAndPromotesReadyCandidate(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = mcpv1alpha1.AddToScheme(scheme)
	server := &mcpv1alpha1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "team"}, Spec: mcpv1alpha1.MCPServerSpec{Port: 8081, ServicePort: 80, Gateway: &mcpv1alpha1.GatewayConfig{Enabled: mcpv1alpha1.BoolPtr(true), Port: 8091}}}
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: server.Name, Namespace: server.Namespace}, Spec: corev1.ServiceSpec{ClusterIP: "10.0.0.1", Selector: map[string]string{LabelApp: server.Name, LabelManagedBy: LabelManagedByValue}, Ports: []corev1.ServicePort{{Name: "http", Port: 80, TargetPort: intstr.FromInt(8081)}}}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "candidate", Namespace: server.Namespace, Labels: map[string]string{LabelApp: server.Name, LabelManagedBy: LabelManagedByValue, servingPortLabel: "8091", "mcpruntime.org/rollout-track": "stable"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "gateway", Ports: []corev1.ContainerPort{{ContainerPort: 8091}}}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}}}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(pod).WithObjects(server, service, pod).Build()
	r := &MCPServerReconciler{Client: c, Scheme: scheme}
	for i := 0; i < 2; i++ { // a fresh reconciler simulates restart with no local transition state
		r = &MCPServerReconciler{Client: c, Scheme: scheme}
		strategy, err := r.servingDeploymentStrategy(ctx, server)
		if err != nil || strategy.RollingUpdate == nil || strategy.RollingUpdate.MaxUnavailable.IntVal != 0 || strategy.RollingUpdate.MaxSurge.IntVal != 1 {
			t.Fatalf("unsafe transition strategy: %+v, %v", strategy, err)
		}
		if err := r.reconcileService(ctx, server); err != nil {
			t.Fatal(err)
		}
		if err := c.Get(ctx, types.NamespacedName{Name: service.Name, Namespace: service.Namespace}, service); err != nil {
			t.Fatal(err)
		}
		if service.Spec.Ports[0].TargetPort.IntVal != 8081 || service.Spec.Selector[servingPortLabel] != "" {
			t.Fatalf("failed candidate replaced route: %+v", service.Spec)
		}
	}
	if err := c.Get(ctx, types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, pod); err != nil {
		t.Fatal(err)
	}
	pod.Status.Conditions[0].Status = corev1.ConditionTrue
	if err := c.Status().Update(ctx, pod); err != nil {
		t.Fatal(err)
	}
	if err := r.reconcileService(ctx, server); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, types.NamespacedName{Name: service.Name, Namespace: service.Namespace}, service); err != nil {
		t.Fatal(err)
	}
	if service.Spec.Ports[0].TargetPort.IntVal != 8091 || service.Spec.Selector[servingPortLabel] != "8091" || service.Spec.ClusterIP != "10.0.0.1" {
		t.Fatalf("candidate not safely promoted: %+v", service.Spec)
	}
}

func TestDeploymentRevisionReadinessRejectsOldReplicas(t *testing.T) {
	one := int32(1)
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Generation: 2}, Spec: appsv1.DeploymentSpec{Replicas: &one}, Status: appsv1.DeploymentStatus{ObservedGeneration: 1, Replicas: 1, ReadyReplicas: 1, AvailableReplicas: 1}}
	if deploymentRevisionReady(d, 1) {
		t.Fatal("old generation reported ready")
	}
	d.Status.ObservedGeneration = 2
	if deploymentRevisionReady(d, 1) {
		t.Fatal("old ready replica reported current template ready")
	}
	d.Status.UpdatedReplicas = 1
	if !deploymentRevisionReady(d, 1) {
		t.Fatal("complete current generation not ready")
	}
	d.Status.Replicas = 2
	if deploymentRevisionReady(d, 1) {
		t.Fatal("overlapping old revision reported complete")
	}
}

func TestServiceReadinessRequiresServingEndpointOnDesiredPort(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = discoveryv1.AddToScheme(scheme)
	server := &mcpv1alpha1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "team"}, Spec: mcpv1alpha1.MCPServerSpec{Port: 8081, Gateway: &mcpv1alpha1.GatewayConfig{Enabled: mcpv1alpha1.BoolPtr(true), Port: 8091}}}
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "team"}, Spec: corev1.ServiceSpec{ClusterIP: "10.0.0.1", Ports: []corev1.ServicePort{{Name: "http", TargetPort: intstr.FromInt(8091)}}}}
	name, port, ready := "http", int32(8081), true
	slice := &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: "demo-1", Namespace: "team", Labels: map[string]string{discoveryv1.LabelServiceName: "demo"}}, Ports: []discoveryv1.EndpointPort{{Name: &name, Port: &port}}, Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.1.0.1"}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}}}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(service, slice).Build()
	r := &MCPServerReconciler{Client: c, Scheme: scheme}
	check := func(want bool) {
		t.Helper()
		got, err := r.checkServiceReady(context.Background(), server)
		if err != nil || got != want {
			t.Fatalf("ready=%v want %v: %v", got, want, err)
		}
	}
	check(false)
	slice.Ports[0].Port = new(int32)
	*slice.Ports[0].Port = 8091
	if err := c.Update(context.Background(), slice); err != nil {
		t.Fatal(err)
	}
	check(true)
	slice.Endpoints[0].Conditions.Ready = new(bool)
	if err := c.Update(context.Background(), slice); err != nil {
		t.Fatal(err)
	}
	check(false)
	slice.Endpoints[0].Conditions.Ready = &ready
	slice.Endpoints[0].Conditions.Terminating = &ready
	if err := c.Update(context.Background(), slice); err != nil {
		t.Fatal(err)
	}
	check(false)
}
