package platform

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"mcp-runtime/pkg/k8sclient"
)

func TestRuntimeAPIEgressIncludesReadyKubernetesBackends(t *testing.T) {
	port := int32(6443)
	ready, notReady := true, false
	client := fake.NewSimpleClientset(
		&networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "mcp-runtime-api-platform-egress", Namespace: "mcp-platform"}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "kubernetes", Namespace: "default"}, Spec: corev1.ServiceSpec{ClusterIP: "10.96.0.1"}},
		&discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: "kubernetes", Namespace: "default", Labels: map[string]string{discoveryv1.LabelServiceName: "kubernetes"}}, AddressType: discoveryv1.AddressTypeIPv4,
			Ports: []discoveryv1.EndpointPort{{Port: &port}}, Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"172.18.0.2"}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}}, {Addresses: []string{"172.18.0.99"}, Conditions: discoveryv1.EndpointConditions{Ready: &notReady}}}},
		&discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: "default", Labels: map[string]string{discoveryv1.LabelServiceName: "other"}}, AddressType: discoveryv1.AddressTypeIPv4,
			Ports: []discoveryv1.EndpointPort{{Port: &port}}, Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"192.0.2.10"}}}},
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
	policy, err := client.NetworkingV1().NetworkPolicies("mcp-platform").Get(context.Background(), "mcp-runtime-api-platform-egress", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]int{}
	for _, rule := range policy.Spec.Egress {
		for _, peer := range rule.To {
			if peer.IPBlock != nil {
				for _, p := range rule.Ports {
					if p.Protocol == nil || *p.Protocol == corev1.ProtocolTCP {
						found[peer.IPBlock.CIDR+":"+p.Port.String()]++
					}
				}
			}
		}
	}
	for _, want := range []string{"10.96.0.1/32:443", "127.0.0.1/32:45678", "172.18.0.2/32:6443"} {
		if found[want] != 1 {
			t.Errorf("expected one exact TCP rule for %s, got %v", want, found)
		}
	}
	for _, denied := range []string{"172.18.0.99/32:6443", "192.0.2.10/32:6443", "0.0.0.0/0:6443"} {
		if found[denied] != 0 {
			t.Errorf("unexpected egress expansion: %s", denied)
		}
	}
}
