package platform

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"mcp-runtime/pkg/k8sclient"
)

// Platform API authenticates gateway session sync with in-cluster TokenReview
// and live Pod/ReplicaSet/Deployment/MCPServer reads. Like the runtime API, it
// must be able to reach the Kubernetes API Service IP on CNIs (Cilium, k3s)
// that do not cover cluster-internal addresses with a public 0.0.0.0/0 rule.
func TestKubernetesAPIEgressCoversRuntimeAndPlatformAPI(t *testing.T) {
	client := fake.NewSimpleClientset(
		&networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "mcp-runtime-api-platform-egress", Namespace: "mcp-platform"}},
		&networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "mcp-platform-api-egress", Namespace: "mcp-platform"}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "kubernetes", Namespace: "default"}, Spec: corev1.ServiceSpec{ClusterIP: "10.96.0.1"}},
	)
	previous := newKubernetesClients
	newKubernetesClients = func() (*k8sclient.Clients, error) {
		return &k8sclient.Clients{Clientset: client, Config: &rest.Config{Host: "https://127.0.0.1:45678"}}, nil
	}
	t.Cleanup(func() { newKubernetesClients = previous })
	for range 2 {
		if err := ensureRuntimeKubernetesAPIEgressClientGo(); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"mcp-runtime-api-platform-egress", "mcp-platform-api-egress"} {
		policy, err := client.NetworkingV1().NetworkPolicies("mcp-platform").Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		found := map[string]int{}
		for _, rule := range policy.Spec.Egress {
			for _, peer := range rule.To {
				if peer.IPBlock == nil {
					continue
				}
				for _, p := range rule.Ports {
					found[peer.IPBlock.CIDR+":"+p.Port.String()]++
				}
			}
		}
		for _, want := range []string{"10.96.0.1/32:443", "127.0.0.1/32:45678"} {
			if found[want] != 1 {
				t.Errorf("%s: expected one exact rule for %s, got %v", name, want, found)
			}
		}
	}
}

func TestKubernetesAPIEgressSkipsMissingPlatformPolicy(t *testing.T) {
	client := fake.NewSimpleClientset(
		&networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "mcp-runtime-api-platform-egress", Namespace: "mcp-platform"}},
	)
	previous := newKubernetesClients
	newKubernetesClients = func() (*k8sclient.Clients, error) { return &k8sclient.Clients{Clientset: client}, nil }
	t.Cleanup(func() { newKubernetesClients = previous })
	if err := ensureRuntimeKubernetesAPIEgressClientGo(); err != nil {
		t.Fatal(err)
	}
}
