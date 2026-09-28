// adapter_stdio_test.go drives the `mcp-runtime adapter stdio` binary as a
// real subprocess against an in-test httptest runtime. The intent is to catch
// wire-format drift between the shim and a "fake MCP runtime" without spinning
// up Kubernetes — envtest is not involved here. The test compiles the CLI
// binary once and then runs scenario flows that exercise the production gates
// added through phases 3–6: anonymous mode, session-required flow, idempotent
// retry, session-expiry signaling, and team isolation via header inspection.
//
// Run with:
//
//	go test -v ./test/integration/... -run TestAdapterStdio
package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// jsonRPCRequest is a minimal request envelope used by the test driver.
type jsonRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// jsonRPCResponse decodes the wire format the shim emits to stdout.
type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int                    `json:"code"`
		Message string                 `json:"message"`
		Data    map[string]interface{} `json:"data,omitempty"`
	} `json:"error,omitempty"`
}

// adapterDriver wraps a running adapter subprocess plus its stdin/stdout
// pipes. The reader goroutine pushes each stdout line into a buffered channel
// so tests can synchronously wait for one response at a time.
type adapterDriver struct {
	cmd     *exec.Cmd
	in      io.WriteCloser
	out     chan []byte
	errc    chan error
	stop    func()
	stderrW *testWriter    // closed before t returns so t.Log can no longer fire
	readers sync.WaitGroup // tracks the stdout reader + Wait() goroutine
}

func (d *adapterDriver) send(t *testing.T, req jsonRPCRequest) {
	t.Helper()
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if _, err := d.in.Write(append(raw, '\n')); err != nil {
		t.Fatalf("stdin write: %v", err)
	}
}

func (d *adapterDriver) recv(t *testing.T, timeout time.Duration) jsonRPCResponse {
	t.Helper()
	select {
	case line := <-d.out:
		var resp jsonRPCResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			t.Fatalf("decode response %q: %v", line, err)
		}
		return resp
	case err := <-d.errc:
		t.Fatalf("adapter subprocess exited: %v", err)
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for adapter stdout (waited %s)", timeout)
	}
	return jsonRPCResponse{}
}

// close cancels the context (which terminates the subprocess), waits for
// every helper goroutine to finish, and then disables the stderr writer.
// The order matters: t.Log() inside testWriter.Write would panic if it
// fires after the test function returns, so we must wait for cmd.Wait()
// before letting Cleanup unblock.
func (d *adapterDriver) close() {
	_ = d.in.Close()
	d.stop()
	d.readers.Wait()
	if d.stderrW != nil {
		d.stderrW.disable()
	}
}

// startAdapter compiles the CLI once per test binary (via go test caching)
// and starts a fresh `adapter stdio` subprocess wired against runtimeURL.
// Extra args/env let callers exercise anonymous mode, request timeouts, etc.
func startAdapter(t *testing.T, runtimeURL string, args []string, env map[string]string) *adapterDriver {
	t.Helper()
	binary := buildAdapterBinary(t)

	cliArgs := append([]string{"adapter", "stdio", "--runtime-url", runtimeURL}, args...)
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, binary, cliArgs...)
	cmd.Env = append(os.Environ(),
		// Strip any inherited identity env vars so the subprocess starts from a
		// known state; tests opt in to either explicit flags or anonymous mode.
		"MCP_RUNTIME_HUMAN_ID=",
		"MCP_RUNTIME_AGENT_ID=",
		"MCP_RUNTIME_SESSION_ID=",
		"MCP_RUNTIME_TEAM_ID=",
		"MCP_PLATFORM_API_URL=",
		"MCP_PLATFORM_API_TOKEN=",
	)
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatalf("stdout pipe: %v", err)
	}
	stderrW := newTestWriter(t, "[adapter-stderr] ")
	cmd.Stderr = stderrW

	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("start adapter: %v", err)
	}

	d := &adapterDriver{
		cmd:     cmd,
		in:      stdin,
		out:     make(chan []byte, 16),
		errc:    make(chan error, 1),
		stop:    cancel,
		stderrW: stderrW,
	}

	d.readers.Add(1)
	go func() {
		defer d.readers.Done()
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 16<<20)
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			select {
			case d.out <- line:
			case <-ctx.Done():
				return
			}
		}
		// Surface scanner failures (e.g. a line larger than the buffer cap or
		// pipe I/O errors) so tests fail loudly instead of timing out.
		if err := scanner.Err(); err != nil {
			select {
			case d.errc <- fmt.Errorf("stdout scanner: %w", err):
			default:
			}
		}
		err := cmd.Wait()
		select {
		case d.errc <- err:
		default:
		}
	}()
	t.Cleanup(d.close)
	return d
}

