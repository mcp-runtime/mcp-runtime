package runtimeapi

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"mcp-runtime/pkg/k8sclient"
	"mcp-runtime/pkg/kubeworkload"
)

func TestManagedNamespaceRejectsInfrastructureBeforeWriting(t *testing.T) {
	for _, ns := range []string{"default", "kube-node-lease", "mcp-runtime", "mcp-platform", "mcp-observability", "mcp-log-collector", "cert-manager", "registry", "traefik"} {
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

func TestAdminServerApplyPreparesSharedCatalogNamespace(t *testing.T) {
	t.Setenv("PLATFORM_MODE", "public")
	t.Setenv("PLATFORM_TEAM_TRAEFIK_WATCH", "disabled")
	clearRegistryPullSecretEnv(t)
	for _, namespace := range []string{defaultPublicCatalogNamespace, defaultOrgCatalogNamespace} {
		client := fake.NewSimpleClientset()
		server := &RuntimeServer{k8sClients: &k8sclient.Clients{Clientset: client}}
		ctx := context.Background()
		if err := server.ensureServerApplyNamespace(ctx, principal{Role: roleAdmin}, namespace, principalTeam{}, false); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{kubeworkload.OperatorSecretAccessName, kubeworkload.OperatorWorkloadAccessName} {
			binding, err := client.RbacV1().RoleBindings(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil || binding.RoleRef.Name != name || binding.RoleRef.Kind != "ClusterRole" || len(binding.Subjects) != 1 || binding.Subjects[0].Namespace != "mcp-runtime" {
				t.Fatalf("admin apply omitted scoped operator access in %s: %+v %v", namespace, binding, err)
			}
		}
		policies, err := client.NetworkingV1().NetworkPolicies(namespace).List(ctx, metav1.ListOptions{})
		if err != nil || len(policies.Items) == 0 {
			t.Fatalf("admin apply omitted namespace network protection: %v", err)
		}
		quotas, err := client.CoreV1().ResourceQuotas(namespace).List(ctx, metav1.ListOptions{})
		if err != nil || len(quotas.Items) == 0 {
			t.Fatalf("admin apply omitted resource quota: %v", err)
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
