package registry

import (
	"bytes"
	"context"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"mcp-runtime/internal/cli/core"
	"mcp-runtime/pkg/k8sclient"
	"strings"
	"testing"
)

func TestPullNamespaceSelectionDoesNotExpandAfterFirstMatch(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "registry", Namespace: "registry"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "registry", Env: []corev1.EnvVar{{Name: "REGISTRY_AUTH", Value: "token"}}}}}}}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "mcp-platform-api", Namespace: "mcp-platform"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "platform-api", Image: "external.example/platform:review"}}}}}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "registry", Namespace: "registry"}},
		&networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "registry", Namespace: "registry"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mcp-platform"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mcp-team-unselected"}},
	)
	for _, name := range []string{"mcp-platform-api-credentials", "mcp-runtime-api-credentials", "mcp-ui-credentials"} {
		cs.CoreV1().Secrets("mcp-platform").Create(context.Background(), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "mcp-platform"}, Data: map[string][]byte{"API_KEYS": []byte("fixture-admin"), "ADMIN_API_KEYS": []byte("fixture-admin")}}, metav1.CreateOptions{})
	}
	cs.ClearActions()
	var out bytes.Buffer
	old := core.DefaultPrinter
	core.DefaultPrinter = &core.Printer{Writer: &out}
	defer func() { core.DefaultPrinter = old }()
	err := configureNativeAuth(context.Background(), &k8sclient.Clients{Clientset: cs}, nil, nativeAuthOptions{Realm: "https://platform.example/api/v1/registry/token", Namespaces: []string{"mcp-platform"}, DryRun: true})
	output := out.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "in 1 namespaces") {
		t.Fatalf("explicit namespace selection expanded: %s", output)
	}
	for _, a := range cs.Actions() {
		if a.GetVerb() != "get" && a.GetVerb() != "list" {
			t.Fatal("dry run mutated Kubernetes")
		}
	}
}
