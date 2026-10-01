// Package serviceutil provides OpenTelemetry utilities for MCP services.
package serviceutil

import (
	"context"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/segmentio/kafka-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	"go.opentelemetry.io/otel/trace"
)

// OTLPTraceOptions configures OTLP HTTP exporter options.
// It sets up the endpoint URL and configures secure/insecure connections
// based on whether the endpoint uses HTTPS or HTTP.
func OTLPTraceOptions(endpoint string) []otlptracehttp.Option {
	insecure, insecureSet := BoolEnv("OTEL_EXPORTER_OTLP_INSECURE")
	if u, err := url.Parse(endpoint); err == nil {
		// Handle URLs with schemes (http://host:port/path)
		if u.Scheme != "" && u.Host == "" {
			// This is a scheme-less endpoint, fall through to treat as host:port
		} else if u.Scheme != "" && u.Host != "" {
			opts := []otlptracehttp.Option{otlptracehttp.WithEndpoint(u.Host)}
			if u.Path != "" {
				opts = append(opts, otlptracehttp.WithURLPath(u.Path))
			}
			if insecureSet {
				if insecure {
					opts = append(opts, otlptracehttp.WithInsecure())
				}
				return opts
			}
			if u.Scheme == "http" {
				opts = append(opts, otlptracehttp.WithInsecure())
			}
			return opts
		}
	}

	// Fallback: treat entire endpoint as host:port
	opts := []otlptracehttp.Option{otlptracehttp.WithEndpoint(endpoint)}
	if insecureSet {
		if insecure {
			opts = append(opts, otlptracehttp.WithInsecure())
		}
		return opts
	}
	return opts
}

// ConfigureTracePropagation enables W3C trace context and baggage propagation.
func ConfigureTracePropagation() {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
}

// CaptureTraceContext serializes the active trace context for async handoffs.
func CaptureTraceContext(ctx context.Context) map[string]string {
	if ctx == nil {
		ctx = context.Background()
	}
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	if len(carrier) == 0 {
		return nil
	}
	headers := make(map[string]string, len(carrier))
	for key, value := range carrier {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			continue
		}
		headers[key] = value
	}
	if len(headers) == 0 {
		return nil
	}
	return headers
}

// ContextWithTraceContext extracts serialized trace context into ctx.
func ContextWithTraceContext(ctx context.Context, headers map[string]string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(headers) == 0 {
		return ctx
	}
	return otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier(headers))
}

// InjectKafkaHeaders appends the active trace context to Kafka headers.
func InjectKafkaHeaders(ctx context.Context, headers []kafka.Header) []kafka.Header {
	traceHeaders := CaptureTraceContext(ctx)
	if len(traceHeaders) == 0 {
		return headers
	}
	keys := make([]string, 0, len(traceHeaders))
	for key := range traceHeaders {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		headers = append(headers, kafka.Header{Key: key, Value: []byte(traceHeaders[key])})
	}
	return headers
}

// ExtractKafkaHeaders extracts trace context from Kafka headers into ctx.
func ExtractKafkaHeaders(ctx context.Context, headers []kafka.Header) context.Context {
	if len(headers) == 0 {
		return ContextWithTraceContext(ctx, nil)
	}
	carrier := make(map[string]string, len(headers))
	for _, header := range headers {
		key := strings.ToLower(strings.TrimSpace(header.Key))
		if key == "" || len(header.Value) == 0 {
			continue
		}
		carrier[key] = string(header.Value)
	}
	return ContextWithTraceContext(ctx, carrier)
}

// TraceIDFromContext returns the active span trace ID, or an empty string when
// the context does not carry a valid trace.
func TraceIDFromContext(ctx context.Context) string {
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return ""
	}
	return spanContext.TraceID().String()
}

// InitTracer initializes OpenTelemetry tracing from OTEL_* environment variables.
func InitTracer(serviceName string) (func(context.Context) error, error) {
	ConfigureTracePropagation()
	ConfigureOTelDiagnostics()

	if envName := strings.TrimSpace(os.Getenv("OTEL_SERVICE_NAME")); envName != "" {
		serviceName = envName
	}
	endpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	if endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}

	exporter, err := otlptracehttp.New(context.Background(), OTLPTraceOptions(endpoint)...)
	if err != nil {
		return nil, err
	}

	res, err := resource.New(context.Background(),
		resource.WithAttributes(semconv.ServiceName(serviceName)),
	)
	if err != nil {
		return nil, err
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(provider)
	return provider.Shutdown, nil
}
