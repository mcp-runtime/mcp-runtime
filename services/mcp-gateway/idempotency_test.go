package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/prometheus/client_golang/prometheus"

	policypkg "mcp-runtime/pkg/policy"
)

const idemRawKey = "raw-client-key-7f3a9c"

type idemHarness struct {
	s      *gatewayServer
	issuer *testJWTIssuer
	hits   atomic.Int32
	reg    *prometheus.Registry
}

func idemPolicy(issuerURL string) *policypkg.Document {
	doc := oauthPolicy(issuerURL)
	doc.Server = policypkg.Server{Name: "pilot", Namespace: "mcp-servers", UID: "uid-1"}
	doc.Sessions = append(doc.Sessions, policypkg.Binding{
		Name: "session-2", HumanID: "human-1", AgentID: "client-1", ConsentedTrust: "high",
	})
	return doc
}

func newIdemHarness(t *testing.T, upstream http.HandlerFunc) *idemHarness {
	t.Helper()
	h := &idemHarness{issuer: newTestJWTIssuer(t), reg: prometheus.NewRegistry()}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.hits.Add(1)
		upstream(w, r)
	}))
	t.Cleanup(srv.Close)
	target, _ := url.Parse(srv.URL)
	h.s = minimalServer()
	h.s.proxy = newUpstreamReverseProxy(target)
	h.s.httpClient = &http.Client{Timeout: 2 * time.Second}
	h.s.metrics = newGatewayMetrics(h.reg)
	h.s.snapshotPolicy(policySnapshot{Policy: idemPolicy(h.issuer.url), Ready: true, LoadedAt: time.Now()})
	return h
}

func okJSON(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"done"}]}}`)
}

func (h *idemHarness) call(t *testing.T, key, body, sid string) *httptest.ResponseRecorder {
	t.Helper()
	token := h.issuer.sign(t, jwt.MapClaims{
		"iss": h.issuer.url, "aud": "http://proxy.example.com/mcp", "sub": "human-1", "azp": "client-1",
		"sid": sid, "exp": time.Now().Add(time.Hour).Unix(), "nbf": time.Now().Add(-time.Minute).Unix(),
	})
	req := httptest.NewRequest(http.MethodPost, "http://proxy.example.com/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	h.s.handleGateway(rec, req)
	return rec
}

const toolBody = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"a":1,"b":2}}}`

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, status int, reason string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d body=%s, want %d", rec.Code, rec.Body.String(), status)
	}
	if reason != "" && !strings.Contains(rec.Body.String(), reason) {
		t.Fatalf("body = %s, want reason %q", rec.Body.String(), reason)
	}
}

func TestIdempotencyAbsentHeaderChangesNothing(t *testing.T) {
	h := newIdemHarness(t, okJSON)
	for i := 0; i < 2; i++ {
		rec := h.call(t, "", toolBody, "session-1")
		wantStatus(t, rec, 200, "")
		if rec.Header().Get("Idempotency-Replayed") != "" {
			t.Fatal("unexpected replay header")
		}
	}
	if h.hits.Load() != 2 {
		t.Fatalf("upstream hits = %d, want 2", h.hits.Load())
	}
}

func TestIdempotencyReplaysAfterFreshAuthorization(t *testing.T) {
	var sawKey atomic.Value
	h := newIdemHarness(t, func(w http.ResponseWriter, r *http.Request) {
		sawKey.Store(r.Header.Get("Idempotency-Key"))
		okJSON(w, r)
	})
	first := h.call(t, idemRawKey, toolBody, "session-1")
	wantStatus(t, first, 200, "done")
	if first.Header().Get("Idempotency-Replayed") != "" {
		t.Fatal("first response must not be marked replayed")
	}
	if got := sawKey.Load(); got != "" {
		t.Fatalf("upstream saw Idempotency-Key %q; the gateway-scoped key must not be forwarded", got)
	}
	// Same logical call, params with different key order and spacing.
	reordered := `{"id":1, "method":"tools/call", "jsonrpc":"2.0", "params":{"arguments":{"b":2,"a":1},"name":"echo"}}`
	second := h.call(t, idemRawKey, reordered, "session-1")
	wantStatus(t, second, 200, "done")
	if second.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replay header = %q", second.Header().Get("Idempotency-Replayed"))
	}
	if second.Body.String() != first.Body.String() || second.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("replay body/content-type differ: %q %q", second.Body.String(), second.Header().Get("Content-Type"))
	}
	if h.hits.Load() != 1 {
		t.Fatalf("upstream hits = %d, want 1", h.hits.Load())
	}

	// Revocation/policy change is honoured: the retry is denied, not replayed.
	revoked := idemPolicy(h.issuer.url)
	revoked.Grants = nil
	h.s.snapshotPolicy(policySnapshot{Policy: revoked, Ready: true, LoadedAt: time.Now()})
	denied := h.call(t, idemRawKey, toolBody, "session-1")
	if denied.Code == 200 || denied.Header().Get("Idempotency-Replayed") != "" {
		t.Fatalf("replay served after grant removal: %d %s", denied.Code, denied.Body.String())
	}
	if h.hits.Load() != 1 {
		t.Fatalf("upstream hits = %d, want 1", h.hits.Load())
	}
}

