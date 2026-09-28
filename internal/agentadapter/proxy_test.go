package agentadapter

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHTTPProxyForwardsMCPHeadersWithoutGovernanceIdentityHeaders(t *testing.T) {
	t.Parallel()

	var upstreamHost string
	var upstreamPath string
	var upstreamQuery string
	var upstreamHeaders http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHost = r.Host
		upstreamPath = r.URL.Path
		upstreamQuery = r.URL.RawQuery
		upstreamHeaders = r.Header.Clone()
		w.Header().Set(MCPSessionHeader, "runtime-mcp-session")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	t.Cleanup(upstream.Close)

	target, err := url.Parse(upstream.URL + "/oauth-example-go-2025-11-25/mcp?source=runtime")
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	handler, err := NewHTTPProxyHandler(ProxyConfig{
		CertificateIdentity: true,
		RuntimeURL:          target,
		Identity: Identity{
			HumanID:   "support-lead",
			AgentID:   "ticket-triage-agent",
			TeamID:    "team-acme",
			SessionID: "sess-ticket-triage-agent",
		},
		HostHeader: "mcp.example.local",
	})
	if err != nil {
		t.Fatalf("NewHTTPProxyHandler() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8099/mcp?client=true", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json, text/event-stream")
	req.Header.Set(MCPProtocolHeader, "2025-06-18")
	req.Header.Set(MCPSessionHeader, "client-mcp-session")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if upstreamHost != "mcp.example.local" {
		t.Fatalf("upstream host = %q, want host header override", upstreamHost)
	}
	if upstreamPath != "/oauth-example-go-2025-11-25/mcp" {
		t.Fatalf("upstream path = %q, want exact runtime route", upstreamPath)
	}
	if upstreamQuery != "source=runtime&client=true" {
		t.Fatalf("upstream query = %q, want merged query", upstreamQuery)
	}
	assertHeader(t, upstreamHeaders, MCPProtocolHeader, "2025-06-18")
	assertHeader(t, upstreamHeaders, MCPSessionHeader, "client-mcp-session")
	assertHeader(t, upstreamHeaders, "content-type", "application/json")
	assertHeader(t, upstreamHeaders, "accept", "application/json, text/event-stream")
	if got := recorder.Header().Get(MCPSessionHeader); got != "runtime-mcp-session" {
		t.Fatalf("response %s = %q, want runtime-mcp-session", MCPSessionHeader, got)
	}
}

func TestHTTPProxyPropagatesRuntimeDenial(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"trust_too_low"}`))
	}))
	t.Cleanup(upstream.Close)

	target, err := url.Parse(upstream.URL + "/oauth-example-go-2025-11-25/mcp")
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	handler, err := NewHTTPProxyHandler(testConfig(target))
	if err != nil {
		t.Fatalf("NewHTTPProxyHandler() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8099/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"upper"}}`))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	body, err := io.ReadAll(recorder.Result().Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if strings.TrimSpace(string(body)) != `{"error":"trust_too_low"}` {
		t.Fatalf("body = %q, want trust_too_low denial", strings.TrimSpace(string(body)))
	}
}

func TestHTTPProxyLogsRuntimeDenialWhenInfoEnabled(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"trust_too_low"}`))
	}))
	t.Cleanup(upstream.Close)

	target, err := url.Parse(upstream.URL + "/oauth-example-go-2025-11-25/mcp")
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	var logs bytes.Buffer
	cfg := testConfig(target)
	cfg.LogLevel = "info"
	cfg.LogWriter = &logs
	handler, err := NewHTTPProxyHandler(cfg)
	if err != nil {
		t.Fatalf("NewHTTPProxyHandler() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8099/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"upper"}}`))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	logLine := logs.String()
	for _, want := range []string{"adapter/proxy:", "403", "trust_too_low", "method=tools/call", "tool=upper"} {
		if !strings.Contains(logLine, want) {
			t.Fatalf("log line = %q, want %q", logLine, want)
		}
	}
}

