package operator

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	"testing"
)

func TestServerTracingDefaultsPreserveExplicitWorkloadSettings(t *testing.T) {
	server := &mcpv1alpha1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "team-a"}}
	r := &MCPServerReconciler{GatewayOTLPEndpoint: "http://collector:4318"}
	env := r.buildServerEnvVars(server)
	values := map[string]string{}
	for _, e := range env {
		values[e.Name] = e.Value
	}
	if values["OTEL_SERVICE_NAME"] != "example-server" || values["OTEL_EXPORTER_OTLP_ENDPOINT"] != r.GatewayOTLPEndpoint {
		t.Fatalf("missing server tracing: %v", values)
	}
	expected := map[string]string{"OTEL_SERVICE_NAME": "custom", "OTEL_EXPORTER_OTLP_ENDPOINT": "http://other:4318"}
	server.Spec.EnvVars = []mcpv1alpha1.EnvVar{{Name: "OTEL_SERVICE_NAME", Value: "custom"}, {Name: "OTEL_EXPORTER_OTLP_ENDPOINT", Value: "http://other:4318"}}
	for _, e := range r.buildServerEnvVars(server) {
		if want, ok := expected[e.Name]; ok && e.Value != want {
			t.Fatalf("explicit %s overwritten", e.Name)
		}
	}
}
