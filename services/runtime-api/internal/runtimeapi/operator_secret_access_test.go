package runtimeapi

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"mcp-runtime/pkg/k8sclient"
)

func TestManagedNamespaceRejectsInfrastructureBeforeWriting(t *testing.T) {
	for _, ns := range []string{"default", "kube-node-lease", "mcp-runtime", "mcp-sentinel", "mcp-platform", "mcp-observability", "mcp-log-collector", "cert-manager", "registry", "traefik"} {
		client := fake.NewSimpleClientset()
		service := &DeploymentService{k8sClients: &k8sclient.Clients{Clientset: client}}
		if err := service.ensureManagedNamespace(context.Background(), ns, nil, managedNamespaceOptions{}); err == nil {
			t.Errorf("accepted reserved namespace %q", ns)
		}
		if len(client.Actions()) != 0 {
			t.Errorf("modified reserved namespace %q: %+v", ns, client.Actions())
		}
	}
}

func TestManagedNamespaceHasOperatorSecretBinding(t *testing.T) {
	client := fake.NewSimpleClientset()
	service := &DeploymentService{k8sClients: &k8sclient.Clients{Clientset: client}}
	if err := service.ensureManagedNamespace(context.Background(), "mcp-team-acme", nil, managedNamespaceOptions{}); err != nil {
		t.Fatal(err)
	}
	binding, err := client.RbacV1().RoleBindings("mcp-team-acme").Get(context.Background(), operatorNamespaceSecretAccessName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if binding.RoleRef.Name != operatorNamespaceSecretAccessName || binding.RoleRef.Kind != "ClusterRole" || binding.Subjects[0].Namespace != "mcp-runtime" {
		t.Fatalf("bad scoped grant: %+v", binding)
	}
}
