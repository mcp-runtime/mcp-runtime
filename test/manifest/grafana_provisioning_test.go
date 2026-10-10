package manifest_test

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Server-card deep links emitted by runtime-api target
// /grafana/d/mcp-server/mcp-server?var-namespace=...&var-server=...&viewPanel=N
// (services/runtime-api/internal/runtimeapi/observability.go). These tests pin
// the provisioning contract those links depend on (issue #543): every
// datasource uid a dashboard references is provisioned, every dashboard file
// is mounted where the dashboard provider reads it, and the server dashboard
// keeps its uid, variables, and panel ids.
const (
	grafanaServerDashboardUID = "mcp-server"
	grafanaDatasourceKey      = "datasources.yaml"
	grafanaProviderKey        = "dashboard-provider.yaml"
)

// grafanaServerPanelIDs mirrors the runtime-api query PanelIDs (Target health,
// Request rate, Deny rate, p95 latency).
var grafanaServerPanelIDs = []int{1, 2, 3, 4}

type grafanaConfigMap struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Data map[string]string `yaml:"data"`
}

type grafanaDeployment struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		Template struct {
			Spec struct {
				Containers []struct {
					Name         string `yaml:"name"`
					VolumeMounts []struct {
						Name      string `yaml:"name"`
						MountPath string `yaml:"mountPath"`
						SubPath   string `yaml:"subPath"`
					} `yaml:"volumeMounts"`
				} `yaml:"containers"`
				Volumes []struct {
					Name      string `yaml:"name"`
					ConfigMap *struct {
						Name  string `yaml:"name"`
						Items []struct {
							Key  string `yaml:"key"`
							Path string `yaml:"path"`
						} `yaml:"items"`
					} `yaml:"configMap"`
				} `yaml:"volumes"`
			} `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

func decodeGrafanaDocs[T any](t *testing.T, manifest string, keep func(T) bool) []T {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "k8s", manifest))
	if err != nil {
		t.Fatalf("read %s: %v", manifest, err)
	}
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	var out []T
	for {
		var doc T
		if err := dec.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("decode %s: %v", manifest, err)
		}
		if keep(doc) {
			out = append(out, doc)
		}
	}
	return out
}

func grafanaConfigMapNamed(t *testing.T, manifest, name string) grafanaConfigMap {
	t.Helper()
	docs := decodeGrafanaDocs(t, manifest, func(cm grafanaConfigMap) bool {
		return cm.Kind == "ConfigMap" && cm.Metadata.Name == name
	})
	if len(docs) != 1 {
		t.Fatalf("%s: expected exactly one ConfigMap %q, found %d", manifest, name, len(docs))
	}
	return docs[0]
}

func provisionedGrafanaDatasourceUIDs(t *testing.T) map[string]string {
	t.Helper()
	cm := grafanaConfigMapNamed(t, "19-grafana-datasources.yaml", "grafana-datasources")
	var cfg struct {
		Datasources []struct {
			Name string `yaml:"name"`
			Type string `yaml:"type"`
			UID  string `yaml:"uid"`
		} `yaml:"datasources"`
	}
	if err := yaml.Unmarshal([]byte(cm.Data[grafanaDatasourceKey]), &cfg); err != nil {
		t.Fatalf("parse %s: %v", grafanaDatasourceKey, err)
	}
	uids := map[string]string{}
	for _, ds := range cfg.Datasources {
		if strings.TrimSpace(ds.UID) == "" {
			t.Errorf("datasource %q (%s) has no pinned uid; Grafana would auto-assign one and dashboards could not reference it", ds.Name, ds.Type)
			continue
		}
		if prev, dup := uids[ds.UID]; dup {
			t.Errorf("datasource uid %q is used by both %q and %q", ds.UID, prev, ds.Type)
		}
		uids[ds.UID] = ds.Type
	}
	return uids
}

// collectDatasourceRefs walks a dashboard document and returns every
// {"datasource": {"type": ..., "uid": ...}} reference, including panel and
// target level references.
func collectDatasourceRefs(node any, out *[][2]string) {
	switch v := node.(type) {
	case map[string]any:
		for key, child := range v {
			if key == "datasource" {
				if ref, ok := child.(map[string]any); ok {
					uid, _ := ref["uid"].(string)
					typ, _ := ref["type"].(string)
					*out = append(*out, [2]string{uid, typ})
				}
				continue
			}
			collectDatasourceRefs(child, out)
		}
	case []any:
		for _, child := range v {
			collectDatasourceRefs(child, out)
		}
	}
}

func TestGrafanaDashboardDatasourceUIDsAreProvisioned(t *testing.T) {
	provisioned := provisionedGrafanaDatasourceUIDs(t)
	if provisioned["prometheus"] != "prometheus" {
		t.Fatalf("the Prometheus datasource must be provisioned with uid %q, got %v", "prometheus", provisioned)
	}

	dashboards := grafanaConfigMapNamed(t, "21-grafana-dashboards.yaml", "grafana-dashboards")
	found := 0
	for key, body := range dashboards.Data {
		if !strings.HasSuffix(key, ".json") {
			continue
		}
		found++
		var doc any
		if err := json.Unmarshal([]byte(body), &doc); err != nil {
			t.Errorf("%s is not valid JSON: %v", key, err)
			continue
		}
		var refs [][2]string
		collectDatasourceRefs(doc, &refs)
		if len(refs) == 0 {
			t.Errorf("%s does not reference any datasource", key)
		}
		for _, ref := range refs {
			uid, typ := ref[0], ref[1]
			gotType, ok := provisioned[uid]
			if !ok {
				t.Errorf("%s references datasource uid %q which grafana-datasources does not provision (Grafana reports \"Data source not found\")", key, uid)
				continue
			}
			if typ != "" && typ != gotType {
				t.Errorf("%s references datasource uid %q as type %q, but it is provisioned as %q", key, uid, typ, gotType)
			}
		}
	}
	if found == 0 {
		t.Fatal("grafana-dashboards provisions no dashboard JSON")
	}
}

func TestGrafanaServerDashboardMatchesDeepLinks(t *testing.T) {
	dashboards := grafanaConfigMapNamed(t, "21-grafana-dashboards.yaml", "grafana-dashboards")
	var dash struct {
		UID    string `json:"uid"`
		Panels []struct {
			ID int `json:"id"`
		} `json:"panels"`
		Templating struct {
			List []struct {
				Name string `json:"name"`
			} `json:"list"`
		} `json:"templating"`
	}
	if err := json.Unmarshal([]byte(dashboards.Data["mcp-server.json"]), &dash); err != nil {
		t.Fatalf("mcp-server.json: %v", err)
	}
	if dash.UID != grafanaServerDashboardUID {
		t.Fatalf("mcp-server.json uid = %q, want %q", dash.UID, grafanaServerDashboardUID)
	}
	panels := map[int]bool{}
	for _, p := range dash.Panels {
		panels[p.ID] = true
	}
	for _, id := range grafanaServerPanelIDs {
		if !panels[id] {
			t.Errorf("mcp-server dashboard is missing panel id %d used by server-card viewPanel links", id)
		}
	}
	vars := map[string]bool{}
	for _, v := range dash.Templating.List {
		vars[v.Name] = true
	}
	for _, name := range []string{"namespace", "server"} {
		if !vars[name] {
			t.Errorf("mcp-server dashboard is missing template variable %q used by var-%s links", name, name)
		}
	}
}

func TestGrafanaDeploymentMountsAllProvisioningInputs(t *testing.T) {
	dashboards := grafanaConfigMapNamed(t, "21-grafana-dashboards.yaml", "grafana-dashboards")
	var provider struct {
		Providers []struct {
			Type    string `yaml:"type"`
			Options struct {
				Path string `yaml:"path"`
			} `yaml:"options"`
		} `yaml:"providers"`
	}
	if err := yaml.Unmarshal([]byte(dashboards.Data[grafanaProviderKey]), &provider); err != nil {
		t.Fatalf("parse %s: %v", grafanaProviderKey, err)
	}
	if len(provider.Providers) != 1 || provider.Providers[0].Type != "file" || provider.Providers[0].Options.Path == "" {
		t.Fatalf("expected one file dashboard provider with a path, got %+v", provider.Providers)
	}
	dashboardDir := path.Clean(provider.Providers[0].Options.Path)

	deployments := decodeGrafanaDocs(t, "12-grafana.yaml", func(d grafanaDeployment) bool {
		return d.Kind == "Deployment" && d.Metadata.Name == "grafana"
	})
	if len(deployments) != 1 {
		t.Fatalf("expected one grafana Deployment, got %d", len(deployments))
	}
	podSpec := deployments[0].Spec.Template.Spec

	// files maps each in-container file path to the ConfigMap key it serves.
	files := map[string]string{}
	mountedConfigMaps := map[string]bool{}
	for _, c := range podSpec.Containers {
		if c.Name != "grafana" {
			continue
		}
		for _, m := range c.VolumeMounts {
			if m.SubPath != "" {
				t.Errorf("volumeMount %q uses subPath; ConfigMap updates would never reach Grafana", m.Name)
			}
			for _, v := range podSpec.Volumes {
				if v.Name != m.Name || v.ConfigMap == nil {
					continue
				}
				if len(v.ConfigMap.Items) == 0 {
					mountedConfigMaps[path.Clean(m.MountPath)+"|"+v.ConfigMap.Name] = true
				}
				for _, item := range v.ConfigMap.Items {
					files[path.Join(m.MountPath, item.Path)] = v.ConfigMap.Name + "/" + item.Key
				}
			}
		}
	}

	if !mountedConfigMaps["/etc/grafana/provisioning/datasources|grafana-datasources"] {
		t.Error("grafana must mount ConfigMap grafana-datasources at /etc/grafana/provisioning/datasources")
	}
	if got := files["/etc/grafana/provisioning/dashboards/"+grafanaProviderKey]; got != "grafana-dashboards/"+grafanaProviderKey {
		t.Errorf("grafana must mount %s into /etc/grafana/provisioning/dashboards, got %q", grafanaProviderKey, got)
	}

	var keys []string
	for key := range dashboards.Data {
		if strings.HasSuffix(key, ".json") {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		want := "grafana-dashboards/" + key
		mounted := false
		for file, source := range files {
			if source == want && path.Dir(file) == dashboardDir {
				mounted = true
			}
		}
		if !mounted {
			t.Errorf("dashboard %s is in grafana-dashboards but not mounted into the provider path %s; Grafana never loads it", key, dashboardDir)
		}
	}
}