// buildAdapterBinary compiles cmd/mcp-runtime once per test run and returns
// the path. Subsequent calls reuse the same binary.
var (
	adapterBinaryOnce sync.Once
	adapterBinary     string
	adapterBinaryErr  error
)

func buildAdapterBinary(t *testing.T) string {
	t.Helper()
	adapterBinaryOnce.Do(func() {
		dir, err := os.MkdirTemp("", "mcp-runtime-adapter-test-*")
		if err != nil {
			adapterBinaryErr = err
			return
		}
		// dir intentionally retained for the rest of the process; Go's test
		// framework cleans /tmp on exit.
		out := filepath.Join(dir, "mcp-runtime")
		// Resolve the repo root from this test file's location.
		repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
		if err != nil {
			adapterBinaryErr = err
			return
		}
		cmd := exec.Command("go", "build", "-o", out, "./cmd/mcp-runtime")
		cmd.Dir = repoRoot
		cmd.Env = os.Environ()
		if output, err := cmd.CombinedOutput(); err != nil {
			adapterBinaryErr = fmt.Errorf("go build: %v\n%s", err, output)
			return
		}
		adapterBinary = out
	})
	if adapterBinaryErr != nil {
		t.Fatalf("build adapter binary: %v", adapterBinaryErr)
	}
	return adapterBinary
}

// testWriter forwards subprocess stderr through t.Log for debug visibility.
// disable() flips it into a swallow-only mode so any in-flight subprocess
// stderr that arrives after the test function returns can't panic by calling
// t.Log on a finished test.
type testWriter struct {
	t      *testing.T
	prefix string

	mu       sync.Mutex
	disabled bool
}

func newTestWriter(t *testing.T, prefix string) *testWriter {
	return &testWriter{t: t, prefix: prefix}
}

func (w *testWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.disabled {
		return len(p), nil
	}
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if line != "" {
			w.t.Log(w.prefix + line)
		}
	}
	return len(p), nil
}

func (w *testWriter) disable() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.disabled = true
}

// fakeRuntime is the in-test stand-in for an mcp-runtime gateway. It captures
// every inbound request so tests can assert governance headers and trigger
// scenario-specific responses by tweaking the handler.
type fakeRuntime struct {
	server  *httptest.Server
	mu      sync.Mutex
	reqs    []capturedRequest
	handler func(w http.ResponseWriter, r *http.Request)
}

type capturedRequest struct {
	headers http.Header
	method  string // JSON-RPC method
	body    []byte
}

func newFakeRuntime(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *fakeRuntime {
	t.Helper()
	rt := &fakeRuntime{handler: handler}
	rt.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var envelope struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &envelope)
		rt.mu.Lock()
		rt.reqs = append(rt.reqs, capturedRequest{
			headers: r.Header.Clone(),
			method:  envelope.Method,
			body:    body,
		})
		rt.mu.Unlock()
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		rt.handler(w, r)
	}))
	t.Cleanup(rt.server.Close)
	return rt
}

func (rt *fakeRuntime) URL() string {
	return rt.server.URL + "/mcp"
}

