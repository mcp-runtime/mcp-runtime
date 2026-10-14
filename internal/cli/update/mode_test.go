package update

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"mcp-runtime/internal/platformrelease"
)

func modeInstall() *fake.Clientset {
	objects := installed("v0.8.1")
	for _, ns := range []string{"mcp-platform", "mcp-observability"} {
		objects = append(objects, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "mcp-shared-config", Namespace: ns},
			Data: map[string]string{"PLATFORM_MODE": "tenant", "UNRELATED": "retain"}})
	}
	return fake.NewSimpleClientset(objects...)
}

func TestModeCommandPreparesCatalogsWithoutImageChanges(t *testing.T) {
	ctx := context.Background()
	cs := modeInstall()
	m := manifest(t, "v0.8.1")
	d := testDeps(cs, m, nil)
	out, err := execute(t, d, "--release-manifest", "candidate.json", "--platform-mode", "public", "--dry-run", "--output", "json")
	if err != nil || !strings.Contains(out, `"catalogNamespaces"`) || !strings.Contains(out, `"mcp-servers-public"`) {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	for _, action := range cs.Actions() {
		if action.GetVerb() != "get" && action.GetVerb() != "list" {
			t.Fatalf("dry-run mutated cluster: %v", action)
		}
	}
	if _, err = execute(t, d, "--release-manifest", "candidate.json", "--platform-mode", "public", "--yes"); err != nil {
		t.Fatal(err)
	}
	for name, scope := range map[string]string{"mcp-servers-public": "public", "mcp-servers-org": "org"} {
		ns, err := cs.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
		if err != nil || ns.Labels["mcpruntime.org/scope"] != scope || ns.Labels["pod-security.kubernetes.io/enforce"] != "restricted" {
			t.Fatalf("catalog %s: %+v %v", name, ns, err)
		}
	}
	for _, ns := range []string{"mcp-platform", "mcp-observability"} {
		cm, _ := cs.CoreV1().ConfigMaps(ns).Get(ctx, "mcp-shared-config", metav1.GetOptions{})
		if cm.Data["PLATFORM_MODE"] != "public" || cm.Data["UNRELATED"] != "retain" {
			t.Fatalf("mode update replaced unrelated config: %v", cm.Data)
		}
	}
	for _, name := range modeDependents {
		component, _ := platformrelease.Lookup(name)
		dep, _ := cs.AppsV1().Deployments(component.Namespace).Get(ctx, component.Deployment, metav1.GetOptions{})
		if len(dep.Spec.Template.Annotations) == 0 || !strings.HasSuffix(dep.Spec.Template.Spec.Containers[0].Image, ":v0.8.1") {
			t.Fatalf("mode-dependent service must restart preserving its image: %+v", dep)
		}
	}
	cs.ClearActions()
	if _, err = execute(t, d, "--release-manifest", "candidate.json", "--platform-mode", "public", "--yes"); err != nil {
		t.Fatal(err)
	}
	for _, action := range cs.Actions() {
		if action.GetVerb() != "get" && action.GetVerb() != "list" {
			t.Fatalf("repeat update must be idempotent: %v", action)
		}
	}
}

func TestModeCommandFailureRestoresConfigRetainsCatalogs(t *testing.T) {
	cs := modeInstall()
	d := testDeps(cs, manifest(t, "v0.8.1"), nil)
	d.waiter = func(_ kubernetes.Interface) RolloutWaiter {
		return func(context.Context, string, string, time.Duration) error { return errors.New("rollout failed") }
	}
	if _, err := execute(t, d, "--release-manifest", "candidate.json", "--platform-mode", "public", "--yes"); err == nil {
		t.Fatal("expected rollout error")
	}
	for _, ns := range []string{"mcp-platform", "mcp-observability"} {
		cm, _ := cs.CoreV1().ConfigMaps(ns).Get(context.Background(), "mcp-shared-config", metav1.GetOptions{})
		if cm.Data["PLATFORM_MODE"] != "tenant" || cm.Data["UNRELATED"] != "retain" {
			t.Fatalf("failed rollout must restore previous configuration: %v", cm.Data)
		}
	}
	if _, err := cs.CoreV1().Namespaces().Get(context.Background(), "mcp-servers-public", metav1.GetOptions{}); err != nil {
		t.Fatal("additive namespaces must survive rollback", err)
	}
}

func TestModeCommandRejectsUnsafeOptionsBeforeClusterAccess(t *testing.T) {
	for _, args := range [][]string{
		{"--platform-mode", "tenant"}, {"--platform-mode", " "}, {"--platform-mode", "public", "--only", "ui"},
	} {
		d := deps{kube: func(string, string) (kubeHandle, error) {
			t.Fatal("invalid options reached cluster")
			return kubeHandle{}, nil
		}}
		if _, err := execute(t, d, append([]string{"--to", "v0.8.1"}, args...)...); err == nil {
			t.Fatalf("expected rejection: %v", args)
		}
	}
}

func TestModeCommandRefusesExistingNamespaceScopeChange(t *testing.T) {
	for _, scope := range []string{"tenant", "org", ""} {
		cs := modeInstall()
		_, _ = cs.CoreV1().Namespaces().Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mcp-servers-public", Labels: map[string]string{
			"mcpruntime.org/scope": scope, "platform.mcpruntime.org/managed": "true",
		}}}, metav1.CreateOptions{})
		cs.ClearActions()
		_, err := execute(t, testDeps(cs, manifest(t, "v0.8.1"), nil), "--release-manifest", "candidate.json", "--platform-mode", "public", "--yes")
		if err == nil || !strings.Contains(err.Error(), "refusing to adopt") {
			t.Fatalf("existing %q namespace must not become anonymous: %v", scope, err)
		}
		for _, action := range cs.Actions() {
			if action.GetVerb() != "get" && action.GetVerb() != "list" {
				t.Fatalf("unsafe mode plan mutated cluster: %v", action)
			}
		}
	}
}

