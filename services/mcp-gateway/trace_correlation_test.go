package main

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"mcp-runtime/pkg/serviceutil"
)

func startTestSpan(t *testing.T) (context.Context, *tracetest.SpanRecorder, *bytes.Buffer, func()) {
	t.Helper()
	spans := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	ctx, span := provider.Tracer("test").Start(context.Background(), "http.server")

	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return ctx, spans, &logs, func() { span.End() }
}

func TestHandleUpstreamErrorRecordsErrorSpanAndTraceLog(t *testing.T) {
	ctx, spans, logs, end := startTestSpan(t)

	req := httptest.NewRequest(http.MethodPost, "http://proxy.example.com/mcp", nil).WithContext(ctx)
	recorder := httptest.NewRecorder()
	handleUpstreamError(recorder, req, errors.New("dial tcp: connection refused; private-url-token"))
	end()

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadGateway)
	}
	if got := recorder.Result().Header.Get(serviceutil.TraceResponseHeader); got != serviceutil.TraceIDFromContext(ctx) {
		t.Fatalf("response trace ID = %q", got)
	}
	ended := spans.Ended()
	if len(ended) != 1 || ended[0].Status().Code != codes.Error {
		t.Fatalf("spans = %#v, want one error span", ended)
	}
	if strings.Contains(logs.String(), "private-url-token") {
		t.Fatal("upstream error detail leaked")
	}
	if !strings.Contains(logs.String(), "trace_id="+serviceutil.TraceIDFromContext(ctx)) {
		t.Fatalf("log %q missing trace_id", logs.String())
	}
}

func TestGatewayRejectedRequestRecordsErrorSpanAndTraceLog(t *testing.T) {
	issuer := newTestJWTIssuer(t)
	server := newTestGatewayServer(t, oauthPolicy(issuer.url), func(w http.ResponseWriter, _ *http.Request) {
		t.Error("rejected request must not reach upstream")
	})
	ctx, spans, logs, end := startTestSpan(t)

	// No bearer token: the OAuth-protected MCP request must be rejected.
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"secret":"s3cr3t"}}}`
	req := httptest.NewRequest(http.MethodPost, "http://proxy.example.com/mcp", strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.handleGateway(recorder, req)
	end()

	if got := recorder.Result().Header.Get(serviceutil.TraceResponseHeader); got != serviceutil.TraceIDFromContext(ctx) {
		t.Fatalf("denial trace ID = %q", got)
	}
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusUnauthorized, recorder.Body.String())
	}
	ended := spans.Ended()
	if len(ended) != 1 || ended[0].Status().Code != codes.Error {
		t.Fatalf("spans = %#v, want one error span", ended)
	}
	line := logs.String()
	if !strings.Contains(line, "trace_id="+serviceutil.TraceIDFromContext(ctx)) ||
		!strings.Contains(line, "gateway request rejected") ||
		!strings.Contains(line, "status=401") {
		t.Fatalf("log %q missing correlated rejection line", line)
	}
	if strings.Contains(line, "s3cr3t") {
		t.Fatalf("log %q leaked tool arguments", line)
	}
}

func TestGatewaySuccessfulCompletionHasCorrelatedBoundedLog(t *testing.T) {
	ctx, _, logs, end := startTestSpan(t)
	defer end()
	server := &gatewayServer{}
	request := httptest.NewRequest(http.MethodPost, "/mcp?token=private-url", nil).WithContext(ctx)
	ex := &Exchange{R: request, W: &statusRecorder{ResponseWriter: httptest.NewRecorder(), status: http.StatusOK}, StartTime: time.Now(), SkipAudit: true}
	ex.Inspection.Method = "arbitrary-private-method"
	server.recordGatewayOutcome(ex)
	line := logs.String()
	for _, want := range []string{"event=gateway.request", "status=200", `decision="skip"`, "trace_id=" + serviceutil.TraceIDFromContext(ctx)} {
		if !strings.Contains(line, want) {
			t.Errorf("log %q missing %q", line, want)
		}
	}
	for _, secret := range []string{"private-url", "arbitrary-private-method"} {
		if strings.Contains(line, secret) {
			t.Errorf("log leaked %q", secret)
		}
	}
}

func TestProxyCannotOverrideResponseTraceID(t *testing.T) {
	ctx, _, _, end := startTestSpan(t)
	defer end()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(serviceutil.TraceResponseHeader, "upstream-private-id")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()
	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	newUpstreamReverseProxy(target).ServeHTTP(serviceutil.CorrelateResponse(recorder, ctx), httptest.NewRequest(http.MethodGet, "/mcp", nil).WithContext(ctx))
	if recorder.Code != http.StatusOK || recorder.Body.String() != "ok" {
		t.Fatalf("response changed: %d %s", recorder.Code, recorder.Body.String())
	}
	values := recorder.Result().Header.Values(serviceutil.TraceResponseHeader)
	if len(values) != 1 || values[0] != serviceutil.TraceIDFromContext(ctx) {
		t.Fatalf("response trace IDs = %v", values)
	}
}

func TestGatewayRejectedUnknownMethodIsNotLoggedRaw(t *testing.T) {
	ctx, _, logs, end := startTestSpan(t)
	defer end()
	ex := &Exchange{R: httptest.NewRequest(http.MethodPost, "/mcp", nil).WithContext(ctx), W: &statusRecorder{ResponseWriter: httptest.NewRecorder(), status: http.StatusBadRequest}, StartTime: time.Now()}
	ex.Inspection.Method = "private-method-value"
	(&gatewayServer{}).recordGatewayOutcome(ex)
	if strings.Contains(logs.String(), "private-method-value") {
		t.Fatal("raw unknown method leaked into rejection log")
	}
	if !strings.Contains(logs.String(), `method="other"`) {
		t.Fatalf("normalized method missing from %s", logs.String())
	}
}
