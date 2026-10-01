package main

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

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
	handleUpstreamError(recorder, req, errors.New("dial tcp: connection refused"))
	end()

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadGateway)
	}
	ended := spans.Ended()
	if len(ended) != 1 || ended[0].Status().Code != codes.Error {
		t.Fatalf("spans = %#v, want one error span", ended)
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
