package kubeworkload

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestOperatorSecretAccessReservedNamespaces(t *testing.T) {
	for _, ns := range []string{"", "default", "kube-system", "kube-node-lease", "kube-public", "mcp-runtime", "mcp-sentinel", "mcp-platform", "mcp-observability", "mcp-log-collector", "cert-manager", "registry", "traefik"} {
		client := fake.NewSimpleClientset()
		if err := EnsureOperatorSecretAccess(context.Background(), client, ns); err == nil {
			t.Errorf("accepted %q", ns)
		}
		if len(client.Actions()) != 0 {
			t.Errorf("mutated reserved namespace %q", ns)
		}
	}
}

func TestOperatorSecretBindingIsScopedAndIdempotent(t *testing.T) {
	client := fake.NewSimpleClientset()
	for range 2 {
		if err := EnsureOperatorSecretAccess(context.Background(), client, "mcp-team-acme"); err != nil {
			t.Fatal(err)
		}
	}
	binding, err := client.RbacV1().RoleBindings("mcp-team-acme").Get(context.Background(), OperatorSecretAccessName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if binding.RoleRef.Kind != "ClusterRole" || binding.RoleRef.Name != OperatorSecretAccessName || len(binding.Subjects) != 1 || binding.Subjects[0].Namespace != OperatorNamespace {
		t.Fatalf("bad binding: %+v", binding)
	}
	binding.RoleRef.Name = "unexpected"
	if _, err = client.RbacV1().RoleBindings("mcp-team-acme").Update(context.Background(), binding, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = EnsureOperatorSecretAccess(context.Background(), client, "mcp-team-acme"); err == nil {
		t.Fatal("accepted conflicting immutable roleRef")
	}
}

func TestOperatorWorkloadBindingIsNamespaceScoped(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	if err := EnsureOperatorSecretAccess(ctx, client, "mcp-team-acme"); err != nil {
		t.Fatal(err)
	}
	binding, err := client.RbacV1().RoleBindings("mcp-team-acme").Get(ctx, OperatorWorkloadAccessName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if binding.RoleRef.Kind != "ClusterRole" || binding.RoleRef.Name != OperatorWorkloadAccessName || len(binding.Subjects) != 1 || binding.Subjects[0].Namespace != OperatorNamespace {
		t.Fatalf("bad workload binding: %+v", binding)
	}
	binding.RoleRef.Name = "unexpected"
	if _, err = client.RbacV1().RoleBindings("mcp-team-acme").Update(ctx, binding, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = EnsureOperatorSecretAccess(ctx, client, "mcp-team-acme"); err == nil {
		t.Fatal("accepted conflicting workload roleRef")
	}
	denied := fake.NewSimpleClientset()
	if err = EnsureOperatorSecretAccess(ctx, denied, "cert-manager"); err == nil || len(denied.Actions()) != 0 {
		t.Fatalf("cert-manager must receive no workload binding, err=%v actions=%v", err, denied.Actions())
	}
}

func TestTrustBundleGrantCannotReadSigningKeyOrCreateSecrets(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: OperatorTrustBundleName, Namespace: "traefik"}, Data: map[string][]byte{"ca.crt": []byte("public-root")}})
	for range 2 {
		if err := EnsureOperatorTrustBundleAccess(ctx, client, "traefik"); err != nil {
			t.Fatal(err)
		}
	}
	bundle, err := client.CoreV1().Secrets("traefik").Get(ctx, OperatorTrustBundleName, metav1.GetOptions{})
	if err != nil || string(bundle.Data["ca.crt"]) != "public-root" {
		t.Fatalf("existing trust bundle changed: %+v %v", bundle, err)
	}
	role, err := client.RbacV1().Roles("traefik").Get(ctx, "mcp-runtime-operator-trust-bundle", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(role.Rules) != 1 {
		t.Fatalf("bad rules: %+v", role.Rules)
	}
	rule := role.Rules[0]
	if len(rule.ResourceNames) != 1 || rule.ResourceNames[0] != OperatorTrustBundleName {
		t.Fatalf("unnamed access: %+v", rule)
	}
	for _, verb := range rule.Verbs {
		if verb != "get" && verb != "patch" && verb != "update" {
			t.Fatalf("excess verb: %s", verb)
		}
	}
}

func TestTrustBundleBootstrapAndProtectedIssuers(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	if err := EnsureOperatorTrustBundleAccess(ctx, client, "traefik"); err != nil {
		t.Fatal(err)
	}
	bundle, err := client.CoreV1().Secrets("traefik").Get(ctx, OperatorTrustBundleName, metav1.GetOptions{})
	if err != nil || len(bundle.Data) != 0 {
		t.Fatalf("unexpected bootstrap bundle: %+v %v", bundle, err)
	}
	for _, ns := range []string{"cert-manager", "mcp-runtime", "kube-system", "default"} {
		denied := fake.NewSimpleClientset()
		if err := EnsureOperatorTrustBundleAccess(ctx, denied, ns); err == nil || len(denied.Actions()) != 0 {
			t.Fatalf("issuer namespace touched: %s", ns)
		}
	}
	bad := fake.NewSimpleClientset(&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "mcp-runtime-operator-trust-bundle", Namespace: "traefik"}, RoleRef: rbacv1.RoleRef{Kind: "ClusterRole", Name: "cluster-admin"}})
	if err := EnsureOperatorTrustBundleAccess(ctx, bad, "traefik"); err == nil {
		t.Fatal("accepted conflicting trust RoleBinding")
	}
}
