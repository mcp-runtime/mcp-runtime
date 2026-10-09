package runtimeapi

import (
	"net/http/httptest"
	"net/url"
	"testing"

	"mcp-runtime/pkg/controlplane"
)

func TestCustomPublicObservabilityPaths(t *testing.T) {
	t.Setenv("UI_PATH_PREFIX", "/console")
	t.Setenv("UI_GRAFANA_PATH", "/monitoring")
	t.Setenv(envGrafanaServerDashboardURL, "")
	r := httptest.NewRequest("GET", "/api/v1/runtime/servers/team/demo/observability", nil)
	r.Header.Set("x-mcp-source", "ui")
	if path := observabilityRuntimeAPIPrefix(r); path != "/console/api/ui/v1/runtime" {
		t.Fatalf("UI proxy link: %s", path)
	}
	r.Header.Del("x-mcp-source")
	if path := observabilityRuntimeAPIPrefix(r); path != "/api/v1/runtime" {
		t.Fatalf("direct API link: %s", path)
	}
	info := controlplane.ServerInfo{Name: "demo", Namespace: "team"}
	link := grafanaLinkForServer(info, principal{Role: roleAdmin})
	u, err := url.Parse(link.URL)
	if err != nil || !link.Available || !link.DirectAdminOnly || u.Path != "/monitoring/d/mcp-server/mcp-server" || u.Query().Get("var-server") != "demo" {
		t.Fatalf("custom admin link: %+v %v", link, err)
	}
	if link := grafanaLinkForServer(info, principal{Role: roleUser}); link.Available {
		t.Fatal("custom path exposed an admin-only dashboard to a user")
	}
}
