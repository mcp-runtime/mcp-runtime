package serviceutil

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

type flushResponseRecorder struct {
	http.ResponseWriter
	flushed bool
}

func (r *flushResponseRecorder) Flush() {
	r.flushed = true
}

func TestLogRequestsPreservesFlusher(t *testing.T) {
	recorder := &flushResponseRecorder{ResponseWriter: httptest.NewRecorder()}
	handler := LogRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("wrapped response writer does not implement http.Flusher")
		}
		flusher.Flush()
		w.WriteHeader(http.StatusNoContent)
	}))

	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/events", nil))

	if !recorder.flushed {
		t.Fatal("Flush was not delegated to the underlying response writer")
	}
}

func TestMetricsHandlerServesHealth(t *testing.T) {
	recorder := httptest.NewRecorder()
	metricsHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if recorder.Body.String() != "ok" {
		t.Fatalf("body = %q, want ok", recorder.Body.String())
	}
}

func TestStartMetricsServerReportsListenError(t *testing.T) {
	_, errs := StartMetricsServer("127.0.0.1:-1")

	select {
	case err, ok := <-errs:
		if !ok {
			t.Fatal("error channel closed without a listen error")
		}
		if err == nil {
			t.Fatal("listen error is nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for metrics server listen error")
	}
}

func TestRequestMetricsUseRoutePatternsAndExportLatency(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewRequestMetrics(registry)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/tools/{name}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	handler := metrics.Middleware("mcp-runtime-api", mux)
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/tools/private-tool", nil))

	labels := []string{"mcp-runtime-api", "GET /api/v1/tools/{name}", "503", "none"}
	if got := testutil.ToFloat64(metrics.total.WithLabelValues(labels...)); got != 1 {
		t.Fatalf("request count = %v, want 1", got)
	}
	if got := testutil.ToFloat64(metrics.errors.WithLabelValues(labels...)); got != 1 {
		t.Fatalf("error count = %v, want 1", got)
	}

	recorder := httptest.NewRecorder()
	promhttp.HandlerFor(registry, promhttp.HandlerOpts{}).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := recorder.Body.String()
	for _, want := range []string{
		"mcp_request_total",
		"mcp_request_errors_total",
		"mcp_request_duration_seconds_bucket",
		`operation="GET /api/v1/tools/{name}"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output missing %q", want)
		}
	}
	if strings.Contains(body, "private-tool") {
		t.Fatal("metrics output contains a raw path segment")
	}
}

func TestRequestMetricsDoNotCountClientErrorsAsServiceErrors(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewRequestMetrics(registry)
	metrics.Record("mcp-platform-api", "/api/v1/users", http.StatusForbidden, "none", time.Millisecond)
	if got := testutil.ToFloat64(metrics.errors.WithLabelValues("mcp-platform-api", "/api/v1/users", "403", "none")); got != 0 {
		t.Fatalf("error count for client rejection = %v, want 0", got)
	}
}

func TestStatusRecorderUnwrapsToUnderlyingWriter(t *testing.T) {
	inner := httptest.NewRecorder()
	recorder := &statusRecorder{ResponseWriter: inner, status: http.StatusOK}
	if got := recorder.Unwrap(); got != inner {
		t.Fatalf("Unwrap() = %T, want the wrapped writer", got)
	}
}
