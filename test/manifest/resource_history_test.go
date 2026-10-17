package manifest_test

import (
	"io"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestResourceHistoryScrapeCredentialAndKubeletPrivilege(t *testing.T) {
	content, err := os.ReadFile("../../k8s/11-prometheus.yaml")
	if err != nil {
		t.Fatal(err)
	}
	decoder := yaml.NewDecoder(strings.NewReader(string(content)))
	var tokenFound, jobFound bool
	for {
		var object map[string]any
		if err := decoder.Decode(&object); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if object["kind"] == "ConfigMap" {
			data := object["data"].(map[string]any)
			var config struct {
				Scrapes []struct {
					JobName       string `yaml:"job_name"`
					MetricsPath   string `yaml:"metrics_path"`
					Authorization struct {
						CredentialsFile string `yaml:"credentials_file"`
					} `yaml:"authorization"`
				} `yaml:"scrape_configs"`
			}
			if err := yaml.Unmarshal([]byte(data["prometheus.yml"].(string)), &config); err != nil {
				t.Fatal(err)
			}
			for _, job := range config.Scrapes {
				if job.JobName == "mcp-resource-history" {
					jobFound = job.MetricsPath == "/internal/observability/resources/metrics" && job.Authorization.CredentialsFile == "/var/run/secrets/mcp-resource-history/token"
				}
			}
		}
		if object["kind"] == "Deployment" {
			spec := object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
			for _, raw := range spec["volumes"].([]any) {
				volume := raw.(map[string]any)
				if volume["name"] != "resource-history-token" {
					continue
				}
				projection := volume["projected"].(map[string]any)
				source := projection["sources"].([]any)[0].(map[string]any)["serviceAccountToken"].(map[string]any)
				tokenFound = source["audience"] == "mcp-resource-history" && source["expirationSeconds"] == 600 && source["path"] == "token"
			}
		}
	}
	if !jobFound || !tokenFound {
		t.Fatalf("scrape job=%v projected token=%v", jobFound, tokenFound)
	}
	rbac, err := os.ReadFile("../../k8s/08-runtime-api-rbac.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rbac), `"nodes/proxy"`) {
		t.Fatal("resource history introduced kubelet exec-capable proxy permission")
	}
	if !strings.Contains(string(rbac), `resources: ["nodes/stats"]`) {
		t.Fatal("missing narrow stats permission")
	}
}
