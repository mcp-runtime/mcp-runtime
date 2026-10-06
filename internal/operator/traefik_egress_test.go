package operator

import (
	"context"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
)

func traefikEgressScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, networkingv1.AddToScheme, mcpv1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return scheme
}

func traefikDefaultDeny(namespace string) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "traefik-default-deny", Namespace: namespace},
		Spec: networkingv1.NetworkPolicySpec{
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
		},
	}
}

func egressTestServer(port int32) *mcpv1alpha1.MCPServer {
	return &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "mcp-servers"},
		Spec: mcpv1alpha1.MCPServerSpec{
			Port: 8088, ServicePort: 80,
			Gateway: &mcpv1alpha1.GatewayConfig{Enabled: mcpv1alpha1.BoolPtr(true), Port: port},
		},
	}
}

func getEgressPolicy(t *testing.T, c client.Client, namespace string, server *mcpv1alpha1.MCPServer) (*networkingv1.NetworkPolicy, bool) {
	t.Helper()
	policy := &networkingv1.NetworkPolicy{}
	err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: traefikEgressPolicyName(server.Namespace, server.Name)}, policy)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return policy, true
}

func egressPolicyPorts(policy *networkingv1.NetworkPolicy) []int32 {
	var ports []int32
	for _, rule := range policy.Spec.Egress {
		for _, port := range rule.Ports {
			if port.Protocol == nil || *port.Protocol != corev1.ProtocolTCP || port.Port == nil {
				continue
			}
			ports = append(ports, port.Port.IntVal)
		}
	}
	return ports
}

func TestTraefikEgressAllowsServerPortOutsideStaticList(t *testing.T) {
	ctx := context.Background()
	server := egressTestServer(8101)
	c := fake.NewClientBuilder().WithScheme(traefikEgressScheme(t)).WithObjects(server, traefikDefaultDeny("traefik")).Build()
	r := &MCPServerReconciler{Client: c, Scheme: c.Scheme()}

	if err := r.reconcileTraefikEgress(ctx, server); err != nil {
		t.Fatal(err)
	}
	policy, ok := getEgressPolicy(t, c, "traefik", server)
	if !ok {
		t.Fatal("egress policy not created in the Traefik namespace")
	}
	if got := egressPolicyPorts(policy); !slices.Equal(got, []int32{8101}) {
		t.Fatalf("egress ports = %v, want [8101]", got)
	}
	if policy.Spec.PodSelector.MatchLabels["app"] != "traefik" {
		t.Fatalf("policy must select the Traefik pods: %+v", policy.Spec.PodSelector)
	}
	if !slices.Equal(policy.Spec.PolicyTypes, []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}) {
		t.Fatalf("policy types = %v, want Egress only", policy.Spec.PolicyTypes)
	}
	if len(policy.Spec.Egress) != 1 || len(policy.Spec.Egress[0].To) != 1 {
		t.Fatalf("want one narrow peer: %+v", policy.Spec.Egress)
	}
	peer := policy.Spec.Egress[0].To[0]
	if peer.NamespaceSelector == nil || peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "mcp-servers" {
		t.Fatalf("peer namespace selector = %+v", peer.NamespaceSelector)
	}
	if peer.PodSelector == nil || peer.PodSelector.MatchLabels[LabelApp] != "demo" || len(peer.PodSelector.MatchLabels) != 1 {
		t.Fatalf("peer pod selector = %+v", peer.PodSelector)
	}
	if policy.Labels[labelServerName] != "demo" || policy.Labels[labelServerNamespace] != "mcp-servers" || policy.Labels[LabelManagedBy] != LabelManagedByValue {
		t.Fatalf("policy labels = %v", policy.Labels)
	}
	if len(policy.OwnerReferences) != 0 {
		t.Fatalf("cross-namespace owner references are invalid: %v", policy.OwnerReferences)
	}
}

func TestTraefikEgressUsesConfiguredIngressControllerIdentity(t *testing.T) {
	ctx := context.Background()
	server := egressTestServer(8091)
	k3sLabels := map[string]string{"app.kubernetes.io/name": "traefik", "app.kubernetes.io/instance": "traefik-kube-system"}
	deny := traefikDefaultDeny("kube-system")
	deny.Spec.PodSelector = metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "traefik"}}
	c := fake.NewClientBuilder().WithScheme(traefikEgressScheme(t)).WithObjects(server, deny).Build()
	r := &MCPServerReconciler{Client: c, Scheme: c.Scheme(), IngressControllerNamespace: "kube-system", IngressControllerPodLabels: k3sLabels}

	if err := r.reconcileTraefikEgress(ctx, server); err != nil {
		t.Fatal(err)
	}
	policy, ok := getEgressPolicy(t, c, "kube-system", server)
	if !ok {
		t.Fatal("egress policy not created in the configured ingress controller namespace")
	}
	if len(policy.Spec.PodSelector.MatchLabels) != 2 || policy.Spec.PodSelector.MatchLabels["app.kubernetes.io/instance"] != "traefik-kube-system" {
		t.Fatalf("policy must select the configured Traefik pod labels: %+v", policy.Spec.PodSelector)
	}
}

