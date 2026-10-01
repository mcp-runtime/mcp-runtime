package platformrelease

import (
	"mcp-runtime/pkg/platforminventory"
	"testing"
)

func TestReleasePlacementComesFromInventory(t *testing.T) {
	for _, c := range Catalog() {
		identity, ok := platforminventory.Lookup(c.Name)
		if !ok || c.Namespace != identity.Namespace || c.Deployment != identity.Resource {
			t.Fatalf("release placement disagrees with canonical inventory: %+v, %+v", c, identity)
		}
	}
	proxy, _ := Lookup("gateway-proxy")
	operator, _ := Lookup("operator")
	if proxy.Deployment != operator.Deployment || proxy.EnvVar != "MCP_GATEWAY_PROXY_IMAGE" {
		t.Fatal("gateway image must remain on operator env var")
	}
	doctor, _ := Lookup("doctor-smoke")
	if doctor.HasWorkload() || doctor.Namespace != "" {
		t.Fatal("doctor smoke is image-only")
	}
}
