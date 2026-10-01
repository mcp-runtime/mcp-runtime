package platform

import (
	"os"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// requiredScrapeJobs is the scrape coverage contract for the platform install.
// Removing one of these jobs from k8s/11-prometheus.yaml, or from the required
// target inventory in rules.yml, must fail this test.
var requiredScrapeJobs = []string{
	"mcp-platform-api",
	"mcp-runtime-api",
	"mcp-analytics-api",
	"mcp-sentinel-ingest",
	"mcp-sentinel-processor",
	"clickhouse",
	"prometheus",
	"otel-collector",
	"loki",
	"tempo",
	"promtail",
	"mcp-sentinel-annotated-services",
	"mcp-gateway-sidecars",
}

func loadPrometheusConfigMap(t *testing.T) map[string]string {
	t.Helper()
	content, err := os.ReadFile("../../../../k8s/11-prometheus.yaml")
	if err != nil {
		t.Fatalf("failed to read prometheus manifest: %v", err)
	}
	dec := yaml.NewDecoder(strings.NewReader(string(content)))
	for {
		var doc struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
			Data map[string]string `yaml:"data"`
		}
		if err := dec.Decode(&doc); err != nil {
			t.Fatalf("prometheus-config ConfigMap not found: %v", err)
		}
		if doc.Kind == "ConfigMap" && doc.Metadata.Name == "prometheus-config" {
			return doc.Data
		}
	}
}

func TestPrometheusScrapeCoverageContract(t *testing.T) {
	data := loadPrometheusConfigMap(t)

	var cfg struct {
		RuleFiles     []string `yaml:"rule_files"`
		ScrapeConfigs []struct {
			JobName string `yaml:"job_name"`
		} `yaml:"scrape_configs"`
	}
	if err := yaml.Unmarshal([]byte(data["prometheus.yml"]), &cfg); err != nil {
		t.Fatalf("prometheus.yml is not valid YAML: %v", err)
	}
	var jobs []string
	for _, sc := range cfg.ScrapeConfigs {
		jobs = append(jobs, sc.JobName)
	}
	for _, want := range requiredScrapeJobs {
		if !slices.Contains(jobs, want) {
			t.Errorf("Prometheus is missing required scrape job %q (have %v)", want, jobs)
		}
	}
	if !slices.Contains(cfg.RuleFiles, "/etc/prometheus/rules.yml") {
		t.Fatalf("prometheus.yml must load /etc/prometheus/rules.yml, got %v", cfg.RuleFiles)
	}

	var rules struct {
		Groups []struct {
			Rules []struct {
				Record string `yaml:"record"`
				Alert  string `yaml:"alert"`
				Expr   string `yaml:"expr"`
			} `yaml:"rules"`
		} `yaml:"groups"`
	}
	if err := yaml.Unmarshal([]byte(data["rules.yml"]), &rules); err != nil {
		t.Fatalf("rules.yml is not valid YAML: %v", err)
	}
	inventory := map[string]bool{}
	alerts := map[string]string{}
	var uninstrumented []string
	for _, g := range rules.Groups {
		for _, r := range g.Rules {
			switch {
			case r.Record == "mcp:scrape_required_target:info":
				inventory[r.Expr] = true
			case r.Record == "mcp:scrape_uninstrumented_workload:info":
				uninstrumented = append(uninstrumented, r.Expr)
			case r.Alert != "":
				alerts[r.Alert] = r.Expr
			}
		}
	}
	// Every required scrape job except the discovery-based ones must be
	// declared in the required-target inventory so its absence alerts.
	for _, job := range requiredScrapeJobs {
		if strings.HasPrefix(job, "mcp-sentinel-annotated-services") || job == "mcp-gateway-sidecars" {
			continue
		}
		want := `label_replace(vector(1), "job", "` + job + `", "", "")`
		if !inventory[want] {
			t.Errorf("required target %q is not in the mcp:scrape_required_target:info inventory", job)
		}
		if !strings.Contains(alerts["MCPRequiredScrapeTargetDown"], job) {
			t.Errorf("MCPRequiredScrapeTargetDown does not cover job %q", job)
		}
	}
	if !strings.Contains(alerts["MCPRequiredScrapeTargetAbsent"], "unless on (job) up") {
		t.Errorf("MCPRequiredScrapeTargetAbsent must compare the inventory against up, got %q", alerts["MCPRequiredScrapeTargetAbsent"])
	}
	for _, name := range []string{"MCPOAuthFailures", "MCPGatewayAnalyticsDrops", "MCPTelemetryExportFailures"} {
		if alerts[name] == "" {
			t.Errorf("missing alert rule %s", name)
		}
	}
	if !strings.Contains(alerts["MCPOAuthFailures"], "mcp:gateway_oauth_failures:rate5m") {
		t.Errorf("MCPOAuthFailures must be built on the OAuth outcome recording rule")
	}
	// Workloads without metrics must be listed, not silently absent.
	for _, workload := range []string{"kafka", "postgres", "mcp-sentinel-ui", "mcp-auth-server"} {
		found := false
		for _, expr := range uninstrumented {
			if strings.Contains(expr, `"`+workload+`"`) {
				found = true
			}
		}
		if !found {
			t.Errorf("uninstrumented workload %q is not surfaced in the inventory", workload)
		}
	}
}

func TestOtelCollectorExposesInternalMetrics(t *testing.T) {
	content, err := os.ReadFile("../../../../k8s/15-otel-collector.yaml")
	if err != nil {
		t.Fatalf("failed to read otel collector manifest: %v", err)
	}
	text := string(content)
	for _, want := range []string{"address: 0.0.0.0:8888", "port: 8888", "containerPort: 8888"} {
		if !strings.Contains(text, want) {
			t.Errorf("otel collector manifest must expose internal metrics on 8888; missing %q", want)
		}
	}
}

func TestScrapeCoverageDashboardProvisioned(t *testing.T) {
	content, err := os.ReadFile("../../../../k8s/21-grafana-dashboards.yaml")
	if err != nil {
		t.Fatalf("failed to read grafana dashboards manifest: %v", err)
	}
	var cm struct {
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal(content, &cm); err != nil {
		t.Fatalf("dashboards manifest is not valid YAML: %v", err)
	}
	dash := cm.Data["scrape-coverage.json"]
	for _, want := range []string{
		"mcp:scrape_required_target:info",
		"mcp:scrape_uninstrumented_workload:info",
		"mcp_gateway_oauth_outcomes_total",
		"mcp_gateway_analytics_drop_total",
	} {
		if !strings.Contains(dash, want) {
			t.Errorf("scrape-coverage dashboard missing query for %q", want)
		}
	}
}
