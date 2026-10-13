package main

import (
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"testing"
)

func TestToolExecutionTraceSharesParentAndMarksToolErrors(t *testing.T) {
	previous := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(previous) })
	ctx, root := provider.Tracer("test").Start(context.Background(), "gateway.upstream")
	handler := traceMCPMethods(func(context.Context, string, mcp.Request) (mcp.Result, error) {
		return &mcp.CallToolResult{IsError: true}, nil
	})
	_, err := handler(ctx, "tools/call", &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "echo", Arguments: json.RawMessage(`{"secret":"private"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	root.End()
	spans := recorder.Ended()
	if len(spans) != 2 || spans[0].Parent().SpanID() != root.SpanContext().SpanID() || spans[0].Status().Code != codes.Error {
		t.Fatalf("unexpected trace: %v", spans)
	}
	for _, attr := range spans[0].Attributes() {
		if attr.Key == "secret" || attr.Value.AsString() == "private" {
			t.Fatal("payload recorded")
		}
	}
}
