package agentadapter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

const modernMeta = `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}`

type stdioHarness struct {
	t      *testing.T
	stdin  *io.PipeWriter
	reader *bufio.Reader
	cancel context.CancelFunc
	done   chan error
	logs   *bytes.Buffer
	logsMu *sync.Mutex
}

// startStdioShim runs the shim against handler with piped stdin/stdout.
func startStdioShim(t *testing.T, handler http.Handler, timeout time.Duration) *stdioHarness {
	t.Helper()
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)
	runtimeURL, err := url.Parse(upstream.URL + "/svc/mcp")
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	stdin, stdinWriter := io.Pipe()
	stdoutReader, stdout := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	h := &stdioHarness{
		t:      t,
		stdin:  stdinWriter,
		reader: bufio.NewReader(stdoutReader),
		cancel: cancel,
		done:   make(chan error, 1),
		logs:   &bytes.Buffer{},
		logsMu: &sync.Mutex{},
	}
	go func() {
		defer stdout.Close()
		h.done <- RunStdioShim(ctx, ShimConfig{
			CertificateIdentity: true,
			RuntimeURL:          runtimeURL,
			Identity:            Identity{HumanID: "human-1", AgentID: "agent-1", SessionID: "session-1"},
			Transport:           &RuntimeTransport{Base: upstream.Client().Transport, Timeout: timeout},
			LogWriter:           lockedWriter{mu: h.logsMu, w: h.logs},
		}, StdioOptions{Stdin: stdin, Stdout: stdout})
	}()
	t.Cleanup(h.stop)
	return h
}

func (h *stdioHarness) send(line string) {
	h.t.Helper()
	if _, err := h.stdin.Write([]byte(line + "\n")); err != nil {
		h.t.Fatalf("stdin Write() error = %v", err)
	}
}

func (h *stdioHarness) read() string {
	h.t.Helper()
	return readLineWithin(h.t, h.reader, 2*time.Second)
}

func (h *stdioHarness) stop() {
	h.cancel()
	_ = h.stdin.Close()
	select {
	case err := <-h.done:
		if err != nil {
			h.t.Errorf("RunStdioShim() error = %v", err)
		}
	case <-time.After(10 * time.Second):
		h.t.Error("RunStdioShim() did not exit")
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func rpcID(t *testing.T, body []byte) string {
	t.Helper()
	var envelope struct {
		ID json.RawMessage `json:"id"`
	}
	_ = json.Unmarshal(body, &envelope)
	return string(envelope.ID)
}

func TestStdioShimModernRequestHeaders(t *testing.T) {
	t.Parallel()

	headers := make(chan http.Header, 4)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		headers <- r.Header.Clone()
		if strings.Contains(string(body), `"method":"initialize"`) {
			w.Header().Set(MCPSessionHeader, "legacy-session")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + rpcID(t, body) + `,"result":{}}`))
	})
	h := startStdioShim(t, handler, 0)

	// A legacy initialize negotiates 2025-11-25 and a session; the modern
	// request after it must still declare its own version and no session.
	h.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`)
	h.read()
	legacy := <-headers
	if legacy.Get(MCPMethodHeader) != "" {
		t.Fatalf("legacy request carried Mcp-Method = %q", legacy.Get(MCPMethodHeader))
	}

	h.send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"Grüße","arguments":{},` + modernMeta + `}}`)
	h.read()
	modern := <-headers
	if got := modern.Get(MCPProtocolHeader); got != "2026-07-28" {
		t.Fatalf("MCP-Protocol-Version = %q, want 2026-07-28 from _meta", got)
	}
	if got := modern.Get(MCPMethodHeader); got != "tools/call" {
		t.Fatalf("Mcp-Method = %q, want tools/call", got)
	}
	if got := modern.Get(MCPNameHeader); got != "=?base64?R3LDvMOfZQ==?=" {
		t.Fatalf("Mcp-Name = %q, want base64 sentinel", got)
	}
	if got := modern.Get(MCPSessionHeader); got != "" {
		t.Fatalf("modern request sent Mcp-Session-Id = %q", got)
	}

	h.send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{}}}`)
	h.read()
	legacyCall := <-headers
	if got := legacyCall.Get(MCPProtocolHeader); got != "2025-11-25" {
		t.Fatalf("legacy MCP-Protocol-Version = %q, want negotiated 2025-11-25", got)
	}
	if got := legacyCall.Get(MCPSessionHeader); got != "legacy-session" {
		t.Fatalf("legacy Mcp-Session-Id = %q, want legacy-session", got)
	}
}

func TestStdioShimMirrorsXMCPHeaderParameters(t *testing.T) {
	t.Parallel()

	callHeaders := make(chan http.Header, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"method":"tools/list"`) {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"resultType":"complete","tools":[
				{"name":"execute_sql","inputSchema":{"type":"object","properties":{
					"region":{"type":"string","x-mcp-header":"Region"},
					"shard":{"type":"integer","x-mcp-header":"Shard"},
					"query":{"type":"string"}}}},
				{"name":"broken","inputSchema":{"type":"object","properties":{"list":{"type":"array","items":{"type":"string","x-mcp-header":"Item"}}}}}]}}`))
			return
		}
		callHeaders <- r.Header.Clone()
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"content":[]}}`))
	})
	h := startStdioShim(t, handler, 0)

	h.send(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{` + modernMeta + `}}`)
	listLine := h.read()
	if strings.Contains(listLine, `"broken"`) || !strings.Contains(listLine, `"execute_sql"`) {
		t.Fatalf("tools/list = %s, want execute_sql only", listLine)
	}
	h.logsMu.Lock()
	logged := h.logs.String()
	h.logsMu.Unlock()
	if !strings.Contains(logged, "dropped tool broken") {
		t.Fatalf("log = %q, want warning about dropped tool", logged)
	}

	h.send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"execute_sql","arguments":{"region":"us-west1","shard":null,"query":"SELECT 1"},` + modernMeta + `}}`)
	h.read()
	got := <-callHeaders
	if v := got.Get("Mcp-Param-Region"); v != "us-west1" {
		t.Fatalf("Mcp-Param-Region = %q, want us-west1", v)
	}
	if _, ok := got["Mcp-Param-Shard"]; ok {
		t.Fatalf("null argument produced Mcp-Param-Shard = %q", got.Get("Mcp-Param-Shard"))
	}
}

