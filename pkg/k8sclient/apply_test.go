package k8sclient

import (
	"context"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	discoveryfake "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestApplyManifestYAMLCreatesNamespacedObject(t *testing.T) {
	clients := newApplyTestClients()

	results, err := ApplyManifestYAML(context.Background(), clients, []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
data:
  key: value
`), "mcp-servers")
	if err != nil {
		t.Fatalf("ApplyManifestYAML() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if got, want := results[0].String(), "configmap/demo created"; got != want {
		t.Fatalf("result = %q, want %q", got, want)
	}

	got, err := clients.Dynamic.Resource(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}).
		Namespace("mcp-servers").
		Get(context.Background(), "demo", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get(ConfigMap) error = %v", err)
	}
	if got.GetNamespace() != "mcp-servers" {
		t.Fatalf("namespace = %q, want mcp-servers", got.GetNamespace())
	}
	if value, _, _ := unstructured.NestedString(got.Object, "data", "key"); value != "value" {
		t.Fatalf("data.key = %q, want value", value)
	}
}

func TestApplyManifestYAMLUpdatesAndPreservesExistingLabels(t *testing.T) {
	existing := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata": map[string]any{
			"name":            "mcp-servers",
			"resourceVersion": "1",
			"labels": map[string]any{
				"keep":      "true",
				"overwrite": "old",
			},
		},
	}}
	clients := newApplyTestClients(existing)

	results, err := ApplyManifestYAML(context.Background(), clients, []byte(`apiVersion: v1
kind: Namespace
metadata:
  name: mcp-servers
  labels:
    overwrite: new
`), "")
	if err != nil {
		t.Fatalf("ApplyManifestYAML() error = %v", err)
	}
	if len(results) != 1 || results[0].Action != "configured" {
		t.Fatalf("results = %#v, want one configured result", results)
	}

	got, err := clients.Dynamic.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).
		Get(context.Background(), "mcp-servers", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get(Namespace) error = %v", err)
	}
	labels := got.GetLabels()
	if labels["keep"] != "true" {
		t.Fatalf("keep label = %q, want true", labels["keep"])
	}
	if labels["overwrite"] != "new" {
		t.Fatalf("overwrite label = %q, want new", labels["overwrite"])
	}
}

func TestApplyManifestYAMLRetriesUpdateConflict(t *testing.T) {
	existing := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata": map[string]any{
			"name":            "mcp-servers",
			"resourceVersion": "1",
		},
	}}
	clients := newApplyTestClients(existing)
	dynamicClient := clients.Dynamic.(*dynamicfake.FakeDynamicClient)
	conflicts := 0
	dynamicClient.PrependReactor("update", "namespaces", func(action clienttesting.Action) (bool, runtime.Object, error) {
		if conflicts == 0 {
			conflicts++
			return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "namespaces"}, "mcp-servers", nil)
		}
		return false, nil, nil
	})

	results, err := ApplyManifestYAML(context.Background(), clients, []byte(`apiVersion: v1
kind: Namespace
metadata:
  name: mcp-servers
  labels:
    retry: ok
`), "")
	if err != nil {
		t.Fatalf("ApplyManifestYAML() error = %v", err)
	}
	if conflicts != 1 {
		t.Fatalf("conflicts = %d, want 1", conflicts)
	}
	if len(results) != 1 || results[0].Action != "configured" {
		t.Fatalf("results = %#v, want one configured result", results)
	}
}

func TestApplyManifestYAMLRejectsInvalidManifest(t *testing.T) {
	clients := newApplyTestClients()

	_, err := ApplyManifestYAML(context.Background(), clients, []byte("apiVersion: [\n"), "")
	if err == nil {
		t.Fatal("ApplyManifestYAML() error = nil, want decode error")
	}
	if !strings.Contains(err.Error(), "decode Kubernetes manifest") {
		t.Fatalf("ApplyManifestYAML() error = %v, want decode context", err)
	}
}

func TestApplyManifestYAMLRecreatesStatefulSetOnImmutableUpdate(t *testing.T) {
	existing := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "StatefulSet",
		"metadata": map[string]any{
			"name":            "kafka",
			"namespace":       "mcp-observability",
			"resourceVersion": "1",
		},
		"spec": map[string]any{
			"serviceName": "kafka",
			"replicas":    int64(1),
		},
	}}
	clients := newApplyTestClientsWithApps(existing)
	dynamicClient := clients.Dynamic.(*dynamicfake.FakeDynamicClient)
	deleted := false
	dynamicClient.PrependReactor("update", "statefulsets", func(action clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewInvalid(schema.GroupKind{Group: "apps", Kind: "StatefulSet"}, "kafka", nil)
	})
	dynamicClient.PrependReactor("delete", "statefulsets", func(action clienttesting.Action) (bool, runtime.Object, error) {
		deleted = true
		return false, nil, nil
	})
	dynamicClient.PrependReactor("create", "statefulsets", func(action clienttesting.Action) (bool, runtime.Object, error) {
		obj := action.(clienttesting.CreateAction).GetObject().(*unstructured.Unstructured)
		return true, obj, nil
	})

	results, err := ApplyManifestYAML(context.Background(), clients, []byte(`apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: kafka
  namespace: mcp-observability
spec:
  serviceName: kafka-headless
  podManagementPolicy: Parallel
  replicas: 3
  selector:
    matchLabels:
      app: kafka
  template:
    metadata:
      labels:
        app: kafka
    spec:
      containers:
        - name: kafka
          image: example/kafka:latest
`), "mcp-observability")
	if err != nil {
		t.Fatalf("ApplyManifestYAML() error = %v", err)
	}
	if !deleted {
		t.Fatal("expected StatefulSet delete before recreate")
	}
	if len(results) != 1 || results[0].Action != "recreated" {
		t.Fatalf("results = %#v, want one recreated result", results)
	}
}

func newApplyTestClients(objects ...runtime.Object) *Clients {
	resources := []*metav1.APIResourceList{{
		GroupVersion: "v1",
		APIResources: []metav1.APIResource{
			{Name: "configmaps", Namespaced: true, Kind: "ConfigMap", Verbs: []string{"get", "create", "update"}},
			{Name: "namespaces", Namespaced: false, Kind: "Namespace", Verbs: []string{"get", "create", "update"}},
		},
	}}

	return &Clients{
		Dynamic:   dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), objects...),
		Discovery: &discoveryfake.FakeDiscovery{Fake: &clienttesting.Fake{Resources: resources}},
		Namespace: metav1.NamespaceDefault,
	}
}

func newApplyTestClientsWithApps(objects ...runtime.Object) *Clients {
	resources := []*metav1.APIResourceList{
		{
			GroupVersion: "v1",
			APIResources: []metav1.APIResource{
				{Name: "configmaps", Namespaced: true, Kind: "ConfigMap", Verbs: []string{"get", "create", "update", "delete"}},
				{Name: "namespaces", Namespaced: false, Kind: "Namespace", Verbs: []string{"get", "create", "update", "delete"}},
			},
		},
		{
			GroupVersion: "apps/v1",
			APIResources: []metav1.APIResource{
				{Name: "statefulsets", Namespaced: true, Kind: "StatefulSet", Verbs: []string{"get", "create", "update", "delete"}},
			},
		},
	}

	return &Clients{
		Dynamic:   dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), objects...),
		Discovery: &discoveryfake.FakeDiscovery{Fake: &clienttesting.Fake{Resources: resources}},
		Namespace: metav1.NamespaceDefault,
	}
}
