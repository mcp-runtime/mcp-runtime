package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/felixge/httpsnoop"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func initExampleTracing() (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	endpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	if endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}
	exporter, err := otlptracehttp.New(context.Background(), otlptracehttp.WithEndpointURL(endpoint))
	if err != nil {
		return nil, err
	}
	name := envOr("OTEL_SERVICE_NAME", "oauth-example-mcp")
	provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(exampleResource(name)))
	otel.SetTracerProvider(provider)
	return provider.Shutdown, nil
}

func tracedExampleHTTP(next http.Handler) http.Handler {
	logged := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		metrics := httpsnoop.CaptureMetrics(next, w, r)
		if r.URL.Path == "/health" {
			return
		}
		sc := trace.SpanContextFromContext(r.Context())
		log.Printf("mcp http method=%q status=%d duration=%s trace_id=%s span_id=%s", r.Method, metrics.Code, metrics.Duration, sc.TraceID(), sc.SpanID())
	})
	return otelhttp.NewHandler(logged, "mcp.server.http", otelhttp.WithFilter(func(r *http.Request) bool {
		return r.URL.Path != "/health"
	}))
}

// Trace MCP execution, not tool arguments/results. Third-party clients may
// supply traceparent; transport/session identity is never used as trace data.
func traceMCPMethods(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
		if !trace.SpanContextFromContext(ctx).IsValid() && request.GetExtra() != nil {
			ctx = otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(request.GetExtra().Header))
		}
		ctx, span := otel.Tracer("mcp-runtime/example").Start(ctx, "mcp."+method)
		defer span.End()
		span.SetAttributes(attribute.String("mcp.method.name", method))
		tool := ""
		switch params := request.GetParams().(type) {
		case *mcp.CallToolParams:
			tool = params.Name
		case *mcp.CallToolParamsRaw:
			tool = params.Name
		}
		if len(tool) > 0 && len(tool) <= 128 {
			span.SetAttributes(attribute.String("gen_ai.tool.name", tool))
		}
		start := time.Now()
		result, err := next(ctx, method, request)
		failed := err != nil
		if call, ok := result.(*mcp.CallToolResult); ok {
			failed = failed || call.IsError
		}
		if failed {
			span.SetStatus(codes.Error, "MCP method failed")
		}
		sc := span.SpanContext()
		log.Printf("mcp execution method=%q failed=%t duration=%s trace_id=%s span_id=%s",
			method, failed, time.Since(start), sc.TraceID(), sc.SpanID())
		return result, err
	}
}

func exampleResource(name string) *resource.Resource {
	res, _ := resource.New(context.Background(), resource.WithFromEnv(), resource.WithAttributes(attribute.String("service.name", name)))
	return res
}