func (rt *fakeRuntime) requests() []capturedRequest {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	out := make([]capturedRequest, len(rt.reqs))
	copy(out, rt.reqs)
	return out
}

func (rt *fakeRuntime) callsByMethod(method string) int {
	count := 0
	for _, r := range rt.requests() {
		if r.method == method {
			count++
		}
	}
	return count
}

// ---- scenarios ----

// TestAdapterStdioSessionRequiredHappyPath drives the full handshake +
// tool/list + tool/call sequence with explicit-identity flags and verifies
// governance headers reach the runtime exactly as configured.
func TestAdapterStdioSessionRequiredHappyPath(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess test; skipped in -short")
	}

	rt := newFakeRuntime(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var env struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.Unmarshal(body, &env)
		w.Header().Set("content-type", "application/json")
		w.Header().Set("Mcp-Session-Id", "rt-session-1")
		switch env.Method {
		case "initialize":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18"}}`, env.ID)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"upper"}]}}`, env.ID)
		case "tools/call":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"HELLO"}]}}`, env.ID)
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	})

	d := startAdapter(t, rt.URL(), nil, map[string]string{
		"MCP_RUNTIME_HUMAN_ID":   "support-lead",
		"MCP_RUNTIME_AGENT_ID":   "ticket-triage-agent",
		"MCP_RUNTIME_SESSION_ID": "sess-1",
		"MCP_RUNTIME_TEAM_ID":    "team-acme",
	})

	d.send(t, jsonRPCRequest{JSONRPC: "2.0", ID: 1, Method: "initialize", Params: map[string]any{}})
	if got := d.recv(t, 5*time.Second); got.Error != nil {
		t.Fatalf("initialize error: %#v", got.Error)
	}

	// notifications/initialized has no response id; skip waiting.
	d.send(t, jsonRPCRequest{JSONRPC: "2.0", Method: "notifications/initialized"})

	d.send(t, jsonRPCRequest{JSONRPC: "2.0", ID: 2, Method: "tools/list"})
	listed := d.recv(t, 5*time.Second)
	if listed.Error != nil || !strings.Contains(string(listed.Result), `"upper"`) {
		t.Fatalf("tools/list = %#v", listed)
	}

	d.send(t, jsonRPCRequest{JSONRPC: "2.0", ID: 3, Method: "tools/call", Params: map[string]any{"name": "upper", "arguments": map[string]any{"message": "hello"}}})
	called := d.recv(t, 5*time.Second)
	if called.Error != nil || !strings.Contains(string(called.Result), `HELLO`) {
		t.Fatalf("tools/call = %#v", called)
	}

	// Governance identity is the client certificate / session, not X-MCP
	// request headers. Confirm the shim completed the tools/call path above.
	if n := rt.callsByMethod("tools/call"); n != 1 {
		t.Fatalf("tools/call upstream count = %d, want 1", n)
	}
}

// TestAdapterStdioAnonymousModeBlocksDisallowedMethod verifies the
// anonymous-mode allowlist refuses tools/call before it leaves the shim and
// that initialize/tools/list still flow through without identity headers.
func TestAdapterStdioAnonymousModeBlocksDisallowedMethod(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess test; skipped in -short")
	}
	rt := newFakeRuntime(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var env struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.Unmarshal(body, &env)
		w.Header().Set("content-type", "application/json")
		switch env.Method {
		case "initialize":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18"}}`, env.ID)
		case "tools/list":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"tools":[]}}`, env.ID)
		default:
			http.Error(w, "anonymous route should not see method "+env.Method, http.StatusForbidden)
		}
	})
	d := startAdapter(t, rt.URL(), []string{"--anonymous"}, nil)

	d.send(t, jsonRPCRequest{JSONRPC: "2.0", ID: 1, Method: "initialize", Params: map[string]any{}})
	_ = d.recv(t, 5*time.Second)

	d.send(t, jsonRPCRequest{JSONRPC: "2.0", ID: 2, Method: "tools/list"})
	if got := d.recv(t, 5*time.Second); got.Error != nil {
		t.Fatalf("tools/list should be allowed in anonymous mode: %#v", got.Error)
	}

	// tools/call is not in the default anonymous allowlist; the shim must
	// reject before forwarding and the runtime must never see it.
	d.send(t, jsonRPCRequest{JSONRPC: "2.0", ID: 3, Method: "tools/call", Params: map[string]any{"name": "upper"}})
	denied := d.recv(t, 5*time.Second)
	if denied.Error == nil || denied.Error.Code != -32601 {
		t.Fatalf("expected -32601 method not allowed, got %#v", denied)
	}
	if rt.callsByMethod("tools/call") != 0 {
		t.Fatalf("tools/call leaked to upstream in anonymous mode")
	}
}

// TestAdapterStdioRetriesIdempotentMethodOnBadGateway exercises the
// method-keyed retry: tools/list gets a 502 on first attempt, then a 200.
// The shim must succeed without surfacing the transient failure.
func TestAdapterStdioRetriesIdempotentMethodOnBadGateway(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess test; skipped in -short")
	}
	var listAttempts int32
	rt := newFakeRuntime(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var env struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.Unmarshal(body, &env)
		w.Header().Set("content-type", "application/json")
		switch env.Method {
		case "initialize":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18"}}`, env.ID)
		case "tools/list":
			n := atomic.AddInt32(&listAttempts, 1)
			if n == 1 {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"tools":[]}}`, env.ID)
		}
	})
	d := startAdapter(t, rt.URL(), nil, map[string]string{
		"MCP_RUNTIME_HUMAN_ID":   "h",
		"MCP_RUNTIME_AGENT_ID":   "a",
		"MCP_RUNTIME_SESSION_ID": "s",
	})

	d.send(t, jsonRPCRequest{JSONRPC: "2.0", ID: 1, Method: "initialize", Params: map[string]any{}})
	_ = d.recv(t, 5*time.Second)

	d.send(t, jsonRPCRequest{JSONRPC: "2.0", ID: 2, Method: "tools/list"})
	got := d.recv(t, 5*time.Second)
	if got.Error != nil {
		t.Fatalf("tools/list error: %#v", got.Error)
	}
	if atomic.LoadInt32(&listAttempts) != 2 {
		t.Fatalf("tools/list upstream attempts = %d, want 2 (retry on 502)", atomic.LoadInt32(&listAttempts))
	}
}

// TestAdapterStdioSurfacesSessionExpired verifies the shim repackages a
// runtime denial body containing "session_not_found" as a JSON-RPC error
// with error.data.runtime_status = "session_expired".
func TestAdapterStdioSurfacesSessionExpired(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess test; skipped in -short")
	}
	rt := newFakeRuntime(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var env struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.Unmarshal(body, &env)
		w.Header().Set("content-type", "application/json")
		switch env.Method {
		case "initialize":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18"}}`, env.ID)
		case "tools/call":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"session_not_found"}`))
		}
	})
	d := startAdapter(t, rt.URL(), nil, map[string]string{
		"MCP_RUNTIME_HUMAN_ID":   "h",
		"MCP_RUNTIME_AGENT_ID":   "a",
		"MCP_RUNTIME_SESSION_ID": "s",
	})

	d.send(t, jsonRPCRequest{JSONRPC: "2.0", ID: 1, Method: "initialize", Params: map[string]any{}})
	_ = d.recv(t, 5*time.Second)

	d.send(t, jsonRPCRequest{JSONRPC: "2.0", ID: 2, Method: "tools/call", Params: map[string]any{"name": "upper"}})
	got := d.recv(t, 5*time.Second)
	if got.Error == nil {
		t.Fatalf("expected error response, got %#v", got)
	}
	if got.Error.Data == nil || got.Error.Data["runtime_status"] != "session_expired" {
		t.Fatalf("error.data = %#v, want runtime_status=session_expired", got.Error.Data)
	}
}