func TestTraefikEgressKeepsRetainedPortDuringTransition(t *testing.T) {
	ctx := context.Background()
	server := egressTestServer(8101)
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: server.Name, Namespace: server.Namespace},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "http", Port: 80, TargetPort: intstr.FromInt32(8091)}}},
	}
	c := fake.NewClientBuilder().WithScheme(traefikEgressScheme(t)).WithObjects(server, service, traefikDefaultDeny("traefik")).Build()
	r := &MCPServerReconciler{Client: c, Scheme: c.Scheme()}

	if err := r.reconcileTraefikEgress(ctx, server); err != nil {
		t.Fatal(err)
	}
	policy, _ := getEgressPolicy(t, c, "traefik", server)
	if got := egressPolicyPorts(policy); !slices.Equal(got, []int32{8091, 8101}) {
		t.Fatalf("transition egress ports = %v, want retained 8091 and candidate 8101", got)
	}

	// The Service is promoted to the candidate: the retired port is dropped.
	service.Spec.Ports[0].TargetPort = intstr.FromInt32(8101)
	if err := c.Update(ctx, service); err != nil {
		t.Fatal(err)
	}
	if err := r.reconcileTraefikEgress(ctx, server); err != nil {
		t.Fatal(err)
	}
	policy, _ = getEgressPolicy(t, c, "traefik", server)
	if got := egressPolicyPorts(policy); !slices.Equal(got, []int32{8101}) {
		t.Fatalf("promoted egress ports = %v, want [8101]", got)
	}
}

func TestTraefikEgressSkipsUnrestrictedTraefik(t *testing.T) {
	ctx := context.Background()
	server := egressTestServer(8101)
	// An ingress-only policy does not isolate egress, and a managed policy
	// for this server must not count as the restriction itself.
	ingressOnly := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "traefik-allow-ingress", Namespace: "traefik"},
		Spec:       networkingv1.NetworkPolicySpec{PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}},
	}
	stale := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{
		Name: traefikEgressPolicyName(server.Namespace, server.Name), Namespace: "traefik",
		Labels: traefikEgressLabels(server.Namespace, server.Name),
	}, Spec: networkingv1.NetworkPolicySpec{PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}}}
	c := fake.NewClientBuilder().WithScheme(traefikEgressScheme(t)).WithObjects(server, ingressOnly, stale).Build()
	r := &MCPServerReconciler{Client: c, Scheme: c.Scheme()}

	if err := r.reconcileTraefikEgress(ctx, server); err != nil {
		t.Fatal(err)
	}
	if _, ok := getEgressPolicy(t, c, "traefik", server); ok {
		t.Fatal("egress policy must not isolate a Traefik whose egress is unrestricted")
	}
}

func TestTraefikEgressSkipsMissingIngressControllerNamespace(t *testing.T) {
	server := egressTestServer(8101)
	c := fake.NewClientBuilder().WithScheme(traefikEgressScheme(t)).WithObjects(server).Build()
	r := &MCPServerReconciler{Client: c, Scheme: c.Scheme()}
	if err := r.reconcileTraefikEgress(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	if _, ok := getEgressPolicy(t, c, "traefik", server); ok {
		t.Fatal("no policy expected without a restricted Traefik namespace")
	}
}

func TestTraefikEgressDeletedForOtherIngressClass(t *testing.T) {
	ctx := context.Background()
	server := egressTestServer(8101)
	c := fake.NewClientBuilder().WithScheme(traefikEgressScheme(t)).WithObjects(server, traefikDefaultDeny("traefik")).Build()
	r := &MCPServerReconciler{Client: c, Scheme: c.Scheme()}
	if err := r.reconcileTraefikEgress(ctx, server); err != nil {
		t.Fatal(err)
	}
	server.Spec.IngressClass = "nginx"
	if err := r.reconcileTraefikEgress(ctx, server); err != nil {
		t.Fatal(err)
	}
	if _, ok := getEgressPolicy(t, c, "traefik", server); ok {
		t.Fatal("policy must be removed when the server stops routing through Traefik")
	}
}

func TestReconcileDeletesTraefikEgressForDeletedServer(t *testing.T) {
	ctx := context.Background()
	server := egressTestServer(8101)
	other := egressTestServer(8101)
	other.Name = "other"
	managed := func(s *mcpv1alpha1.MCPServer) *networkingv1.NetworkPolicy {
		return &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{
			Name: traefikEgressPolicyName(s.Namespace, s.Name), Namespace: "traefik",
			Labels: traefikEgressLabels(s.Namespace, s.Name),
		}}
	}
	c := fake.NewClientBuilder().WithScheme(traefikEgressScheme(t)).WithObjects(other, managed(server), managed(other)).Build()
	r := &MCPServerReconciler{Client: c, Scheme: c.Scheme()}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: server.Namespace, Name: server.Name}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := getEgressPolicy(t, c, "traefik", server); ok {
		t.Fatal("deleted server's egress policy must be removed")
	}
	if _, ok := getEgressPolicy(t, c, "traefik", other); !ok {
		t.Fatal("another server's egress policy must be kept")
	}
}