func TestHTTPProxyConvertsUpstreamConnectionFailureToJSONRPCError(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	target, err := url.Parse(upstream.URL + "/oauth-example-go-2025-11-25/mcp")
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	upstream.Close()

	handler, err := NewHTTPProxyHandler(testConfig(target))
	if err != nil {
		t.Fatalf("NewHTTPProxyHandler() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8099/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"call-1","method":"tools/call","params":{"name":"upper"}}`))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadGateway, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("content-type"); !strings.Contains(contentType, "application/json") {
		t.Fatalf("content-type = %q, want application/json", contentType)
	}
	var response rpcErrorResponse
	if err := json.Unmarshal(bytes.TrimSpace(recorder.Body.Bytes()), &response); err != nil {
		t.Fatalf("Unmarshal() error = %v; body=%s", err, recorder.Body.String())
	}
	if string(response.ID) != `"call-1"` {
		t.Fatalf("id = %s, want call-1", response.ID)
	}
	if response.Error.Code != -32000 {
		t.Fatalf("error code = %d, want -32000", response.Error.Code)
	}
	if response.Error.Data["http_status"] != float64(http.StatusBadGateway) {
		t.Fatalf("http_status = %#v, want %d", response.Error.Data["http_status"], http.StatusBadGateway)
	}
}

func TestHTTPProxyCanSuppressXForwardedHeaders(t *testing.T) {
	t.Parallel()

	var upstreamHeaders http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHeaders = r.Header.Clone()
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	t.Cleanup(upstream.Close)

	target, err := url.Parse(upstream.URL + "/oauth-example-go-2025-11-25/mcp")
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	cfg := testConfig(target)
	cfg.DisableXForwarded = true
	handler, err := NewHTTPProxyHandler(cfg)
	if err != nil {
		t.Fatalf("NewHTTPProxyHandler() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8099/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	req.Header.Set("X-Forwarded-For", "198.51.100.1")
	req.Header.Set("X-Forwarded-Host", "evil.example")
	req.Header.Set("X-Forwarded-Proto", "https")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	for _, header := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"} {
		if got := upstreamHeaders.Get(header); got != "" {
			t.Fatalf("%s = %q, want empty when X-Forwarded headers are disabled", header, got)
		}
	}
}

func TestHTTPProxyFlushesStreamableHTTPEventFrames(t *testing.T) {
	t.Parallel()

	releaseSecond := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseSecond) }) }
	defer release()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("response writer does not implement http.Flusher")
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"step\":1}}\n\n"))
		flusher.Flush()
		<-releaseSecond
		_, _ = w.Write([]byte("data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"step\":2}}\n\n"))
		flusher.Flush()
	}))
	t.Cleanup(upstream.Close)

	target, err := url.Parse(upstream.URL + "/oauth-example-go-2025-11-25/mcp")
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	handler, err := NewHTTPProxyHandler(testConfig(target))
	if err != nil {
		t.Fatalf("NewHTTPProxyHandler() error = %v", err)
	}
	proxy := httptest.NewServer(handler)
	t.Cleanup(proxy.Close)

	resp, err := proxy.Client().Post(proxy.URL+"/mcp", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"upper"}}`))
	if err != nil {
		t.Fatalf("Post() error = %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	firstLineCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		line, err := reader.ReadString('\n')
		if err != nil {
			errCh <- err
			return
		}
		firstLineCh <- line
	}()

	select {
	case line := <-firstLineCh:
		if !strings.Contains(line, `"step":1`) {
			t.Fatalf("first event line = %q, want step 1", line)
		}
	case err := <-errCh:
		t.Fatalf("ReadString() error = %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first event before upstream response completed")
	}

	release()
	rest, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if !strings.Contains(string(rest), `"step":2`) {
		t.Fatalf("remaining stream = %q, want step 2", string(rest))
	}
}

func TestHTTPProxyRoutesThroughSharedTransport(t *testing.T) {
	t.Parallel()

	var called bool
	cfg := testConfig(&url.URL{Scheme: "http", Host: "ignored.example", Path: "/demo/mcp"})
	cfg.Transport = &RuntimeTransport{
		Base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			called = true
			body := io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":1,"result":{}}`))
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{},
				Body:       body,
				Request:    req,
			}, nil
		}),
	}
	handler, err := NewHTTPProxyHandler(cfg)
	if err != nil {
		t.Fatalf("NewHTTPProxyHandler() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8099/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if !called {
		t.Fatal("shared transport was not invoked; proxy still uses the default RoundTripper")
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
}

func TestHTTPProxyInjectsRuntimeStatusOnSessionExpiredDenial(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":3,"error":{"code":-32000,"message":"session_not_found"}}`))
	}))
	t.Cleanup(upstream.Close)

	target, err := url.Parse(upstream.URL + "/mcp")
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	handler, err := NewHTTPProxyHandler(testConfig(target))
	if err != nil {
		t.Fatalf("NewHTTPProxyHandler() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8099/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"upper"}}`))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
	body, _ := io.ReadAll(recorder.Result().Body)
	var response rpcErrorResponse
	if err := json.Unmarshal(bytes.TrimSpace(body), &response); err != nil {
		t.Fatalf("Unmarshal() error = %v; body=%s", err, body)
	}
	if got, _ := response.Error.Data["runtime_status"].(string); got != "session_expired" {
		t.Fatalf("runtime_status = %q, want session_expired", got)
	}
}

func TestHTTPProxyMaxInboundBytesMaxInt64DoesNotOverflow(t *testing.T) {
	t.Parallel()

	var upstreamCalled bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalled = true
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	t.Cleanup(upstream.Close)

	target, _ := url.Parse(upstream.URL + "/mcp")
	cfg := testConfig(target)
	// Setting maxBytes to MaxInt64 must not overflow when the handler computes
	// maxBytes+1 internally; reads should succeed and the upstream should see
	// the full body.
	cfg.MaxInboundBytes = math.MaxInt64
	handler, err := NewHTTPProxyHandler(cfg)
	if err != nil {
		t.Fatalf("NewHTTPProxyHandler() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8099/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (cap=MaxInt64 must not cause early EOF)", w.Code)
	}
	if !upstreamCalled {
		t.Fatal("upstream was not called; cap=MaxInt64 likely overflowed to a negative LimitReader")
	}
}

func TestHTTPProxyReturns413WhenInboundExceedsCap(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("upstream must not be called when inbound body is rejected")
	}))
	t.Cleanup(upstream.Close)

	target, _ := url.Parse(upstream.URL + "/mcp")
	cfg := testConfig(target)
	cfg.MaxInboundBytes = 64
	handler, err := NewHTTPProxyHandler(cfg)
	if err != nil {
		t.Fatalf("NewHTTPProxyHandler() error = %v", err)
	}

	// Body larger than the 64-byte cap.
	big := strings.Repeat("a", 200)
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8099/mcp", strings.NewReader(big))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusRequestEntityTooLarge)
	}
	if !bytes.Contains(recorder.Body.Bytes(), []byte(`"code":-32700`)) {
		t.Fatalf("body = %s, want JSON-RPC parse error", recorder.Body.String())
	}
}

