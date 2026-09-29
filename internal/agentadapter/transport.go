package agentadapter

import (
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

const (
	retryMaxAttempts = 3
	retryBaseDelay   = 100 * time.Millisecond
	retryMaxDelay    = 1 * time.Second
)

// RuntimeTransport is the shared outbound HTTP transport used by both the
// reverse proxy when forwarding to the runtime. It owns
// every production gate — auth, OTel instrumentation, and method-keyed retry —
// so both adapters behave identically with a single implementation.
type RuntimeTransport struct {
	// Base is the underlying round-tripper. nil means http.DefaultTransport.
	// Tests swap in a mock by setting this field.
	Base http.RoundTripper
	// Timeout is the per-request timeout applied to the *http.Client wrapper
	// returned by Client(). Zero means no timeout.
	Timeout time.Duration
	// AuthHeader is a static Authorization header value injected into every
	// outbound request (e.g. "Bearer <token>"). Empty means no header is set.
	AuthHeader string
	// Tracer is an optional OTel tracer. When non-nil, RoundTrip opens one
	// client span per RPC labelled with the JSON-RPC method name.
	Tracer trace.Tracer
	// Meter is an optional OTel meter. When non-nil, RoundTrip records a
	// latency histogram and a denial counter keyed by method name.
	Meter metric.Meter

	otelOnce    sync.Once
	latencyHist metric.Float64Histogram
	denialCount metric.Int64Counter
}

// RoundTrip implements http.RoundTripper. Execution order per call:
//  1. Start OTel span (if Tracer is set).
//  2. Inject Authorization header (if AuthHeader is set).
//  3. Execute the request, retrying idempotent methods on gateway errors.
//  4. Record OTel latency histogram and denial counter (if Meter is set).
//  5. Set span outcome and end it.
func (t *RuntimeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	method := rpcMethodFromContext(req.Context())

	// 1. Start OTel span before any I/O.
	var span trace.Span
	if t != nil && t.Tracer != nil {
		spanName := method
		if spanName == "" {
			spanName = "rpc"
		}
		_, span = t.Tracer.Start(req.Context(), "adapter.rpc/"+spanName,
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(attribute.String("rpc.method", spanName)))
		defer span.End()
	}

	// 2. Inject auth header without mutating the caller's copy.
	if t != nil && t.AuthHeader != "" {
		cloned := req.Clone(req.Context())
		cloned.Header.Set("Authorization", t.AuthHeader)
		req = cloned
	}

	retryable := isRetryableMethod(method)
	maxAttempts := 1
	if retryable {
		maxAttempts = retryMaxAttempts
	}

	// 3. Retry loop.
	baseReq := req
	start := time.Now()
	var resp *http.Response
	var lastErr error

	for i := 0; i < maxAttempts; i++ {
		if i > 0 {
			// time.NewTimer + Stop() so a context cancellation does not leak
			// the underlying *Timer until backoff would have fired.
			timer := time.NewTimer(retryBackoff(i))
			select {
			case <-req.Context().Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				lastErr = req.Context().Err()
				goto done
			case <-timer.C:
			}
			// Rehydrate the request body from the factory so the retry
			// sends a fresh reader over the same in-memory bytes.
			if baseReq.GetBody == nil {
				break
			}
			body, err := baseReq.GetBody()
			if err != nil {
				break
			}
			req = baseReq.Clone(req.Context())
			req.Body = body
		}

		resp, lastErr = t.base().RoundTrip(req)
		if lastErr != nil {
			if isRetryableError(lastErr) {
				continue
			}
			break
		}
		// Defensive nil check: the RoundTripper contract guarantees a
		// non-nil resp when err is nil, but a misbehaving Base might
		// violate it and panic the adapter.
		if resp == nil || !shouldRetryStatus(resp.StatusCode) {
			break
		}
		// Drain body before retry to allow connection reuse.
		if i < maxAttempts-1 {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			resp = nil
		}
	}

done:
	// 4. Record OTel metrics.
	if t != nil && t.Meter != nil {
		t.initOTelInstruments()
		elapsed := float64(time.Since(start).Milliseconds())
		attrs := metric.WithAttributes(attribute.String("rpc.method", method))
		t.latencyHist.Record(req.Context(), elapsed, attrs)
		// Only count 4xx as denials: 5xx is an upstream failure and would
		// inflate the denial metric, misclassifying availability incidents
		// as authorization/policy denials.
		if lastErr == nil && resp != nil && resp.StatusCode >= http.StatusBadRequest && resp.StatusCode < http.StatusInternalServerError {
			t.denialCount.Add(req.Context(), 1, attrs)
		}
	}

	// 5. Finalise span.
	if span != nil {
		if lastErr != nil {
			span.RecordError(lastErr)
			span.SetStatus(otelcodes.Error, lastErr.Error())
		} else if resp != nil && resp.StatusCode >= http.StatusBadRequest {
			span.SetStatus(otelcodes.Error, http.StatusText(resp.StatusCode))
			span.SetAttributes(attribute.Int("http.status_code", resp.StatusCode))
		} else {
			span.SetStatus(otelcodes.Ok, "")
		}
	}

	return resp, lastErr
}

// Client returns an *http.Client whose Transport is this RuntimeTransport.
// Both adapters route requests through this wrapper so every gate (auth, OTel,
// retry) applies uniformly.
func (t *RuntimeTransport) Client() *http.Client {
	var timeout time.Duration
	if t != nil {
		timeout = t.Timeout
	}
	return &http.Client{Transport: t, Timeout: timeout}
}

// CloseIdleConnections drains idle connections on the base round-tripper if it
// supports the optional interface, matching net/http's contract.
func (t *RuntimeTransport) CloseIdleConnections() {
	if closer, ok := t.base().(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func (t *RuntimeTransport) base() http.RoundTripper {
	if t == nil || t.Base == nil {
		return http.DefaultTransport
	}
	return t.Base
}

func (t *RuntimeTransport) initOTelInstruments() {
	t.otelOnce.Do(func() {
		t.latencyHist, _ = t.Meter.Float64Histogram(
			"mcp_adapter_rpc_duration_ms",
			metric.WithDescription("Adapter→runtime RPC round-trip duration in milliseconds"),
		)
		t.denialCount, _ = t.Meter.Int64Counter(
			"mcp_adapter_rpc_denials_total",
			metric.WithDescription("Total 4xx denial responses seen by the adapter"),
		)
	})
}

// retryableMethods lists the read-only MCP methods safe to replay on a
// transient gateway failure. tools/call and other mutating methods are never
// retried to avoid double-execution.
var retryableMethods = map[string]struct{}{
	"tools/list":      {},
	"resources/list":  {},
	"prompts/list":    {},
	"ping":            {},
	"server/discover": {},
}

func isRetryableMethod(method string) bool {
	_, ok := retryableMethods[method]
	return ok
}

func shouldRetryStatus(status int) bool {
	return status == http.StatusBadGateway || status == http.StatusGatewayTimeout
}

func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return !netErr.Timeout()
	}
	return strings.Contains(err.Error(), "connection reset by peer")
}

func retryBackoff(attempt int) time.Duration {
	if attempt <= 1 {
		return retryBaseDelay
	}
	d := retryBaseDelay
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= retryMaxDelay {
			return retryMaxDelay
		}
	}
	return d
}
