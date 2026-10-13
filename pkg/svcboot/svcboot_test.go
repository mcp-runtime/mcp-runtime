package svcboot

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"mcp-runtime/pkg/serviceutil"
)

// A handler behind the shared middleware must be able to outlive the
// server-wide read timeout by extending its own deadline, as the runtime-api
// registry push does for large image uploads.
func TestWrapHandlerAllowsExtendingReadDeadline(t *testing.T) {
	const readTimeout = 200 * time.Millisecond

	handler := WrapHandler("svcboot-test", serviceutil.NewRequestMetrics(prometheus.NewRegistry()), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := http.NewResponseController(w).SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Errorf("SetReadDeadline through middleware: %v", err)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusRequestTimeout)
			return
		}
		_, _ = io.WriteString(w, strconv.Itoa(len(body)))
	}))
	server := httptest.NewUnstartedServer(handler)
	server.Config.ReadTimeout = readTimeout
	server.Config.ReadHeaderTimeout = readTimeout
	server.Start()
	defer server.Close()

	// Stream the body for several read timeouts' worth of time.
	const chunks = 5
	pr, pw := io.Pipe()
	go func() {
		for i := 0; i < chunks; i++ {
			time.Sleep(readTimeout / 2)
			if _, err := pw.Write([]byte("chunk")); err != nil {
				return
			}
		}
		_ = pw.Close()
	}()

	resp, err := http.Post(server.URL, "application/octet-stream", pr)
	if err != nil {
		t.Fatalf("slow upload failed: %v", err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(got) != strconv.Itoa(chunks*len("chunk")) {
		t.Fatalf("slow upload: status %d body %q", resp.StatusCode, got)
	}
}

func TestWrapHandlerAlignsConfiguredServiceAcrossLogsAndMetrics(t *testing.T) {
	t.Setenv("OTEL_SERVICE_NAME", "custom-runtime-api")
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })
	registry := prometheus.NewRegistry()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /demo", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	handler := WrapHandler("mcp-runtime-api", serviceutil.NewRequestMetrics(registry), mux)
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/demo", nil))
	if !strings.Contains(logs.String(), `service="custom-runtime-api"`) {
		t.Fatalf("log identity differs from configured tracing service: %s", logs.String())
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, family := range families {
		if family.GetName() != "mcp_request_total" {
			continue
		}
		for _, metric := range family.Metric {
			for _, label := range metric.Label {
				if label.GetName() == "service" {
					found = true
					if label.GetValue() != "custom-runtime-api" {
						t.Fatalf("metric identity = %q", label.GetValue())
					}
				}
			}
		}
	}
	if !found {
		t.Fatal("missing service request counter")
	}
}
