package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	policypkg "mcp-runtime/pkg/policy"
)

type upstreamRoundTripFunc func(*http.Request) (*http.Response, error)

func (f upstreamRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestUpstreamUnaryBodyDeadlineAndStreamingClass(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stream" {
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer upstream.Close()
	transport := newResilientUpstreamTransport(http.DefaultTransport.(*http.Transport).Clone(), 30*time.Millisecond, 2, 2, 5, time.Second)
	request, _ := http.NewRequest("POST", upstream.URL+"/unary", strings.NewReader(`{"method":"tools/call"}`))
	request.Header.Set("Accept", "application/json, text/event-stream")
	outcome := &upstreamOutcome{}
	request = request.WithContext(context.WithValue(request.Context(), upstreamOutcomeKey{}, outcome))
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err = io.ReadAll(response.Body)
	response.Body.Close()
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("unary body deadline: elapsed=%v err=%v", time.Since(started), err)
	}
	outcome.mu.Lock()
	if outcome.reason != "upstream_timeout" {
		t.Errorf("timeout not recorded: %q", outcome.reason)
	}
	outcome.mu.Unlock()
	request, _ = http.NewRequest("POST", upstream.URL+"/stream", strings.NewReader(`{"method":"tools/call"}`))
	request.Header.Set("Accept", "application/json, text/event-stream")
	response, err = transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	result := make(chan error, 1)
	go func() { _, err := response.Body.Read(make([]byte, 1)); result <- err }()
	select {
	case err := <-result:
		t.Fatalf("SSE was cut off by unary timeout: %v", err)
	case <-time.After(90 * time.Millisecond):
	}
	response.Body.Close()
	select {
	case <-result:
	case <-time.After(time.Second):
		t.Fatal("closing stream did not release read")
	}
}

func TestUpstreamStreamsDoNotBlockMixedUnaryTraffic(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stream" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"result":"ok"}`)
	}))
	defer upstream.Close()
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.MaxIdleConnsPerHost = 32
	transport := newResilientUpstreamTransport(base, 2*time.Second, 2, 8, 5, time.Second)
	var streams []io.Closer
	defer func() {
		for _, stream := range streams {
			stream.Close()
		}
	}()
	for i := 0; i < 8; i++ {
		r, _ := http.NewRequest("GET", upstream.URL+"/stream", nil)
		response, err := transport.RoundTrip(r)
		if err != nil {
			t.Fatal(err)
		}
		streams = append(streams, response.Body)
	}
	// A full streaming budget rejects another stream; its slot is released
	// immediately so two unary workers can still complete 100 real HTTP calls.
	r, _ := http.NewRequest("GET", upstream.URL+"/stream", nil)
	if _, err := transport.RoundTrip(r); err == nil {
		t.Fatal("streaming capacity was unbounded")
	} else {
		var gate upstreamGateError
		if !errors.As(err, &gate) || gate.status != 429 {
			t.Fatal(err)
		}
	}
	// A dispatched tool POST may choose an SSE response. It stays in the RPC
	// budget and must not be rejected after execution because GET slots are full.
	post, _ := http.NewRequest("POST", upstream.URL+"/stream", strings.NewReader(`{"method":"tools/call"}`))
	postResponse, err := transport.RoundTrip(post)
	if err != nil {
		t.Fatalf("dispatched streaming RPC was rejected: %v", err)
	}
	if len(transport.unary) != 1 || len(transport.streams) != 8 {
		t.Fatal("POST stream did not retain its pre-dispatch RPC slot")
	}
	postResponse.Body.Close()
	var workers sync.WaitGroup
	failures := make(chan error, 2)
	for i := 0; i < 2; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := 0; j < 50; j++ {
				r, _ := http.NewRequest("POST", upstream.URL+"/unary", strings.NewReader(`{"method":"tools/call"}`))
				response, err := transport.RoundTrip(r)
				if err != nil {
					failures <- err
					return
				}
				body, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil {
					failures <- err
					return
				}
				if string(body) != `{"result":"ok"}` {
					failures <- errors.New("wrong unary result")
					return
				}
			}
		}()
	}
	workers.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
}

func TestUpstreamBreakerHalfOpenAndStaleCompletion(t *testing.T) {
	var ticks atomic.Int64
	breaker := &upstreamBreaker{threshold: 2, cooldown: time.Second, now: func() time.Time { return time.Unix(0, ticks.Load()) }}
	stale, _ := breaker.acquire()
	first, _ := breaker.acquire()
	breaker.complete(first, -1)
	second, _ := breaker.acquire()
	breaker.complete(second, -1)
	if _, ok := breaker.acquire(); ok {
		t.Fatal("breaker did not open")
	}
	breaker.complete(stale, 1)
	if _, ok := breaker.acquire(); ok {
		t.Fatal("stale success closed a newer breaker")
	}
	ticks.Store(int64(time.Second))
	probe, ok := breaker.acquire()
	if !ok || !probe.probe {
		t.Fatal("missing half-open probe")
	}
	var accepted atomic.Int32
	var requests sync.WaitGroup
	for i := 0; i < 32; i++ {
		requests.Add(1)
		go func() {
			defer requests.Done()
			if _, ok := breaker.acquire(); ok {
				accepted.Add(1)
			}
		}()
	}
	requests.Wait()
	if accepted.Load() != 0 {
		t.Fatal("half-open admitted concurrent probes")
	}
	breaker.complete(probe, -1)
	if _, ok := breaker.acquire(); ok {
		t.Fatal("failed probe did not reopen")
	}
	ticks.Store(int64(2 * time.Second))
	probe, _ = breaker.acquire()
	breaker.complete(probe, 1)
	if lease, ok := breaker.acquire(); !ok || lease.probe {
		t.Fatal("successful probe did not recover")
	}
}

