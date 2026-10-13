package update

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"mcp-runtime/internal/platformrelease"
)

func TestEnsureArgs(t *testing.T) {
	tests := []struct {
		name    string
		current []string
		want    []string
		out     []string
		changed bool
	}{
		{"appends a missing flag", []string{"--a=1"}, []string{"--b=2"}, []string{"--a=1", "--b=2"}, true},
		{"is idempotent", []string{"--a=1", "--b=2"}, []string{"--b=2"}, []string{"--a=1", "--b=2"}, false},
		{"replaces a single-valued flag", []string{"--tracing.sampleRate=0.1", "--x"}, []string{"--tracing.sampleRate=1"}, []string{"--tracing.sampleRate=1", "--x"}, true},
		{"appends a repeatable flag", []string{"--enable-feature=a"}, []string{"--enable-feature=exemplar-storage"}, []string{"--enable-feature=a", "--enable-feature=exemplar-storage"}, true},
		{"keeps unrelated flags", []string{"--keep=1"}, []string{"--add"}, []string{"--keep=1", "--add"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := ensureArgs(tc.current, tc.want)
			if !reflect.DeepEqual(got, tc.out) || changed != tc.changed {
				t.Fatalf("got %v changed=%v, want %v changed=%v", got, changed, tc.out, tc.changed)
			}
		})
	}
}

func TestEnsureDNSOptions(t *testing.T) {
	five := "5"
	got, changed := ensureDNSOptions([]corev1.PodDNSConfigOption{{Name: "ndots", Value: &five}, {Name: "attempts"}},
		[]platformrelease.DNSOption{{Name: "ndots", Value: "3"}})
	if !changed || len(got) != 2 || got[0].Value == nil || *got[0].Value != "3" || got[1].Name != "attempts" {
		t.Fatalf("ndots must be corrected and others kept: %+v", got)
	}
	if _, changed := ensureDNSOptions(got, []platformrelease.DNSOption{{Name: "ndots", Value: "3"}}); changed {
		t.Fatal("second pass must be a no-op")
	}
	added, changed := ensureDNSOptions(nil, []platformrelease.DNSOption{{Name: "ndots", Value: "3"}})
	if !changed || len(added) != 1 {
		t.Fatalf("missing option must be added: %+v", added)
	}
}

// thirdParty returns Deployments for the config-managed third-party components.
func thirdParty() []runtime.Object {
	dep := func(ns, name, container, image string, args []string) *appsv1.Deployment {
		d := deployment(ns, name, container, image, nil)
		d.Spec.Template.Spec.Containers[0].Args = args
		return d
	}
	return []runtime.Object{
		dep("mcp-platform", "mcp-platform-gateway", "traefik", "traefik:v3.0", []string{"--api.insecure=false"}),
		dep("mcp-observability", "prometheus", "prometheus", "prom/prometheus:v2.49.1", []string{"--config.file=/etc/prometheus/prometheus.yml"}),
		dep("mcp-observability", "grafana", "grafana", "grafana/grafana:10.2.3", nil),
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "prometheus-config", Namespace: "mcp-observability"},
			Data: map[string]string{"prometheus.yml": "old", "extra": "keep-me"}},
	}
}

func releaseWithConfig(t *testing.T) *platformrelease.Manifest {
	t.Helper()
	m := manifest(t, "v0.8.0")
	extras := platformrelease.ReleaseExtras{
		ConfigMaps: []platformrelease.ManifestConfigMap{
			{Namespace: "mcp-observability", Name: "prometheus-config", Data: map[string]string{"prometheus.yml": "new", "rules.yml": "rules"}, Restart: []string{"prometheus"}},
			{Namespace: "mcp-observability", Name: "grafana-datasources", Data: map[string]string{"datasources.yaml": "ds"}, Restart: []string{"grafana"}},
		},
		ConfigOnly: []string{"prometheus", "grafana"},
		Wiring: map[string]*platformrelease.ComponentWiring{
			"prometheus": {Args: []string{"--enable-feature=exemplar-storage"}},
			"gateway":    {Args: []string{"--accesslog.format=json"}, DNSOptions: []platformrelease.DNSOption{{Name: "ndots", Value: "3"}}},
		},
	}
	m.Components = append(m.Components, platformrelease.ManifestComponent{Name: "gateway", Repository: "traefik", Tag: "v3.7.14"})
	if err := m.ApplyReleaseExtras(extras); err != nil {
		t.Fatal(err)
	}
	return m
}

