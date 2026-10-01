package serviceutil

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

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func newRecordedSpan(t *testing.T) (context.Context, *tracetest.SpanRecorder, func()) {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	ctx, span := provider.Tracer("test").Start(context.Background(), "op")
	return ctx, recorder, func() { span.End() }
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

func TestRecordSpanFailureMarksErrorSpan(t *testing.T) {
	ctx, recorder, end := newRecordedSpan(t)
	RecordSpanFailure(ctx, "gateway.request", "invalid_token", http.StatusUnauthorized, errors.New("boom"))
	end()

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	span := spans[0]
	if span.Status().Code != codes.Error || span.Status().Description != "invalid_token" {
		t.Fatalf("status = %#v, want Error/invalid_token", span.Status())
	}
	want := map[attribute.Key]string{
		"mcp.failure.operation": "gateway.request",
		"mcp.failure.reason":    "invalid_token",
	}
	for _, kv := range span.Attributes() {
		if w, ok := want[kv.Key]; ok {
			if kv.Value.AsString() != w {
				t.Fatalf("%s = %q, want %q", kv.Key, kv.Value.AsString(), w)
			}
			delete(want, kv.Key)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing attributes: %v", want)
	}
	if len(span.Events()) != 1 {
		t.Fatalf("events = %d, want 1 recorded error", len(span.Events()))
	}
}

func TestRecordSpanFailureNoSpanIsNoop(t *testing.T) {
	RecordSpanFailure(context.Background(), "op", "reason", 500, nil)
}

func TestLogfCtxAppendsTraceIDs(t *testing.T) {
	ctx, _, end := newRecordedSpan(t)
	defer end()
	buf := captureLog(t)

	LogfCtx(ctx, "hello %s", "world")
	got := buf.String()
	if !strings.Contains(got, "hello world trace_id="+TraceIDFromContext(ctx)) ||
		!strings.Contains(got, "span_id="+SpanIDFromContext(ctx)) {
		t.Fatalf("log line %q missing trace correlation", got)
	}

	buf.Reset()
	LogfCtx(context.Background(), "plain")
	if strings.Contains(buf.String(), "trace_id=") {
		t.Fatalf("log line %q should not carry trace ids without a span", buf.String())
	}
}

func TestLogRequestsRecordsAuthFailureOnSpan(t *testing.T) {
	ctx, recorder, end := newRecordedSpan(t)
	buf := captureLog(t)

	handler := LogRequests(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	req := httptest.NewRequest(http.MethodPost, "/token", nil).WithContext(ctx)
	handler.ServeHTTP(httptest.NewRecorder(), req)
	end()

	if !strings.Contains(buf.String(), "trace_id="+TraceIDFromContext(ctx)) {
		t.Fatalf("request log %q missing trace_id", buf.String())
	}
	if code := recorder.Ended()[0].Status().Code; code != codes.Error {
		t.Fatalf("span status = %v, want Error", code)
	}
}

func TestLogRequestsLeavesSuccessSpanUnset(t *testing.T) {
	ctx, recorder, end := newRecordedSpan(t)
	captureLog(t)
	handler := LogRequests(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil).WithContext(ctx))
	end()
	if code := recorder.Ended()[0].Status().Code; code != codes.Unset {
		t.Fatalf("span status = %v, want Unset for 404", code)
	}
}

func TestIsProbePath(t *testing.T) {
	for path, want := range map[string]bool{
		"/health": true, "/ready": true, "/healthz": true, "/readyz": true,
		"/livez": true, "/metrics": true, "/health/": true,
		"/mcp": false, "/oauth/token": false, "/healthcheck": false,
	} {
		if got := IsProbePath(httptest.NewRequest(http.MethodGet, path, nil)); got != want {
			t.Errorf("IsProbePath(%q) = %v, want %v", path, got, want)
		}
		if got := TraceableRequest(httptest.NewRequest(http.MethodGet, path, nil)); got == want {
			t.Errorf("TraceableRequest(%q) = %v, want %v", path, got, !want)
		}
	}
	if IsProbePath(nil) {
		t.Error("IsProbePath(nil) = true")
	}
}