func TestUpstreamAdmissionDenialsHaveDistinctAuditDecisions(t *testing.T) {
	for _, reason := range []string{"upstream_backpressure", "upstream_unavailable"} {
		t.Run(reason, func(t *testing.T) {
			base := upstreamRoundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("denied request reached upstream")
				return nil, nil
			})
			transport := newResilientUpstreamTransport(base, time.Second, 1, 1, 1, time.Second)
			want := http.StatusTooManyRequests
			if reason == "upstream_backpressure" {
				transport.unary <- struct{}{}
			} else {
				lease, _ := transport.breaker.acquire()
				transport.breaker.complete(lease, -1)
				want = http.StatusServiceUnavailable
			}
			target, _ := url.Parse("http://upstream.invalid")
			proxy := newUpstreamReverseProxy(target)
			proxy.Transport = transport
			server := &gatewayServer{proxy: proxy, analyticsURL: "http://ingest.invalid", source: "resilience-test", eventType: "mcp.request", analyticsQueue: make(chan analyticsEvent, 1)}
			recorder := httptest.NewRecorder()
			exchange := newExchange(recorder, httptest.NewRequest("POST", "http://gateway.invalid/mcp", strings.NewReader(`{"method":"tools/call"}`)), "v1")
			exchange.Decision = policypkg.Allow("allowed", "v1")
			exchange.Inspection.Method, exchange.Inspection.ToolName = "tools/call", "echo"
			server.upstreamFilter(exchange)
			if recorder.Code != want || exchange.Decision.Allowed || exchange.Decision.Reason != reason || exchange.Decision.Status != want || !strings.Contains(recorder.Body.String(), reason) {
				t.Fatalf("audit outcome=%+v http=%d %s", exchange.Decision, recorder.Code, recorder.Body.String())
			}
			server.emitAuditFromExchange(exchange)
			select {
			case event := <-server.analyticsQueue:
				var payload map[string]any
				if err := json.Unmarshal(event.Envelope.Payload, &payload); err != nil {
					t.Fatal(err)
				}
				if payload["reason"] != reason || payload["decision"] != "deny" || payload["status"] != float64(want) {
					t.Fatalf("incorrect audit event: %+v", payload)
				}
			default:
				t.Fatal("upstream admission denial was not audited")
			}
		})
	}
}

func TestUpstreamTransportCountsFailuresAndRecovers(t *testing.T) {
	for _, failure := range []string{"connect", "502"} {
		t.Run(failure, func(t *testing.T) {
			var calls atomic.Int32
			var ticks atomic.Int64
			base := upstreamRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				status := http.StatusOK
				if calls.Add(1) <= 2 {
					if failure == "connect" {
						return nil, errors.New("connection refused")
					}
					status = http.StatusBadGateway
				}
				return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("result")), Request: r}, nil
			})
			transport := newResilientUpstreamTransport(base, time.Second, 2, 1, 2, time.Second)
			transport.breaker.now = func() time.Time { return time.Unix(0, ticks.Load()) }
			r := httptest.NewRequest("GET", "http://upstream.invalid", nil)
			for i := 0; i < 2; i++ {
				response, err := transport.RoundTrip(r)
				if failure == "connect" && err == nil {
					t.Fatal("expected connection error")
				}
				if failure == "502" {
					if err != nil || response.StatusCode != 502 {
						t.Fatalf("response=%v err=%v", response, err)
					}
					io.Copy(io.Discard, response.Body)
					response.Body.Close()
				}
			}
			if _, err := transport.RoundTrip(r); err == nil || calls.Load() != 2 {
				t.Fatalf("breaker did not fast fail, calls=%d err=%v", calls.Load(), err)
			}
			ticks.Store(int64(time.Second))
			for i := 0; i < 2; i++ {
				response, err := transport.RoundTrip(r)
				if err != nil {
					t.Fatal(err)
				}
				io.Copy(io.Discard, response.Body)
				response.Body.Close()
			}
			if calls.Load() != 4 {
				t.Fatalf("half-open probe did not restore traffic: calls=%d", calls.Load())
			}
		})
	}
}

