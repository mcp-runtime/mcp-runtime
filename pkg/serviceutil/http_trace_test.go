package serviceutil

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestDoHTTPPropagatesParentAndPreservesClient(t *testing.T) {
	previous := otel.GetTracerProvider()
	propagator := otel.GetTextMapPropagator()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	ConfigureTracePropagation()
	t.Cleanup(func() { otel.SetTracerProvider(previous); otel.SetTextMapPropagator(propagator) })
	ctx, parent := provider.Tracer("test").Start(context.Background(), "caller")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Traceparent"), parent.SpanContext().TraceID().String()) {
			t.Errorf("missing parent trace context")
		}
		if r.Header.Get("Authorization") != "Bearer private-token" {
			t.Errorf("instrumentation changed authentication")
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()
	client := server.Client()
	transport := client.Transport
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	request.Header.Set("Authorization", "Bearer private-token")
	response, err := DoHTTP(client, request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	parent.End()
	if client.Transport != transport {
		t.Fatal("shared client mutated")
	}
	spans := recorder.Ended()
	if len(spans) != 2 || spans[0].Parent().SpanID() != parent.SpanContext().SpanID() {
		t.Fatalf("expected connected client and parent spans, got %d", len(spans))
	}
	for _, span := range spans {
		for _, attr := range span.Attributes() {
			if strings.Contains(attr.Value.AsString(), "private-token") {
				t.Fatal("credential recorded in span")
			}
		}
	}
	if len(spans[0].Events()) == 0 {
		t.Fatal("connection phases missing")
	}
}
