package svcboot

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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