func planAndRefine(t *testing.T, cs *fake.Clientset, m *platformrelease.Manifest) *Plan {
	t.Helper()
	plan, err := BuildPlan(context.Background(), cs, m, Selection{})
	if err != nil {
		t.Fatal(err)
	}
	if err := refineObjectPlan(context.Background(), cs, plan); err != nil {
		t.Fatal(err)
	}
	if err := refineConfigMapPlan(context.Background(), cs, plan); err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestPlanConfigOnlyAndConfigMapChanges(t *testing.T) {
	cs := fake.NewSimpleClientset(append(installed("v0.7.2"), thirdParty()...)...)
	plan := planAndRefine(t, cs, releaseWithConfig(t))

	prom := rowFor(t, plan, "prometheus")
	if prom.Action != ActionUpdate || prom.CurrentImage != prom.TargetImage || prom.TargetImage == "" {
		t.Fatalf("prometheus must update without an image change: %+v", prom)
	}
	joined := strings.Join(prom.Wiring, "; ")
	if !strings.Contains(joined, "container args") || !strings.Contains(joined, "config prometheus-config changed") {
		t.Fatalf("prometheus wiring = %q", joined)
	}
	graf := rowFor(t, plan, "grafana")
	if graf.Action != ActionUpdate || !strings.Contains(strings.Join(graf.Wiring, ";"), "config grafana-datasources changed") {
		t.Fatalf("grafana must restart for its ConfigMap: %+v", graf)
	}
	gw := rowFor(t, plan, "gateway")
	if gw.Action != ActionUpdate || !strings.Contains(gw.TargetImage, "v3.7.14") {
		t.Fatalf("gateway image must move to the pinned tag: %+v", gw)
	}
	byKey := map[string]string{}
	for _, c := range plan.ConfigMaps {
		byKey[c.Key] = c.Action
	}
	if byKey["mcp-observability/prometheus-config"] != ObjectActionUpdate || byKey["mcp-observability/grafana-datasources"] != ObjectActionCreate {
		t.Fatalf("configmap actions = %v", byKey)
	}
}

func TestApplyWritesOnlyDeclaredKeysAndRollsDependents(t *testing.T) {
	cs := fake.NewSimpleClientset(append(installed("v0.7.2"), thirdParty()...)...)
	plan := planAndRefine(t, cs, releaseWithConfig(t))
	res := Apply(context.Background(), cs, plan, ApplyOptions{Timeout: time.Second, RollbackOnFailure: true, Waiter: noopWaiter})
	if res.Failed {
		t.Fatalf("apply failed: %+v", res)
	}
	cm, err := cs.CoreV1().ConfigMaps("mcp-observability").Get(context.Background(), "prometheus-config", metav1.GetOptions{})
	if err != nil || cm.Data["prometheus.yml"] != "new" || cm.Data["rules.yml"] != "rules" || cm.Data["extra"] != "keep-me" {
		t.Fatalf("declared keys must be written and others kept: %v %+v", err, cm.Data)
	}
	if _, err := cs.CoreV1().ConfigMaps("mcp-observability").Get(context.Background(), "grafana-datasources", metav1.GetOptions{}); err != nil {
		t.Fatalf("missing ConfigMap must be created: %v", err)
	}
	prom, _ := cs.AppsV1().Deployments("mcp-observability").Get(context.Background(), "prometheus", metav1.GetOptions{})
	if got := prom.Spec.Template.Annotations[configHashAnnotationPrefix+"prometheus-config"]; got == "" {
		t.Fatal("prometheus pod template must carry the config hash so it restarts")
	}
	if args := prom.Spec.Template.Spec.Containers[0].Args; !reflect.DeepEqual(args, []string{"--config.file=/etc/prometheus/prometheus.yml", "--enable-feature=exemplar-storage"}) {
		t.Fatalf("prometheus args = %v", args)
	}
	if prom.Spec.Template.Spec.Containers[0].Image != "prom/prometheus:v2.49.1" {
		t.Fatal("a config-only component must keep its image")
	}
	gw, _ := cs.AppsV1().Deployments("mcp-platform").Get(context.Background(), "mcp-platform-gateway", metav1.GetOptions{})
	if gw.Spec.Template.Spec.DNSConfig == nil || len(gw.Spec.Template.Spec.DNSConfig.Options) != 1 || gw.Spec.Template.Spec.Containers[0].Image != "traefik:v3.7.14" {
		t.Fatalf("gateway must get dns options and the new image: %+v", gw.Spec.Template.Spec)
	}

	// Converged: a second plan has nothing to do for these components.
	again := planAndRefine(t, cs, releaseWithConfig(t))
	for _, name := range []string{"prometheus", "grafana", "gateway"} {
		if r := rowFor(t, again, name); r.Action != ActionUnchanged {
			t.Fatalf("%s should be unchanged after apply, got %s %v", name, r.Action, r.Wiring)
		}
	}
	if len(again.configToWrite) != 0 {
		t.Fatalf("configmaps should be converged, %d still to write", len(again.configToWrite))
	}
}

func TestRollbackRestoresConfigMapAndWorkloadWiring(t *testing.T) {
	cs := fake.NewSimpleClientset(append(installed("v0.7.2"), thirdParty()...)...)
	plan := planAndRefine(t, cs, releaseWithConfig(t))
	waiter := func(_ context.Context, ns, name string, _ time.Duration) error {
		if name == "grafana" {
			return errors.New("grafana never became ready")
		}
		return nil
	}
	res := Apply(context.Background(), cs, plan, ApplyOptions{Timeout: time.Second, RollbackOnFailure: true, Waiter: waiter})
	if !res.Failed {
		t.Fatal("expected the stuck rollout to fail the update")
	}
	cm, _ := cs.CoreV1().ConfigMaps("mcp-observability").Get(context.Background(), "prometheus-config", metav1.GetOptions{})
	if cm.Data["prometheus.yml"] != "old" {
		t.Fatalf("prometheus-config must be restored, got %q", cm.Data["prometheus.yml"])
	}
	if _, ok := cm.Data["rules.yml"]; ok {
		t.Fatal("a key added by the update must be removed on rollback")
	}
	if cm.Data["extra"] != "keep-me" {
		t.Fatal("unrelated keys must survive rollback")
	}
	if _, err := cs.CoreV1().ConfigMaps("mcp-observability").Get(context.Background(), "grafana-datasources", metav1.GetOptions{}); err == nil {
		t.Fatal("a ConfigMap created by the update must be removed on rollback")
	}
	prom, _ := cs.AppsV1().Deployments("mcp-observability").Get(context.Background(), "prometheus", metav1.GetOptions{})
	if _, ok := prom.Spec.Template.Annotations[configHashAnnotationPrefix+"prometheus-config"]; ok {
		t.Fatal("the restart annotation must be removed on rollback")
	}
	if args := prom.Spec.Template.Spec.Containers[0].Args; !reflect.DeepEqual(args, []string{"--config.file=/etc/prometheus/prometheus.yml"}) {
		t.Fatalf("prometheus args must be restored, got %v", args)
	}
	gw, _ := cs.AppsV1().Deployments("mcp-platform").Get(context.Background(), "mcp-platform-gateway", metav1.GetOptions{})
	if gw.Spec.Template.Spec.DNSConfig != nil || gw.Spec.Template.Spec.Containers[0].Image != "traefik:v3.0" {
		t.Fatalf("gateway must be restored: %+v", gw.Spec.Template.Spec)
	}
}

func TestOnlyScopeSkipsConfigMaps(t *testing.T) {
	cs := fake.NewSimpleClientset(append(installed("v0.7.2"), thirdParty()...)...)
	plan, err := BuildPlan(context.Background(), cs, releaseWithConfig(t), Selection{Only: []string{"ui"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.configMaps) != 0 {
		t.Fatal("--only must skip release ConfigMaps")
	}
	if !strings.Contains(strings.Join(plan.Warnings, " "), "ConfigMaps") {
		t.Fatalf("warnings = %v", plan.Warnings)
	}
}

func TestExpectClusterIDGuard(t *testing.T) {
	cs := fake.NewSimpleClientset(installed("v0.7.2")...)
	m := manifest(t, "v0.7.2")
	d := testDeps(cs, m, nil)
	if _, err := execute(t, d, "--to", "v0.7.2", "--dry-run", "--expect-cluster-id", "someone-elses-cluster"); err == nil || !strings.Contains(err.Error(), "uid-1") {
		t.Fatalf("a mismatched cluster must be refused naming the actual id, got %v", err)
	}
	if _, err := execute(t, d, "--to", "v0.7.2", "--dry-run", "--expect-cluster-id", "uid-1"); err != nil {
		t.Fatalf("matching cluster must proceed: %v", err)
	}
}

// TestRealReleaseConvergesLikeTheProductionGate builds the manifest exactly as
// the release generator does from this repository, applies it to a cluster in
// the previous release's state, and requires that a second plan has nothing
// left to do: the same condition the hosted pipeline asserts after `update`.
func TestRealReleaseConvergesLikeTheProductionGate(t *testing.T) {
	const k8sDir = "../../../k8s"
	wiring, err := platformrelease.LoadWiring("../../../config/release/wiring.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m := manifest(t, "v0.8.0")
	objects, err := platformrelease.BundlePlatformObjects(k8sDir, wiring.PlatformObjects)
	if err != nil {
		t.Fatal(err)
	}
	configMaps, err := platformrelease.BundleConfigMaps(k8sDir, wiring.ConfigMaps)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range wiring.ThirdPartyImages {
		repository, tag, err := platformrelease.K8sDeploymentImage(k8sDir, name)
		if err != nil {
			t.Fatal(err)
		}
		m.Components = append(m.Components, platformrelease.ManifestComponent{Name: name, Repository: repository, Tag: tag})
	}
	if err := m.ApplyReleaseExtras(platformrelease.ReleaseExtras{PlatformObjects: objects, ConfigMaps: configMaps, Wiring: wiring.Components, ConfigOnly: wiring.ConfigOnly}); err != nil {
		t.Fatal(err)
	}

	cs := fake.NewSimpleClientset(append(installedWithIngestEnv("v0.7.2"), thirdParty()...)...)
	first := planAndRefine(t, cs, m)
	if !first.NeedsApply() {
		t.Fatal("the new release must have work to do against the previous release")
	}
	res := Apply(context.Background(), cs, first, ApplyOptions{Timeout: time.Second, RollbackOnFailure: true, Waiter: noopWaiter})
	if res.Failed {
		t.Fatalf("apply failed: %+v", res)
	}

	second := planAndRefine(t, cs, m)
	for _, row := range second.Rows {
		if row.Action == ActionUpdate || row.Action == ActionBlocked {
			t.Errorf("%s still %s after apply: %v %s", row.Component, row.Action, row.Wiring, row.Reason)
		}
	}
	if len(second.objects) != 0 || len(second.configToWrite) != 0 {
		t.Fatalf("objects/configmaps not converged: %d objects, %d configmaps", len(second.objects), len(second.configToWrite))
	}
}