func TestStdioShimCancelsModernRequestOnNotificationsCancelled(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	cancelled := make(chan struct{})
	var mu sync.Mutex
	var methods []string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		meta := parseRPCRequestMetadata(body)
		mu.Lock()
		methods = append(methods, meta.Method)
		mu.Unlock()
		if meta.ToolName == "slow" {
			w.Header().Set("content-type", "text/event-stream")
			w.(http.Flusher).Flush()
			close(started)
			<-r.Context().Done()
			close(cancelled)
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + rpcID(t, body) + `,"result":{}}`))
	})
	h := startStdioShim(t, handler, 0)

	h.send(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"slow","arguments":{},` + modernMeta + `}}`)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("slow request did not reach the runtime")
	}
	h.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":7,"reason":"user"}}`)
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("runtime request was not cancelled")
	}

	// The shim keeps serving, and never responds for the cancelled id.
	h.send(`{"jsonrpc":"2.0","id":8,"method":"tools/list","params":{` + modernMeta + `}}`)
	if line := h.read(); !strings.Contains(line, `"id":8`) {
		t.Fatalf("stdout = %q, want response for id 8", line)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, m := range methods {
		if m == "notifications/cancelled" {
			t.Fatal("notifications/cancelled was forwarded over HTTP")
		}
	}
}

func TestStdioShimForwardsLegacyNotificationsCancelled(t *testing.T) {
	t.Parallel()

	forwarded := make(chan string, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if parseRPCRequestMetadata(body).Method == "notifications/cancelled" {
			forwarded <- string(body)
		}
		w.WriteHeader(http.StatusAccepted)
	})
	h := startStdioShim(t, handler, 0)

	h.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":3}}`)
	select {
	case <-forwarded:
	case <-time.After(2 * time.Second):
		t.Fatal("legacy notifications/cancelled was not forwarded")
	}
}

func TestStdioShimSubscriptionsListenIgnoresRequestTimeout(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if parseRPCRequestMetadata(body).Method != "subscriptions/listen" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + rpcID(t, body) + `,"result":{}}`))
			return
		}
		flusher := w.(http.Flusher)
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/subscriptions/acknowledged\",\"params\":{\"notifications\":{\"toolsListChanged\":true}}}\n\n"))
		flusher.Flush()
		select {
		case <-time.After(300 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte("data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\"}\n\n"))
		flusher.Flush()
	})
	// The request timeout is shorter than the gap between notifications.
	h := startStdioShim(t, handler, 100*time.Millisecond)

	h.send(`{"jsonrpc":"2.0","id":1,"method":"subscriptions/listen","params":{"notifications":{"toolsListChanged":true},` + modernMeta + `}}`)
	if line := h.read(); !strings.Contains(line, "notifications/subscriptions/acknowledged") {
		t.Fatalf("first line = %q, want acknowledgment", line)
	}
	if line := h.read(); !strings.Contains(line, "notifications/tools/list_changed") {
		t.Fatalf("second line = %q, want list_changed after the request timeout elapsed", line)
	}
}

func newInflightTestShim() *stdioShim {
	return &stdioShim{
		inflight:     make(map[string]*inflightRequest),
		recentModern: newRecentIDs(recentModernCapacity),
	}
}

