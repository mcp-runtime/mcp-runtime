package platform

import (
	"os"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func loadPromtailConfig(t *testing.T) (string, string) {
	t.Helper()
	content, err := os.ReadFile("../../../../k8s/18-promtail.yaml")
	if err != nil {
		t.Fatalf("read promtail manifest: %v", err)
	}
	var cfg, ds string
	for _, doc := range strings.Split(string(content), "\n---\n") {
		var obj struct {
			Kind string            `json:"kind"`
			Data map[string]string `json:"data"`
		}
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			t.Fatalf("manifest document is not valid YAML: %v", err)
		}
		switch obj.Kind {
		case "ConfigMap":
			cfg = obj.Data["promtail.yaml"]
		case "DaemonSet":
			ds = doc
		}
	}
	if cfg == "" || ds == "" {
		t.Fatal("promtail ConfigMap or DaemonSet missing")
	}
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(cfg), &parsed); err != nil {
		t.Fatalf("promtail.yaml is not valid YAML: %v", err)
	}
	return cfg, ds
}

func TestPromtailCollectsAllNamespacesWithKubernetesMetadata(t *testing.T) {
	cfg, ds := loadPromtailConfig(t)

	for _, want := range []string{
		"role: pod",
		"field: spec.nodeName=${NODE_NAME}",
		"target_label: namespace",
		"target_label: pod",
		"target_label: container",
		"target_label: app",
		"target_label: node",
	} {
		if !strings.Contains(cfg, want) {
			t.Fatalf("promtail config missing %q:\n%s", want, cfg)
		}
	}
	// Discovery must not be limited to the platform namespace.
	if strings.Contains(cfg, "namespaces:") {
		t.Fatalf("kubernetes discovery must not restrict namespaces:\n%s", cfg)
	}
	if strings.Count(cfg, "- cri: {}") < 2 {
		t.Fatalf("expected CRI parsing in every scrape job:\n%s", cfg)
	}
	if !strings.Contains(cfg, "source: filename") {
		t.Fatalf("fallback job must label streams from the log file path:\n%s", cfg)
	}
	if strings.Count(cfg, "[REDACTED]") < 2 {
		t.Fatalf("expected credential redaction stages:\n%s", cfg)
	}
	if !strings.Contains(ds, "-config.expand-env=true") || !strings.Contains(ds, "fieldPath: spec.nodeName") {
		t.Fatalf("DaemonSet must pass NODE_NAME and enable env expansion:\n%s", ds)
	}
	// With env expansion enabled, bare capture references would expand to empty.
	for _, line := range strings.Split(cfg, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "#") {
			continue
		}
		for _, ref := range []string{"$1", "${1}", "${2}"} {
			escaped := "$" + ref
			if strings.Contains(strings.ReplaceAll(trim, escaped, ""), ref) {
				t.Fatalf("unescaped capture reference %s under -config.expand-env: %q", ref, trim)
			}
		}
	}
}

func TestPromtailManifestUsesCollectorNamespace(t *testing.T) {
	content, err := os.ReadFile("../../../../k8s/18-promtail.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(content), "namespace: mcp-log-collector") < 4 {
		t.Fatal("promtail resources must use the collector namespace")
	}
}
