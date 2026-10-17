package k8sprovision

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"

	"mcp-runtime/pkg/kubeworkload"
)

func TestNamespaceProvisioningBindsAccessBeforeIssuingCredentials(t *testing.T) {
	ctx := context.Background()
	client := kubernetesfake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mcp-team-acme", Labels: map[string]string{"existing": "kept"}}})
	called := false
	provisioner := Provisioner{Client: client, EnsureRegistryPullSecret: func(ctx context.Context, kube kubernetes.Interface, namespace string) error {
		called = true
		if _, err := kube.CoreV1().ServiceAccounts(namespace).Get(ctx, kubeworkload.DefaultServiceAccountName, metav1.GetOptions{}); err != nil {
			t.Fatalf("credential callback ran before service account: %v", err)
		}
		for _, name := range []string{PlatformAPISecretAccessName, OperatorSecretAccessName} {
			if _, err := kube.RbacV1().RoleBindings(namespace).Get(ctx, name, metav1.GetOptions{}); err != nil {
				t.Fatalf("credential callback ran before %s binding: %v", name, err)
			}
		}
		return nil
	}}
	if err := provisioner.EnsureNamespace(ctx, "mcp-team-acme", map[string]string{"managed": "true"}, "traefik"); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("registry credential callback did not run")
	}
	namespace, err := client.CoreV1().Namespaces().Get(ctx, "mcp-team-acme", metav1.GetOptions{})
	if err != nil || namespace.Labels["existing"] != "kept" || namespace.Labels["managed"] != "true" {
		t.Fatalf("namespace labels not merged: namespace=%v err=%v", namespace, err)
	}
}

func TestNamespaceProvisioningRejectsUnsafeTargetBeforeMutation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		namespace string
		callback  func(context.Context, kubernetes.Interface, string) error
	}{
		{name: "missing credential owner", namespace: "mcp-team-acme"},
		{name: "reserved platform namespace", namespace: "mcp-platform", callback: func(context.Context, kubernetes.Interface, string) error { return nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := kubernetesfake.NewSimpleClientset()
			p := Provisioner{Client: client, EnsureRegistryPullSecret: tc.callback}
			if err := p.EnsureNamespace(context.Background(), tc.namespace, nil); err == nil {
				t.Fatal("unsafe provisioning configuration accepted")
			}
			if len(client.Actions()) != 0 {
				t.Fatalf("unsafe configuration contacted Kubernetes: %v", client.Actions())
			}
		})
	}
}
