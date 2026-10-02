package platform

import (
	"context"
	"fmt"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	discoveryfake "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/pkg/k8sclient"
)

var platformIngressGVR = schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}

func swapKubernetesClientsForTest(t *testing.T, clients *k8sclient.Clients) {
	t.Helper()
	orig := newKubernetesClients
	t.Cleanup(func() { newKubernetesClients = orig })
	newKubernetesClients = func() (*k8sclient.Clients, error) {
		return clients, nil
	}
}

// resetPlatformKubeconfig restores platformSetupKubeconfig to "" after the test.
func resetPlatformKubeconfig(t *testing.T) {
	t.Helper()
	orig := platformSetupKubeconfig
	t.Cleanup(func() { platformSetupKubeconfig = orig })
}

func TestPlatformKubernetesClientsUsesExplicitKubeconfig(t *testing.T) {
	resetPlatformKubeconfig(t)
	// When platformSetupKubeconfig is empty the mock via newKubernetesClients must be used.
	called := false
	orig := newKubernetesClients
	t.Cleanup(func() { newKubernetesClients = orig })
	newKubernetesClients = func() (*k8sclient.Clients, error) {
		called = true
		return newPlatformKubernetesTestClients(nil, nil), nil
	}
	platformSetupKubeconfig = ""
	_, _ = platformKubernetesClients()
	if !called {
		t.Fatal("expected newKubernetesClients to be called when platformSetupKubeconfig is empty")
	}
	// When platformSetupKubeconfig is set to a non-empty path the mock must NOT be used
	// (NewWithConfig is called directly). We cannot actually connect to a real cluster
	// in unit tests, so we only verify the mock is bypassed.
	called = false
	platformSetupKubeconfig = "/nonexistent/kubeconfig"
	_, _ = platformKubernetesClients() // will error; that's expected
	if called {
		t.Fatal("expected newKubernetesClients to be bypassed when platformSetupKubeconfig is set")
	}
}

func newPlatformKubernetesTestClients(clientObjects []runtime.Object, dynamicObjects []runtime.Object) *k8sclient.Clients {
	scheme := runtime.NewScheme()
	resources := []*metav1.APIResourceList{
		{
			GroupVersion: "v1",
			APIResources: []metav1.APIResource{
				{Name: "configmaps", Namespaced: true, Kind: "ConfigMap", Verbs: []string{"get", "create", "update"}},
				{Name: "namespaces", Namespaced: false, Kind: "Namespace", Verbs: []string{"get", "create", "update"}},
				{Name: "secrets", Namespaced: true, Kind: "Secret", Verbs: []string{"get", "create", "update"}},
			},
		},
		{
			GroupVersion: "apps/v1",
			APIResources: []metav1.APIResource{
				{Name: "deployments", Namespaced: true, Kind: "Deployment", Verbs: []string{"get", "list", "update", "patch"}},
			},
		},
		{
			GroupVersion: "networking.k8s.io/v1",
			APIResources: []metav1.APIResource{
				{Name: "ingresses", Namespaced: true, Kind: "Ingress", Verbs: []string{"get", "create", "update", "delete"}},
			},
		},
		{
			GroupVersion: "apiextensions.k8s.io/v1",
			APIResources: []metav1.APIResource{
				{Name: "customresourcedefinitions", Namespaced: false, Kind: "CustomResourceDefinition", Verbs: []string{"get", "create", "update"}},
			},
		},
	}
	return &k8sclient.Clients{
		Clientset: kubernetesfake.NewSimpleClientset(clientObjects...),
		Dynamic:   dynamicfake.NewSimpleDynamicClient(scheme, dynamicObjects...),
		Discovery: &discoveryfake.FakeDiscovery{Fake: &clienttesting.Fake{Resources: resources}},
		Namespace: metav1.NamespaceDefault,
	}
}

func platformTestClientsWithTraefikDeployment(namespace string) *k8sclient.Clients {
	return newPlatformKubernetesTestClients([]runtime.Object{
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "traefik", Namespace: namespace}},
	}, nil)
}

func platformTestClientsWithNodeArchitectures(architectures ...string) *k8sclient.Clients {
	objects := make([]runtime.Object, 0, len(architectures))
	for i, architecture := range architectures {
		objects = append(objects, &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("node-%d", i)},
			Status: corev1.NodeStatus{
				NodeInfo: corev1.NodeSystemInfo{Architecture: architecture},
			},
		})
	}
	return newPlatformKubernetesTestClients(objects, nil)
}

func platformTestClientsWithRegistryService(port int32) *k8sclient.Clients {
	return newPlatformKubernetesTestClients([]runtime.Object{
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: core.RegistryServiceName, Namespace: core.NamespaceRegistry},
			Spec: corev1.ServiceSpec{
				ClusterIP: "10.96.201.51",
				Ports: []corev1.ServicePort{
					{Port: port},
				},
			},
		},
	}, nil)
}

func platformTestClientsWithCRD(name string) *k8sclient.Clients {
	return newPlatformKubernetesTestClients(nil, []runtime.Object{
		&unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "apiextensions.k8s.io/v1",
			"kind":       "CustomResourceDefinition",
			"metadata": map[string]any{
				"name": name,
			},
		}},
	})
}

func assertPlatformIngressAppliedForTest(t *testing.T, clients *k8sclient.Clients, host string) {
	t.Helper()
	ingress, err := clients.Dynamic.Resource(platformIngressGVR).
		Namespace(core.ComponentNamespace("platform-api")).
		Get(context.Background(), "mcp-platform-ui", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected platform UI ingress apply: %v", err)
	}
	rules, ok, err := unstructured.NestedSlice(ingress.Object, "spec", "rules")
	if err != nil || !ok || len(rules) == 0 {
		t.Fatalf("expected platform UI ingress rules, ok=%v err=%v object=%v", ok, err, ingress.Object)
	}
	firstRule, ok := rules[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected ingress rule shape: %#v", rules[0])
	}
	if got := firstRule["host"]; got != host {
		t.Fatalf("platform UI ingress host = %v, want %s", got, host)
	}
}

func assertIngressDeletedForTest(t *testing.T, clients *k8sclient.Clients, namespace, name string) {
	t.Helper()
	_, err := clients.Clientset.NetworkingV1().Ingresses(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected ingress %s/%s to be deleted, got err=%v", namespace, name, err)
	}
}
