package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestGatewayTraceStopsAtFailedAuthentication(t *testing.T) {
	previous := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(previous) })
	ctx, root := provider.Tracer("test").Start(context.Background(), "request")
	issuer := newTestJWTIssuer(t)
	server := newTestGatewayServer(t, oauthPolicy(issuer.url), func(http.ResponseWriter, *http.Request) {
		t.Error("denied call reached upstream")
	})
	request := httptest.NewRequest(http.MethodPost, "http://gateway/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"password":"private-value"}}}`)).WithContext(ctx)
	response := httptest.NewRecorder()
	server.handleGateway(response, request)
	root.End()
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", response.Code)
	}
	spans := recorder.Ended()
	if len(spans) != 4 {
		t.Fatalf("expected inspect, policy, auth and request spans, got %d", len(spans))
	}
	for i, name := range []string{"gateway.inspect", "gateway.policy", "gateway.auth"} {
		span := spans[i]
		if span.Name() != name || span.Parent().SpanID() != root.SpanContext().SpanID() {
			t.Fatalf("unexpected stage trace: %s", span.Name())
		}
		for _, attr := range span.Attributes() {
			if strings.Contains(attr.Value.AsString(), "private-value") {
				t.Fatal("payload leaked into trace")
			}
		}
	}
	if spans[2].Status().Code != codes.Error || spans[3].Status().Code != codes.Error {
		t.Fatal("denial missing error status")
	}
}