func TestHTTPProxyHealthEndpoints(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(upstream.Close)
	target, _ := url.Parse(upstream.URL + "/mcp")
	handler, err := NewHTTPProxyHandler(testConfig(target))
	if err != nil {
		t.Fatalf("NewHTTPProxyHandler() error = %v", err)
	}
	for _, path := range []string{"/healthz", "/livez", "/readyz"} {
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8099"+path, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusNoContent {
			t.Fatalf("%s status = %d, want 204", path, w.Code)
		}
	}
}

func TestHTTPProxyMetricsEndpointDelegatesToConfiguredHandler(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(upstream.Close)
	target, _ := url.Parse(upstream.URL + "/mcp")
	cfg := testConfig(target)
	cfg.MetricsHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("# HELP mcp_adapter_test"))
	})
	handler, err := NewHTTPProxyHandler(cfg)
	if err != nil {
		t.Fatalf("NewHTTPProxyHandler() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8099/metrics", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "mcp_adapter_test") {
		t.Fatalf("body = %q, want delegated handler output", w.Body.String())
	}
}

func TestHTTPProxyMetricsEndpointReturns404WhenUnconfigured(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(upstream.Close)
	target, _ := url.Parse(upstream.URL + "/mcp")
	handler, err := NewHTTPProxyHandler(testConfig(target))
	if err != nil {
		t.Fatalf("NewHTTPProxyHandler() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8099/metrics", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (no metrics handler configured)", w.Code)
	}
}

func testConfig(runtimeURL *url.URL) ProxyConfig {
	return ProxyConfig{
		CertificateIdentity: true,
		RuntimeURL:          runtimeURL,
		Identity: Identity{
			HumanID:   "human-1",
			AgentID:   "agent-1",
			SessionID: "session-1",
		},
	}
}

func assertHeader(t *testing.T, headers http.Header, name, want string) {
	t.Helper()
	if got := headers.Get(name); got != want {
		t.Fatalf("%s = %q, want %q", name, got, want)
	}
}