func TestSweepTraefikEgressPoliciesRemovesOrphans(t *testing.T) {
	ctx := context.Background()
	live := egressTestServer(8101)
	terminating := egressTestServer(8101)
	terminating.Name = "terminating"
	now := metav1.Now()
	terminating.DeletionTimestamp = &now
	terminating.Finalizers = []string{"test/keep"}
	gone := egressTestServer(8101)
	gone.Name = "gone"
	managed := func(namespace string, s *mcpv1alpha1.MCPServer) *networkingv1.NetworkPolicy {
		return &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{
			Name: traefikEgressPolicyName(s.Namespace, s.Name), Namespace: namespace,
			Labels: traefikEgressLabels(s.Namespace, s.Name),
		}}
	}
	unmanaged := traefikDefaultDeny("traefik")
	c := fake.NewClientBuilder().WithScheme(traefikEgressScheme(t)).WithObjects(
		live, terminating, unmanaged,
		managed("traefik", live), managed("traefik", terminating), managed("traefik", gone),
		managed("old-traefik", live),
	).Build()
	r := &MCPServerReconciler{Client: c, Scheme: c.Scheme()}

	if err := r.sweepTraefikEgressPolicies(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := getEgressPolicy(t, c, "traefik", live); !ok {
		t.Fatal("live server's policy must be kept")
	}
	for _, check := range []struct {
		namespace string
		server    *mcpv1alpha1.MCPServer
	}{{"traefik", terminating}, {"traefik", gone}, {"old-traefik", live}} {
		if _, ok := getEgressPolicy(t, c, check.namespace, check.server); ok {
			t.Fatalf("orphan policy for %s in %s must be removed", check.server.Name, check.namespace)
		}
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(unmanaged), &networkingv1.NetworkPolicy{}); err != nil {
		t.Fatalf("unmanaged policy must be kept: %v", err)
	}
}

func TestTraefikEgressPolicyNameIsUniqueAndValid(t *testing.T) {
	a := traefikEgressPolicyName("a-b", "c")
	b := traefikEgressPolicyName("a", "b-c")
	if a == b {
		t.Fatalf("dashed namespace/name pairs collide: %s", a)
	}
	long := traefikEgressPolicyName(strings.Repeat("n", 63), strings.Repeat("s", 253))
	if len(long) > 253 || strings.Contains(long, "--") {
		t.Fatalf("invalid long name (%d): %s", len(long), long)
	}
	if !strings.HasPrefix(a, traefikEgressPolicyPrefix) {
		t.Fatalf("name %q lacks prefix", a)
	}
}

func TestRequestsForTraefikEgressPolicy(t *testing.T) {
	ctx := context.Background()
	one, two := egressTestServer(8101), egressTestServer(8101)
	two.Name, two.Namespace = "two", "team-a"
	c := fake.NewClientBuilder().WithScheme(traefikEgressScheme(t)).WithObjects(one, two).Build()
	r := &MCPServerReconciler{Client: c, Scheme: c.Scheme()}

	managed := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "traefik", Labels: traefikEgressLabels("team-a", "two")}}
	if got := r.requestsForTraefikEgressPolicy(ctx, managed); len(got) != 1 || got[0].Name != "two" || got[0].Namespace != "team-a" {
		t.Fatalf("managed policy requests = %v", got)
	}
	if got := r.requestsForTraefikEgressPolicy(ctx, traefikDefaultDeny("traefik")); len(got) != 2 {
		t.Fatalf("Traefik namespace policy change must enqueue every server, got %v", got)
	}
	if got := r.requestsForTraefikEgressPolicy(ctx, traefikDefaultDeny("mcp-servers")); len(got) != 0 {
		t.Fatalf("unrelated namespace policy must not enqueue servers, got %v", got)
	}
}