func TestUpstreamBodyAbortStillFinalizesAudit(t *testing.T) {
	doc := &policypkg.Document{
		Auth:   &policypkg.Auth{Mode: "header", Headers: []string{"X-Upstream-Credential"}},
		Policy: &policypkg.Config{Mode: "allow-list", DefaultDecision: "deny", MaxSideEffect: "read", DelegatedToolRules: []policypkg.DelegatedToolRule{{Name: "echo", Decision: "allow"}}},
		Tools:  []policypkg.Tool{{Name: "echo", SideEffect: "read"}},
	}
	server := newTestGatewayServer(t, doc, func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected real upstream call") })
	server.analyticsURL, server.source, server.eventType = "http://ingest.invalid", "resilience-test", "mcp.request"
	server.analyticsQueue = make(chan analyticsEvent, 1)
	base := upstreamRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		reader, writer := io.Pipe()
		go func() {
			io.WriteString(writer, "{")
			<-r.Context().Done()
			writer.CloseWithError(r.Context().Err())
		}()
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: reader, Request: r}, nil
	})
	server.proxy.Transport = newResilientUpstreamTransport(base, 30*time.Millisecond, 1, 1, 5, time.Second)
	request := httptest.NewRequest("POST", "http://proxy.example.com/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{}}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Upstream-Credential", "fixture-secret")
	// ReverseProxy propagates ErrAbortHandler only inside an HTTP server.
	request = request.WithContext(context.WithValue(request.Context(), http.ServerContextKey, &http.Server{}))
	var aborted any
	func() { defer func() { aborted = recover() }(); server.handleGateway(httptest.NewRecorder(), request) }()
	if aborted != http.ErrAbortHandler {
		t.Fatalf("partial response did not abort HTTP stream: %v", aborted)
	}
	select {
	case event := <-server.analyticsQueue:
		var payload map[string]any
		if err := json.Unmarshal(event.Envelope.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["reason"] != "upstream_timeout" || payload["status"] != float64(200) {
			t.Fatalf("partial response audit lost timeout: %+v", payload)
		}
		if strings.Contains(string(event.Envelope.Payload), "fixture-secret") {
			t.Fatal("audit leaked upstream credential")
		}
	default:
		t.Fatal("aborted response skipped audit finalization")
	}
	if len(server.analyticsQueue) != 0 {
		t.Fatal("aborted response emitted duplicate audit")
	}
}

func TestUpstreamRejectionPreservesAuthorizationMetrics(t *testing.T) {
	doc := &policypkg.Document{
		Auth:   &policypkg.Auth{Mode: "header", Headers: []string{"X-Upstream-Credential"}},
		Policy: &policypkg.Config{Mode: "allow-list", DefaultDecision: "deny", MaxSideEffect: "read", DelegatedToolRules: []policypkg.DelegatedToolRule{{Name: "echo", Decision: "allow"}}},
		Tools:  []policypkg.Tool{{Name: "echo", SideEffect: "read"}},
	}
	server := newTestGatewayServer(t, doc, func(http.ResponseWriter, *http.Request) { t.Fatal("overloaded request reached upstream") })
	server.metrics = newGatewayMetrics(prometheus.NewRegistry())
	server.proxy.ErrorHandler = handleUpstreamError
	transport := newResilientUpstreamTransport(http.DefaultTransport, time.Second, 1, 1, 5, time.Second)
	transport.unary <- struct{}{}
	server.proxy.Transport = transport
	request := httptest.NewRequest("POST", "http://proxy.example.com/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{}}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Upstream-Credential", "fixture-secret")
	response := httptest.NewRecorder()
	server.handleGateway(response, request)
	if response.Code != 429 {
		t.Fatalf("response=%d %s", response.Code, response.Body.String())
	}
	if got := testutil.ToFloat64(server.metrics.policyDecisionsTotal.WithLabelValues("", "", "", "", "allow", "delegated_tool_allowed", "tools/call")); got != 1 {
		t.Fatalf("authorization allowance was lost: %v", got)
	}
	if got := testutil.ToFloat64(server.metrics.policyDecisionsTotal.WithLabelValues("", "", "", "", "deny", "upstream_backpressure", "tools/call")); got != 0 {
		t.Fatalf("backpressure was counted as a policy restriction: %v", got)
	}
}

func TestUpstreamPreservesUpgradeDuplexBody(t *testing.T) {
	client, peer := net.Pipe()
	defer peer.Close()
	base := upstreamRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 101, Header: http.Header{}, Body: client, Request: r}, nil
	})
	transport := newResilientUpstreamTransport(base, time.Second, 1, 1, 5, time.Second)
	response, err := transport.RoundTrip(httptest.NewRequest("GET", "http://upstream.invalid", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	duplex, ok := response.Body.(io.ReadWriteCloser)
	if !ok || len(transport.streams) != 1 || len(transport.unary) != 0 {
		t.Fatal("upgrade lost duplex body or occupied unary capacity")
	}
	done := make(chan error, 1)
	go func() { _, err := duplex.Write([]byte("ping")); done <- err }()
	buffer := make([]byte, 4)
	if _, err := io.ReadFull(peer, buffer); err != nil || string(buffer) != "ping" {
		t.Fatalf("duplex write: %q %v", buffer, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