func TestIdempotencySameKeyDifferentRequestConflicts(t *testing.T) {
	h := newIdemHarness(t, okJSON)
	wantStatus(t, h.call(t, idemRawKey, toolBody, "session-1"), 200, "")
	other := strings.Replace(toolBody, `"a":1`, `"a":9`, 1)
	wantStatus(t, h.call(t, idemRawKey, other, "session-1"), 409, "idempotency_key_conflict")
	// A different JSON-RPC id is a different request; id alone never matches.
	otherID := strings.Replace(toolBody, `"id":1`, `"id":2`, 1)
	wantStatus(t, h.call(t, idemRawKey, otherID, "session-1"), 409, "idempotency_key_conflict")
	if h.hits.Load() != 1 {
		t.Fatalf("upstream hits = %d, want 1", h.hits.Load())
	}
}

func TestIdempotencyScopedToSession(t *testing.T) {
	h := newIdemHarness(t, okJSON)
	wantStatus(t, h.call(t, idemRawKey, toolBody, "session-1"), 200, "")
	rec := h.call(t, idemRawKey, toolBody, "session-2")
	wantStatus(t, rec, 200, "")
	if rec.Header().Get("Idempotency-Replayed") != "" || h.hits.Load() != 2 {
		t.Fatalf("a different session replayed another session's result: hits=%d", h.hits.Load())
	}
}

func TestIdempotencyScopedToServerUID(t *testing.T) {
	h := newIdemHarness(t, okJSON)
	wantStatus(t, h.call(t, idemRawKey, toolBody, "session-1"), 200, "")
	recreated := idemPolicy(h.issuer.url)
	recreated.Server.UID = "uid-2"
	h.s.snapshotPolicy(policySnapshot{Policy: recreated, Ready: true, LoadedAt: time.Now()})
	rec := h.call(t, idemRawKey, toolBody, "session-1")
	wantStatus(t, rec, 200, "")
	if rec.Header().Get("Idempotency-Replayed") != "" || h.hits.Load() != 2 {
		t.Fatalf("a recreated MCPServer replayed the old UID's result: hits=%d", h.hits.Load())
	}
}

func TestIdempotencyRejectsInvalidAndUnsupportedUse(t *testing.T) {
	h := newIdemHarness(t, okJSON)
	wantStatus(t, h.call(t, strings.Repeat("k", 256), toolBody, "session-1"), 400, "idempotency_key_invalid")
	wantStatus(t, h.call(t, "bad key\twith control", toolBody, "session-1"), 400, "idempotency_key_invalid")
	list := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	wantStatus(t, h.call(t, idemRawKey, list, "session-1"), 400, "idempotency_unsupported_request")
	if h.hits.Load() != 0 {
		t.Fatalf("rejected idempotency requests reached upstream: %d", h.hits.Load())
	}
}

func TestIdempotencyRejectedForDelegatedHeaderMode(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); okJSON(w, r) }))
	t.Cleanup(upstream.Close)
	target, _ := url.Parse(upstream.URL)
	s := minimalServer()
	s.proxy = newUpstreamReverseProxy(target)
	doc := delegatedPolicy([]string{"X-Example-Credential"}, "any", policypkg.DelegatedToolRule{Name: "list_projects", Decision: "allow"})
	doc.Server.UID = "uid-1"
	s.snapshotPolicy(policySnapshot{Policy: doc, Ready: true, LoadedAt: time.Now()})
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_projects"}}`
	req := httptest.NewRequest(http.MethodPost, "/pilot/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Example-Credential", "synthetic")
	req.Header.Set("Idempotency-Key", idemRawKey)
	rec := httptest.NewRecorder()
	s.handleGateway(rec, req)
	wantStatus(t, rec, 400, "idempotency_requires_verified_identity")
	if hits.Load() != 0 {
		t.Fatal("delegated-header idempotency request reached upstream")
	}
}

func TestIdempotencyRequiresKnownServerUID(t *testing.T) {
	h := newIdemHarness(t, okJSON)
	noUID := idemPolicy(h.issuer.url)
	noUID.Server.UID = ""
	h.s.snapshotPolicy(policySnapshot{Policy: noUID, Ready: true, LoadedAt: time.Now()})
	wantStatus(t, h.call(t, idemRawKey, toolBody, "session-1"), 503, "idempotency_unavailable")
	if h.hits.Load() != 0 {
		t.Fatal("request reached upstream")
	}
}

