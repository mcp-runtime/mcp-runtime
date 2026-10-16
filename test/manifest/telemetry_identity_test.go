package manifest_test

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// Runtime's tenant queries require the same immutable identity in both stores.
func TestTelemetryCollectorsCarryMCPServerUID(t *testing.T) {
	for _, tc := range []struct{ manifest, configMap, key, job string }{
		{"11-prometheus.yaml", "prometheus-config", "prometheus.yml", "mcp-gateway-sidecars"},
		{"18-promtail.yaml", "promtail-config", "promtail.yaml", "kubernetes-pods"},
	} {
		t.Run(tc.configMap, func(t *testing.T) {
			cm := grafanaConfigMapNamed(t, tc.manifest, tc.configMap)
			var config struct {
				Jobs []struct {
					Name     string `yaml:"job_name"`
					Relabels []struct {
						Sources []string `yaml:"source_labels"`
						Target  string   `yaml:"target_label"`
					} `yaml:"relabel_configs"`
				} `yaml:"scrape_configs"`
			}
			if err := yaml.Unmarshal([]byte(cm.Data[tc.key]), &config); err != nil {
				t.Fatal(err)
			}
			for _, job := range config.Jobs {
				if job.Name != tc.job {
					continue
				}
				for _, rule := range job.Relabels {
					if rule.Target == "server_uid" && len(rule.Sources) == 1 && rule.Sources[0] == "__meta_kubernetes_pod_label_mcpruntime_org_server_uid" {
						return
					}
				}
			}
			t.Fatal("collector does not carry the workload's immutable MCPServer UID")
		})
	}
}
