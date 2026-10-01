package platform

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"mcp-runtime/internal/cli/core"
)

func TestCollectorNamespacePreparationPreservesLegacyAdmission(t *testing.T) {
	for _, policy := range []string{"", "privileged", "restricted"} {
		t.Run(policy, func(t *testing.T) {
			labels := map[string]string{"existing": "keep"}
			if policy != "" {
				labels["pod-security.kubernetes.io/enforce"] = policy
			}
			clients := newPlatformKubernetesTestClients([]runtime.Object{&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: core.ComponentNamespace("platform-api"), Labels: labels}}}, nil)
			swapKubernetesClientsForTest(t, clients)
			if err := ensurePlatformNamespaceBeforeIngress(); err != nil {
				t.Fatal(err)
			}
			ns, err := clients.Clientset.CoreV1().Namespaces().Get(context.Background(), core.ComponentNamespace("platform-api"), metav1.GetOptions{})
			if err != nil || ns.Labels["pod-security.kubernetes.io/enforce"] != policy || ns.Labels["existing"] != "keep" {
				t.Fatalf("source policy changed before replacement: %+v %v", ns, err)
			}
		})
	}
	clients := newPlatformKubernetesTestClients(nil, nil)
	swapKubernetesClientsForTest(t, clients)
	if err := ensurePlatformNamespaceBeforeIngress(); err != nil {
		t.Fatal(err)
	}
	ns, err := clients.Clientset.CoreV1().Namespaces().Get(context.Background(), core.ComponentNamespace("platform-api"), metav1.GetOptions{})
	if err != nil || ns.Labels["pod-security.kubernetes.io/enforce"] != "restricted" {
		t.Fatalf("fresh namespace not restricted: %+v %v", ns, err)
	}
}

func TestDeferredAdmissionManifestRetainsCollectorPolicy(t *testing.T) {
	content := `apiVersion: v1
kind: Namespace
metadata:
  name: mcp-platform
  labels:
    pod-security.kubernetes.io/enforce: restricted
---
apiVersion: v1
kind: Namespace
metadata:
  name: mcp-log-collector
  labels:
    pod-security.kubernetes.io/enforce: privileged
`
	rendered, err := deferPlatformAdmissionManifest(content)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rendered, "mcp-platform") || !strings.Contains(rendered, "mcp-log-collector") || !strings.Contains(rendered, "privileged") {
		t.Fatalf("unsafe cutover manifest: %s", rendered)
	}
}

func TestCollectorFinalizationPreservesDestinationBinding(t *testing.T) {
	ctx := context.Background()
	source := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "promtail"}, RoleRef: rbacv1.RoleRef{Kind: "ClusterRole", Name: "promtail"}, Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: "promtail", Namespace: core.ComponentNamespace("promtail")}}}
	destination := source.DeepCopy()
	destination.Name = "promtail-node-log-collector"
	destination.Subjects[0].Namespace = core.LogCollectorNamespace
	clients := newPlatformKubernetesTestClients([]runtime.Object{source, destination, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: core.ComponentNamespace("platform-api"), Labels: map[string]string{"pod-security.kubernetes.io/enforce": "privileged"}}}}, nil)
	swapKubernetesClientsForTest(t, clients)
	if err := finishCollectorAdmissionCutoverClientGo(); err != nil {
		t.Fatal(err)
	}
	if _, err := clients.Clientset.RbacV1().ClusterRoleBindings().Get(ctx, "promtail", metav1.GetOptions{}); err == nil {
		t.Fatal("source binding retained after cutover")
	}
	if _, err := clients.Clientset.RbacV1().ClusterRoleBindings().Get(ctx, destination.Name, metav1.GetOptions{}); err != nil {
		t.Fatal("destination binding removed:", err)
	}
	ns, _ := clients.Clientset.CoreV1().Namespaces().Get(ctx, core.ComponentNamespace("platform-api"), metav1.GetOptions{})
	if ns.Labels["pod-security.kubernetes.io/enforce"] != "restricted" {
		t.Fatal("source policy not restricted after cutover")
	}
	if err := finishCollectorAdmissionCutoverClientGo(); err != nil {
		t.Fatalf("retry failed: %v", err)
	}
}

func TestCollectorFinalizationRejectsUnrelatedBinding(t *testing.T) {
	clients := newPlatformKubernetesTestClients([]runtime.Object{&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "promtail"}, RoleRef: rbacv1.RoleRef{Kind: "ClusterRole", Name: "custom"}}}, nil)
	swapKubernetesClientsForTest(t, clients)
	if err := finishCollectorAdmissionCutoverClientGo(); err == nil {
		t.Fatal("deleted unrelated binding")
	}
	if _, err := clients.Clientset.RbacV1().ClusterRoleBindings().Get(context.Background(), "promtail", metav1.GetOptions{}); err != nil {
		t.Fatal("custom binding removed")
	}
}