func TestIdempotencyAmbiguousUpstreamNeverReexecutes(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"disconnect": func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) },
		"server_5xx": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
	} {
		t.Run(name, func(t *testing.T) {
			h := newIdemHarness(t, handler)
			first := h.call(t, idemRawKey, toolBody, "session-1")
			if first.Code < 500 {
				t.Fatalf("first status = %d, want upstream failure", first.Code)
			}
			wantStatus(t, h.call(t, idemRawKey, toolBody, "session-1"), 409, "idempotency_outcome_unknown")
			if h.hits.Load() != 1 {
				t.Fatalf("upstream hits = %d, want exactly 1 (never re-execute)", h.hits.Load())
			}
		})
	}
}

func TestIdempotencyUpstreamClientErrorReleasesKey(t *testing.T) {
	var n atomic.Int32
	h := newIdemHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		okJSON(w, r)
	})
	wantStatus(t, h.call(t, idemRawKey, toolBody, "session-1"), 400, "")
	wantStatus(t, h.call(t, idemRawKey, toolBody, "session-1"), 200, "done")
	if h.hits.Load() != 2 {
		t.Fatalf("hits = %d", h.hits.Load())
	}
}

func TestIdempotencyStreamingResponseIsNotReplayable(t *testing.T) {
	h := newIdemHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n")
	})
	first := h.call(t, idemRawKey, toolBody, "session-1")
	wantStatus(t, first, 200, "jsonrpc")
	wantStatus(t, h.call(t, idemRawKey, toolBody, "session-1"), 409, "idempotency_response_not_replayable")
	if h.hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1", h.hits.Load())
	}
}

func TestIdempotencyOversizedReplyIsDeliveredButNotReplayable(t *testing.T) {
	big := bytes.Repeat([]byte("x"), 64<<10)
	h := newIdemHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(big)
	})
	first := h.call(t, idemRawKey, toolBody, "session-1")
	if first.Code != 200 || first.Body.Len() != len(big) {
		t.Fatalf("first response truncated: %d %d", first.Code, first.Body.Len())
	}
	wantStatus(t, h.call(t, idemRawKey, toolBody, "session-1"), 409, "idempotency_response_not_replayable")
	if h.hits.Load() != 1 {
		t.Fatalf("hits = %d", h.hits.Load())
	}
}

func TestIdempotencyConcurrentDuplicateIsRejectedWhileInFlight(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	h := newIdemHarness(t, func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		okJSON(w, r)
	})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- h.call(t, idemRawKey, toolBody, "session-1") }()
	<-entered
	dup := h.call(t, idemRawKey, toolBody, "session-1")
	wantStatus(t, dup, 409, "idempotency_in_flight")
	if dup.Header().Get("Retry-After") == "" {
		t.Fatal("in-flight rejection needs Retry-After")
	}
	close(release)
	wantStatus(t, <-done, 200, "done")
	wantStatus(t, h.call(t, idemRawKey, toolBody, "session-1"), 200, "done")
	if h.hits.Load() != 1 {
		t.Fatalf("hits = %d", h.hits.Load())
	}
}

func TestIdempotencyNeverRecordsRawKeyOrPayload(t *testing.T) {
	h := newIdemHarness(t, okJSON)
	h.call(t, idemRawKey, toolBody, "session-1")
	replay := h.call(t, idemRawKey, toolBody, "session-1")
	if replay.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal("expected replay")
	}
	text := gatherMetricsText(t, h.reg)
	if !strings.Contains(text, "mcp_gateway_idempotency_total") || !strings.Contains(text, `outcome="replay"`) || !strings.Contains(text, `outcome="first"`) {
		t.Fatalf("idempotency metric missing:\n%s", text)
	}
	if strings.Contains(text, idemRawKey) {
		t.Fatal("raw key leaked into metrics")
	}

	req := httptest.NewRequest(http.MethodPost, "http://proxy.example.com/mcp", strings.NewReader(toolBody))
	ex := newTestExchange(http.MethodPost, "/mcp", toolBody, nil)
	ex.Idempotency = idempotencyAudit{Outcome: "replay", KeyHash: "abcd1234abcd1234"}
	payload := h.s.auditPayloadForExchange(ex, req)
	if payload["idempotency"] != "replay" || payload["idempotency_key_hash"] != "abcd1234abcd1234" {
		t.Fatalf("audit payload = %#v", payload)
	}
	for k, v := range payload {
		if s, ok := v.(string); ok && strings.Contains(s, idemRawKey) {
			t.Fatalf("raw key in audit field %s", k)
		}
	}
}
