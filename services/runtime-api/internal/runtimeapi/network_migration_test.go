package runtimeapi

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"mcp-runtime-api/internal/runtimeapi/k8sprovision"
	"mcp-runtime/pkg/k8sclient"
)

func TestAPIRolloutReconcilesAllManagedNamespacePolicies(t *testing.T) {
	var objects []runtime.Object
	for _, name := range []string{"mcp-team-example", "mcp-servers-org", "mcp-servers-public", "user-example"} {
		objects = append(objects, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{platformManagedLabel: "true"}}})
		old := k8sprovision.DefaultDenyNetworkPolicy(name)
		old.Spec.Ingress = []networkingv1.NetworkPolicyIngressRule{{}}
		objects = append(objects, old)
	}
	objects = append(objects, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "unmanaged"}})
	client := fake.NewSimpleClientset(objects...)
	server := &RuntimeServer{k8sClients: &k8sclient.Clients{Clientset: client}}
	if err := server.Deployments().ReconcileTeamNamespaceNetworkPolicies(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"mcp-team-example", "mcp-servers-org", "mcp-servers-public", "user-example"} {
		policy, err := client.NetworkingV1().NetworkPolicies(name).Get(context.Background(), "platform-default-deny", metav1.GetOptions{})
		if err != nil || len(policy.Spec.Ingress) != 0 {
			t.Fatalf("%s retained broad policy: %v %#v", name, err, policy)
		}
		if _, err := client.NetworkingV1().NetworkPolicies(name).Get(context.Background(), "platform-generic-workload-ingress", metav1.GetOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if list, err := client.NetworkingV1().NetworkPolicies("unmanaged").List(context.Background(), metav1.ListOptions{}); err != nil || len(list.Items) != 0 {
		t.Fatalf("unmanaged namespace mutated: %v", err)
	}
}

func TestNetworkPolicyMigrationReportsFailedWrites(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team", Labels: map[string]string{platformManagedLabel: "true"}}})
	blocked := errors.New("network policy denied")
	client.PrependReactor("create", "networkpolicies", func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, blocked })
	server := &RuntimeServer{k8sClients: &k8sclient.Clients{Clientset: client}}
	if err := server.Deployments().ReconcileTeamNamespaceNetworkPolicies(context.Background()); !errors.Is(err, blocked) {
		t.Fatalf("failed migration reported success: %v", err)
	}
}
