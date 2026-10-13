package platform

import (
	"strings"
	"testing"
)

func TestTraefikTracingSupportsVersionedControllers(t *testing.T) {
	t.Setenv("MCP_TRAEFIK_OTLP_ENDPOINT", "http://collector:4318/v1/traces")
	for _, image := range []string{"traefik:v3.7.14", "rancher/mirrored-traefik:3.6.2", "registry:5000/traefik:v3.7.14@sha256:abc"} {
		args := traefikTraceArgs(image)
		if len(args) != 6 || !strings.Contains(strings.Join(args, " "), "--tracing.otlp.http.endpoint=http://collector:4318/v1/traces") {
			t.Fatalf("missing tracing for %s: %v", image, args)
		}
	}
	for _, image := range []string{"traefik:v2.10", "traefik:latest", ""} {
		if args := traefikTraceArgs(image); len(args) != 0 {
			t.Fatalf("unsupported controller got flags: %v", args)
		}
	}
}
