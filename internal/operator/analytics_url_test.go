package operator

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
)

const testDefaultIngestURL = "https://mcp-ingest.mcp-observability.svc.cluster.local:8081/events"

func TestEffectiveAnalyticsIngestURL(t *testing.T) {
	tests := []struct {
		name string
		spec string
		def  string
		want string
	}{
		{"empty uses default", "", testDefaultIngestURL, testDefaultIngestURL},
		{"platform http same host", "http://mcp-ingest.mcp-observability.svc.cluster.local:8081/events", testDefaultIngestURL, testDefaultIngestURL},
		{"platform http short svc form", "http://mcp-ingest.mcp-observability.svc:8081/events", testDefaultIngestURL, testDefaultIngestURL},
		{"platform http svc form without default", "http://mcp-ingest.other.svc.cluster.local:8081/events", "", "http://mcp-ingest.other.svc.cluster.local:8081/events"},
		{"platform https passthrough", "https://mcp-ingest.mcp-observability.svc.cluster.local:9000/custom", testDefaultIngestURL, "https://mcp-ingest.mcp-observability.svc.cluster.local:9000/custom"},
		{"external http untouched", "http://ingest.example.com/events", testDefaultIngestURL, "http://ingest.example.com/events"},
		{"lookalike host untouched", "http://mcp-ingest.mcp-observability.svc.evil.com/events", testDefaultIngestURL, "http://mcp-ingest.mcp-observability.svc.evil.com/events"},
		{"embedded credentials untouched", "http://user:pw@mcp-ingest.mcp-observability.svc.cluster.local:8081/events", testDefaultIngestURL, "http://user:pw@mcp-ingest.mcp-observability.svc.cluster.local:8081/events"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := &mcpv1alpha1.MCPServer{}
			if tt.spec != "" {
				server.Spec.Analytics = &mcpv1alpha1.AnalyticsConfig{IngestURL: tt.spec}
			}
			if got := effectiveAnalyticsIngestURL(server, tt.def); got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}

func TestReconcileAnalyticsCARejectsExternalHTTPAndCredentialsButMigratesPlatformHTTP(t *testing.T) {
	for _, tt := range []struct {
		spec    string
		wantErr bool
	}{
		{"http://mcp-ingest.mcp-observability.svc.cluster.local:8081/events", false},
		{"http://ingest.example.com/events", true},
		{"https://user:pw@mcp-ingest.mcp-observability.svc.cluster.local:8081/events", true},
	} {
		scheme := runtime.NewScheme()
		_ = corev1.AddToScheme(scheme)
		_ = mcpv1alpha1.AddToScheme(scheme)
		server := mtlsServer()
		server.Spec.Analytics = &mcpv1alpha1.AnalyticsConfig{IngestURL: tt.spec}
		source := &corev1.ConfigMap{Data: map[string]string{"ca.crt": analyticsTestCA(t)}}
		source.Name, source.Namespace = "mcp-analytics-ca", "mcp-runtime"
		c := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(server, source).Build()
		r := &MCPServerReconciler{Client: c, Scheme: scheme, AnalyticsCAConfigMap: source.Name, DefaultAnalyticsIngestURL: testDefaultIngestURL, GatewayProxyImage: "example.com/gateway:test"}
		err := r.reconcileAnalyticsCA(context.Background(), server)
		if (err != nil) != tt.wantErr {
			t.Fatalf("spec %q: err=%v wantErr=%v", "redacted-case", err, tt.wantErr)
		}
	}
}
