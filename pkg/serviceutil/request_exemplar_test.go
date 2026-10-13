package serviceutil

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/trace"
)

func TestRequestMetricsSampledExemplarMatchesRequestTrace(t *testing.T) {
	for _, sampled := range []bool{true, false} {
		t.Run(map[bool]string{true: "sampled", false: "unsampled"}[sampled], func(t *testing.T) {
			registry := prometheus.NewRegistry()
			metrics := NewRequestMetrics(registry)
			flags := trace.TraceFlags(0)
			if sampled {
				flags = trace.FlagsSampled
			}
			sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: flags})
			ctx := trace.ContextWithSpanContext(context.Background(), sc)
			mux := http.NewServeMux()
			mux.HandleFunc("GET /sessions/{id}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
			handler := metrics.Middleware("mcp-runtime-api", mux)
			for _, path := range []string{"/sessions/private-a", "/sessions/private-b"} {
				handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx))
			}
			families, err := registry.Gather()
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, family := range families {
				if family.GetName() != "mcp_request_duration_seconds" {
					continue
				}
				if len(family.Metric) != 1 {
					t.Fatalf("dynamic URLs created %d series", len(family.Metric))
				}
				for _, bucket := range family.Metric[0].GetHistogram().Bucket {
					exemplar := bucket.GetExemplar()
					if exemplar == nil {
						continue
					}
					found = true
					if !sampled {
						t.Fatal("unsampled request has exemplar")
					}
					labels := exemplar.GetLabel()
					if len(labels) != 1 || labels[0].GetName() != "trace_id" || labels[0].GetValue() != sc.TraceID().String() {
						t.Fatalf("wrong exemplar: %v", exemplar)
					}
				}
			}
			if found != sampled {
				t.Fatalf("exemplar found=%v, sampled=%v", found, sampled)
			}
		})
	}
}