func TestCatalogTargetsUseClusterConfiguration(t *testing.T) {
	t.Setenv("PLATFORM_CATALOG_NAMESPACE", "workstation-namespace")
	for _, tc := range []struct {
		data map[string]string
		want map[string]string
	}{
		{map[string]string{"PLATFORM_MODE": "org"}, map[string]string{"mcp-servers-org": "org"}},
		{map[string]string{"PLATFORM_MODE": "public"}, map[string]string{"mcp-servers-org": "org", "mcp-servers-public": "public"}},
		{map[string]string{"PLATFORM_MODE": "public", "PLATFORM_ORG_NAMESPACE": "company", "PLATFORM_PUBLIC_NAMESPACE": "preview", "PLATFORM_CATALOG_NAMESPACES": "other-preview"}, map[string]string{"company": "org", "preview": "public", "other-preview": "public"}},
	} {
		got, err := catalogTargets(tc.data)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("catalogs %v: got %v %v, want %v", tc.data, got, err, tc.want)
		}
	}
	for _, value := range []string{"mcp-servers-org", "mcp-platform", "registry", "traefik", "default", "kube-system", "bad/name"} {
		if _, err := catalogTargets(map[string]string{"PLATFORM_MODE": "public", "PLATFORM_CATALOG_NAMESPACE": value}); err == nil {
			t.Fatalf("unsafe public catalog accepted: %s", value)
		}
	}
}

func TestModeManifestRepairsMissingCatalogWithoutChangingMode(t *testing.T) {
	cs := modeInstall()
	m := manifest(t, "v0.8.1")
	m.ConfigMaps = []platformrelease.ManifestConfigMap{{Namespace: "mcp-platform", Name: "mcp-shared-config", Data: map[string]string{"PLATFORM_MODE": "public"}}}
	plan, err := BuildPlan(context.Background(), cs, m, Selection{})
	if err != nil || len(plan.namespacesToWrite) != 2 {
		t.Fatalf("release-declared mode must also prepare catalogs: %+v %v", plan, err)
	}
	scoped, err := BuildPlan(context.Background(), cs, m, Selection{Only: []string{"ui"}})
	if err != nil || len(scoped.namespacesToWrite) != 0 {
		t.Fatalf("scoped image update must not prepare catalogs: %+v %v", scoped, err)
	}
	if _, err := cs.CoreV1().Namespaces().Get(context.Background(), "mcp-servers-public", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("planning must not create namespaces")
	}
}

func TestCommitBuildRegistryFailureStopsBeforeBuildOrClusterMutation(t *testing.T) {
	cs := modeInstall()
	tag := "v0.9.0-g" + strings.Repeat("a", 40)
	m := manifest(t, "v0.9.0", setComponent("runtime-api", "mcp-runtime-api", tag, ""))
	d := testDeps(cs, m, nil)
	d.build = BuildOptions{
		RegistryHasImage: func(context.Context, string) (bool, error) { return false, errors.New("401 Unauthorized") },
		BuildImage: func(context.Context, string, string, imageBuildSpec, string) error {
			t.Fatal("must fail before expensive build")
			return nil
		},
	}
	_, err := execute(t, d, "--release-manifest", "candidate.json", "--only", "runtime-api", "--build", "--source", "../../..", "--yes")
	if err == nil || !strings.Contains(err.Error(), "verify Docker registry login") {
		t.Fatalf("expected registry preflight error: %v", err)
	}
	for _, action := range cs.Actions() {
		if action.GetVerb() != "get" && action.GetVerb() != "list" {
			t.Fatalf("registry failure mutated cluster: %v", action)
		}
	}
}
