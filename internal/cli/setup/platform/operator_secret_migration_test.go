package platform

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"mcp-runtime/pkg/kubeworkload"
)

func TestOperatorSecretMigrationIncludesEmptyManagedNamespaces(t *testing.T) {
	t.Setenv("MCP_DEFAULT_INGRESS_TLS_SECRET_NAMESPACE", "traefik")
	resetPlatformKubeconfig(t)
	platformSetupKubeconfig = ""
	clients := newPlatformKubernetesTestClients([]runtime.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mcp-team-empty", Labels: map[string]string{"platform.mcpruntime.org/managed": "true"}}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cert-manager", Labels: map[string]string{"platform.mcpruntime.org/managed": "true"}}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "unrelated"}},
	}, nil)
	gvr := schema.GroupVersionResource{Group: "mcpruntime.org", Version: "v1alpha1", Resource: "mcpservers"}
	server := func(ns string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "mcpruntime.org/v1alpha1", "kind": "MCPServer", "metadata": map[string]any{"name": "example", "namespace": ns}}}
	}
	clients.Dynamic = dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "MCPServerList"}, server("mcp-team-existing"), server("mcp-observability"))
	swapKubernetesClientsForTest(t, clients)
	if err := ensureOperatorSecretAccessBindingsClientGo(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, ns := range []string{"mcp-servers", "mcp-team-existing", "mcp-team-empty"} {
		if _, err := clients.Clientset.RbacV1().RoleBindings(ns).Get(ctx, kubeworkload.OperatorSecretAccessName, metav1.GetOptions{}); err != nil {
			t.Errorf("missing grant in %s: %v", ns, err)
		}
	}
	for _, ns := range []string{"cert-manager", "mcp-observability", "unrelated", "traefik"} {
		if _, err := clients.Clientset.RbacV1().RoleBindings(ns).Get(ctx, kubeworkload.OperatorSecretAccessName, metav1.GetOptions{}); err == nil {
			t.Errorf("tenant grant in %s", ns)
		}
	}
	role, err := clients.Clientset.RbacV1().Roles("traefik").Get(ctx, "mcp-runtime-operator-trust-bundle", metav1.GetOptions{})
	if err != nil || len(role.Rules) != 1 || role.Rules[0].ResourceNames[0] != kubeworkload.OperatorTrustBundleName {
		t.Fatalf("missing named public trust grant: %+v %v", role, err)
	}
}
