package platform

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	"mcp-runtime/internal/cli/core"
	setupplan "mcp-runtime/internal/cli/setup/plan"
	"mcp-runtime/pkg/publicroutes"
)

func TestPublicRouteSettingsReachComponents(t *testing.T) {
	previous := core.DefaultCLIConfig
	t.Cleanup(func() { core.DefaultCLIConfig = previous })
	configured := *previous
	configured.PublicRoutes = publicroutes.Routes{Platform: "/console", Grafana: "/monitoring", Docs: "/help", DocsURL: "https://docs.example.com/", Registry: "/images"}
	core.DefaultCLIConfig = &configured
	raw, err := os.ReadFile("../../../../k8s/01-config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := renderAnalyticsConfigManifestWithReaders(string(raw), setupplan.PlatformModeTenant, AnalyticsImageSet{},
		func(string, string) (map[string]string, error) { return nil, nil }, func() string { return "traefik" })
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal([]byte(rendered), &config); err != nil {
		t.Fatal(err)
	}
	for key, expected := range map[string]string{"UI_PATH_PREFIX": "/console", "UI_GRAFANA_PATH": "/monitoring", "UI_DOCS_PATH": "/help", "UI_DOCS_URL": "https://docs.example.com/", "UI_REGISTRY_PATH": "/images"} {
		if config.Data[key] != expected {
			t.Errorf("%s=%q, want %q", key, config.Data[key], expected)
		}
	}
	raw, err = os.ReadFile("../../../../k8s/12-grafana.yaml")
	if err != nil {
		t.Fatal(err)
	}
	rendered, err = renderAnalyticsManifest(string(raw), AnalyticsImageSet{}, "", setupplan.PlatformModeTenant)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, `value: "%(protocol)s://%(domain)s/monitoring/"`) {
		t.Fatal("Grafana did not receive its public subpath")
	}
}