func modernCancelMeta(id string) rpcRequestMetadata {
	return rpcRequestMetadata{ID: json.RawMessage(id), HasID: true, ProtocolVersion: ModernProtocolVersion}
}

func TestRegisterInflightDuplicateIDKeepsLaterEntry(t *testing.T) {
	t.Parallel()

	s := newInflightTestShim()
	_, releaseFirst := s.registerInflight(context.Background(), modernCancelMeta("7"))
	secondCtx, releaseSecond := s.registerInflight(context.Background(), modernCancelMeta("7"))
	defer releaseSecond()

	// The first request finishing must not remove the second's entry.
	releaseFirst()
	if !s.cancelInflight([]byte(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":7}}`)) {
		t.Fatal("cancelInflight() = false, want true for the still in-flight duplicate")
	}
	if !errors.Is(context.Cause(secondCtx), errRequestCancelled) {
		t.Fatalf("second request cause = %v, want errRequestCancelled", context.Cause(secondCtx))
	}
}

func TestCancelInflightDropsLateModernCancellations(t *testing.T) {
	t.Parallel()

	s := newInflightTestShim()
	_, release := s.registerInflight(context.Background(), modernCancelMeta("9"))
	release()
	if !s.cancelInflight([]byte(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":9}}`)) {
		t.Fatal("cancel for a completed modern request was not dropped")
	}
	if !s.cancelInflight([]byte(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":99,` + modernMeta + `}}`)) {
		t.Fatal("cancel declaring a modern protocol version was not dropped")
	}
	if s.cancelInflight([]byte(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":99}}`)) {
		t.Fatal("legacy cancel for an unknown request was dropped, want forwarded")
	}

	// A legacy request reusing the ID must get its cancellation forwarded.
	_, releaseLegacy := s.registerInflight(context.Background(), rpcRequestMetadata{ID: json.RawMessage("9"), HasID: true})
	defer releaseLegacy()
	if s.cancelInflight([]byte(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":9}}`)) {
		t.Fatal("cancel for an in-flight legacy request was dropped, want forwarded")
	}
}

func TestRecentIDsEvictsOldest(t *testing.T) {
	t.Parallel()

	r := newRecentIDs(2)
	r.add("1")
	r.add("2")
	r.add("3")
	if r.contains("1") || !r.contains("2") || !r.contains("3") {
		t.Fatalf("recentIDs after overflow = %v, want 2 and 3", r.set)
	}
	r.remove("2")
	r.add("4")
	if r.contains("2") || !r.contains("3") || !r.contains("4") {
		t.Fatalf("recentIDs after remove = %v, want 3 and 4", r.set)
	}
}

func TestStdioShimDropsCancelForCompletedModernRequest(t *testing.T) {
	t.Parallel()

	forwarded := make(chan string, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if parseRPCRequestMetadata(body).Method == "notifications/cancelled" {
			forwarded <- string(body)
			w.WriteHeader(http.StatusAccepted)
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + rpcID(t, body) + `,"result":{}}`))
	})
	h := startStdioShim(t, handler, 0)

	h.send(`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"echo","arguments":{},` + modernMeta + `}}`)
	if line := h.read(); !strings.Contains(line, `"id":9`) {
		t.Fatalf("stdout = %q, want response for id 9", line)
	}
	h.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":9}}`)
	h.send(`{"jsonrpc":"2.0","id":10,"method":"tools/list","params":{` + modernMeta + `}}`)
	if line := h.read(); !strings.Contains(line, `"id":10`) {
		t.Fatalf("stdout = %q, want response for id 10", line)
	}
	select {
	case body := <-forwarded:
		t.Fatalf("late cancellation for a modern request was forwarded: %s", body)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestStdioShimModernResponseSessionIDKeepsLegacySession(t *testing.T) {
	t.Parallel()

	legacySessions := make(chan string, 4)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		meta := parseRPCRequestMetadata(body)
		switch {
		case meta.Method == "initialize":
			w.Header().Set(MCPSessionHeader, "legacy-session")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25"}}`))
			return
		case isModernProtocolVersion(meta.ProtocolVersion):
			w.Header().Set(MCPSessionHeader, "modern-stray")
		default:
			legacySessions <- r.Header.Get(MCPSessionHeader)
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + rpcID(t, body) + `,"result":{}}`))
	})
	h := startStdioShim(t, handler, 0)

	h.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`)
	h.read()
	h.send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{},` + modernMeta + `}}`)
	h.read()
	h.send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{}}}`)
	h.read()
	if got := <-legacySessions; got != "legacy-session" {
		t.Fatalf("legacy Mcp-Session-Id after modern response = %q, want legacy-session", got)
	}
}
