package platform

import (
	"os"
	"strings"
	"testing"
)

func TestPlatformGatewayUsesConfiguredTraefikImage(t *testing.T) {
	content, err := os.ReadFile("../../../../k8s/10-gateway.yaml")
	if err != nil {
		t.Fatal(err)
	}
	const image = "registry.example.com/traefik:v3.7.14"
	rendered, err := renderAnalyticsManifest(string(content), AnalyticsImageSet{Traefik: image}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "image: "+image) || strings.Contains(rendered, "image: traefik:") {
		t.Fatal("configured controller image did not replace managed manifest image")
	}
}
