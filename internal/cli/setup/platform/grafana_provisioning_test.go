package platform

import (
	"os"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	setupplan "mcp-runtime/internal/cli/setup/plan"
	sigsyaml "sigs.k8s.io/yaml"
)

func readRepoConfigMap(t *testing.T, manifest string) corev1.ConfigMap {
	t.Helper()
	raw, err := os.ReadFile("../../../../k8s/" + manifest)
	if err != nil {
		t.Fatalf("read %s: %v", manifest, err)
	}
	var cm corev1.ConfigMap
	if err := sigsyaml.Unmarshal(raw, &cm); err != nil {
		t.Fatalf("parse %s: %v", manifest, err)
	}
	return cm
}

func configMapBytes(cm corev1.ConfigMap) map[string][]byte {
	out := map[string][]byte{}
	for k, v := range cm.Data {
		out[k] = []byte(v)
	}
	return out
}

// renderGrafanaForSetup renders k8s/12-grafana.yaml the way setup applies it:
// image/mode rendering followed by dependency-revision stamping against the
// live provisioning ConfigMaps (here: the given fakes).
func renderGrafanaForSetup(t *testing.T, datasources, dashboards corev1.ConfigMap) appsv1.Deployment {
	t.Helper()
	raw, err := os.ReadFile("../../../../k8s/12-grafana.yaml")
	if err != nil {
		t.Fatalf("read 12-grafana.yaml: %v", err)
	}
	rendered, err := renderAnalyticsManifest(string(raw), AnalyticsImageSet{}, "", setupplan.PlatformModeTenant)
	if err != nil {
		t.Fatalf("renderAnalyticsManifest: %v", err)
	}
	read := func(ns, kind, name string) (map[string][]byte, error) {
		switch {
		case kind == "ConfigMap" && name == "grafana-datasources":
			return configMapBytes(datasources), nil
		case kind == "ConfigMap" && name == "grafana-dashboards":
			return configMapBytes(dashboards), nil
		case kind == "Secret":
			return map[string][]byte{"GRAFANA_ADMIN_USER": []byte("admin"), "GRAFANA_ADMIN_PASSWORD": []byte("unused")}, nil
		}
		t.Fatalf("unexpected dependency %s/%s/%s", ns, kind, name)
		return nil, nil
	}
	stamped, err := stampDependencyRevisions(rendered, read)
	if err != nil {
		t.Fatalf("stampDependencyRevisions: %v", err)
	}
	for _, doc := range strings.Split(stamped, "\n---\n") {
		if !strings.Contains(doc, "kind: Deployment") {
			continue
		}
		var deploy appsv1.Deployment
		if err := sigsyaml.Unmarshal([]byte(doc), &deploy); err != nil {
			t.Fatalf("parse rendered Deployment: %v", err)
		}
		return deploy
	}
	t.Fatalf("rendered 12-grafana.yaml has no Deployment:\n%s", stamped)
	return appsv1.Deployment{}
}

// TestSetupAppliesGrafanaProvisioningBeforeGrafana guards issue #543: setup
// must apply the datasource and dashboard ConfigMaps before the Grafana
// Deployment so the Deployment's dependency revision reflects them.
func TestSetupAppliesGrafanaProvisioningBeforeGrafana(t *testing.T) {
	manifests := analyticsServiceManifests("k8s/20-postgres.yaml")
	index := map[string]int{}
	for i, m := range manifests {
		index[m] = i
	}
	grafana, ok := index["k8s/12-grafana.yaml"]
	if !ok {
		t.Fatal("setup does not apply k8s/12-grafana.yaml")
	}
	for _, dep := range []string{"k8s/19-grafana-datasources.yaml", "k8s/21-grafana-dashboards.yaml"} {
		i, ok := index[dep]
		if !ok {
			t.Errorf("setup does not apply %s", dep)
			continue
		}
		if i > grafana {
			t.Errorf("setup applies %s after k8s/12-grafana.yaml; Grafana would start against stale provisioning", dep)
		}
	}
}

// TestSetupRenderedGrafanaMountsAndRollsOnProvisioningChange checks the
// rendered Deployment keeps every provisioning mount, and that a change to
// the datasource or dashboard ConfigMap changes the pod template so Grafana
// restarts and re-reads provisioning (datasources load only at startup).
func TestSetupRenderedGrafanaMountsAndRollsOnProvisioningChange(t *testing.T) {
	datasources := readRepoConfigMap(t, "19-grafana-datasources.yaml")
	dashboards := readRepoConfigMap(t, "21-grafana-dashboards.yaml")
	deploy := renderGrafanaForSetup(t, datasources, dashboards)

	mountedKeys := map[string]bool{}
	for _, v := range deploy.Spec.Template.Spec.Volumes {
		if v.ConfigMap == nil {
			continue
		}
		if len(v.ConfigMap.Items) == 0 {
			mountedKeys[v.ConfigMap.Name] = true
		}
		for _, item := range v.ConfigMap.Items {
			mountedKeys[v.ConfigMap.Name+"/"+item.Key] = true
		}
	}
	want := []string{"grafana-datasources", "grafana-dashboards/dashboard-provider.yaml"}
	for key := range dashboards.Data {
		if strings.HasSuffix(key, ".json") {
			want = append(want, "grafana-dashboards/"+key)
		}
	}
	for _, key := range want {
		if !mountedKeys[key] {
			t.Errorf("setup-rendered grafana Deployment does not mount %s", key)
		}
	}

	base := deploy.Spec.Template.Annotations[dependencyRevisionAnnotation]
	if base == "" {
		t.Fatal("setup-rendered grafana Deployment has no dependency revision annotation")
	}

	drifted := *datasources.DeepCopy()
	drifted.Data["datasources.yaml"] = strings.Replace(drifted.Data["datasources.yaml"], "uid: prometheus\n", "", 1)
	if got := renderGrafanaForSetup(t, drifted, dashboards).Spec.Template.Annotations[dependencyRevisionAnnotation]; got == base {
		t.Error("changing grafana-datasources did not change the Grafana pod template; provisioning fixes would not roll out")
	}

	changedDash := *dashboards.DeepCopy()
	changedDash.Data["mcp-server.json"] += " "
	if got := renderGrafanaForSetup(t, datasources, changedDash).Spec.Template.Annotations[dependencyRevisionAnnotation]; got == base {
		t.Error("changing grafana-dashboards did not change the Grafana pod template")
	}
}
