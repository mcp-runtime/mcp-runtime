package platformrelease

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResourceTokenDeclarationMatchesOnlyCanonicalPrometheus(t *testing.T) {
	wiring := ReleaseWiring{Components: map[string]*ComponentWiring{"prometheus": {ResourceHistoryToken: true}}}
	if err := validateResourceHistoryTokenSource("../../k8s", wiring); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("../../k8s/11-prometheus.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct{ old, new string }{{"audience: mcp-resource-history", "audience: foreign"}, {"expirationSeconds: 600", "expirationSeconds: 3600"}, {"mountPath: /var/run/secrets/mcp-resource-history", "mountPath: /wrong"}} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "prometheus.yaml"), []byte(strings.Replace(string(source), change.old, change.new, 1)), 0600); err != nil {
			t.Fatal(err)
		}
		if err := validateResourceHistoryTokenSource(dir, wiring); err == nil {
			t.Fatalf("canonical customization accepted: %s", change.new)
		}
	}
	wiring.Components = map[string]*ComponentWiring{"ingest": {ResourceHistoryToken: true}}
	if err := validateResourceHistoryTokenSource("../../k8s", wiring); err == nil {
		t.Fatal("wrong component accepted")
	}
}

func TestResourceTokenManifestRejectsWrongComponentOrServiceAccount(t *testing.T) {
	for _, name := range []string{"ingest", "prometheus"} {
		manifest, err := GenerateManifestForCommit("v0.11.0", strings.Repeat("a", 40), false, "")
		if err != nil {
			t.Fatal(err)
		}
		if name == "prometheus" {
			manifest.Components = append(manifest.Components, ManifestComponent{Name: name, ConfigOnly: true, Wiring: &ComponentWiring{ResourceHistoryToken: true, ServiceAccountName: "foreign"}})
		} else {
			manifest.Components[0].Wiring = &ComponentWiring{ResourceHistoryToken: true}
		}
		if err := manifest.Validate(); err == nil || !strings.Contains(err.Error(), "resourceHistoryToken") {
			t.Fatalf("wrong identity accepted: %v", err)
		}
	}
}
