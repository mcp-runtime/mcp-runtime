package serviceutil

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// SpanIDFromContext returns the active span ID, or an empty string when the
// context does not carry a valid span.
func SpanIDFromContext(ctx context.Context) string {
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return ""
	}
	return spanContext.SpanID().String()
}

// TraceLogSuffix returns " trace_id=<id> span_id=<id>" for the active span, or
// an empty string when there is none. Append it to log lines so logs can be
// joined to traces by ID (for example Loki to Tempo in Grafana).
func TraceLogSuffix(ctx context.Context) string {
	traceID := TraceIDFromContext(ctx)
	if traceID == "" {
		return ""
	}
	return " trace_id=" + traceID + " span_id=" + SpanIDFromContext(ctx)
}

// LogfCtx logs like log.Printf and appends trace_id/span_id when ctx carries a
// span.
func LogfCtx(ctx context.Context, format string, args ...any) {
	// #nosec G706 -- operational logs; callers pass bounded, non-secret values.
	log.Print(fmt.Sprintf(format, args...) + TraceLogSuffix(ctx))
}

// RecordSpanFailure marks the active span as failed so error spans are
// searchable. operation and reason must be bounded, non-secret identifiers
// (never tokens, headers, or MCP tool arguments). status is the HTTP status
// returned to the caller, or 0 when unknown. A nil err is allowed.
func RecordSpanFailure(ctx context.Context, operation, reason string, status int, err error) {
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return
	}
	attrs := []attribute.KeyValue{attribute.String("mcp.failure.operation", operation)}
	if reason != "" {
		attrs = append(attrs, attribute.String("mcp.failure.reason", reason))
	}
	if status > 0 {
		attrs = append(attrs, attribute.Int("http.response.status_code", status))
	}
	span.SetAttributes(attrs...)
	description := reason
	if description == "" {
		description = operation
	}
	if err != nil {
		span.RecordError(err)
	}
	span.SetStatus(codes.Error, description)
}

// IsProbePath reports whether the request is a health, readiness, or metrics
// probe. Probes are excluded from tracing so they do not drown failure traces.
func IsProbePath(r *http.Request) bool {
	if r == nil || r.URL == nil {
		return false
	}
	switch strings.TrimRight(r.URL.Path, "/") {
	case "/health", "/healthz", "/ready", "/readyz", "/livez", "/metrics":
		return true
	}
	return false
}

// TraceableRequest is an otelhttp filter that skips probe requests.
func TraceableRequest(r *http.Request) bool { return !IsProbePath(r) }

// failureStatus reports whether an HTTP status should mark the span as failed.
// 401/403 are included because auth rejections are the main OAuth failure path.
func failureStatus(status int) bool {
	return status == http.StatusUnauthorized || status == http.StatusForbidden || status >= 500
}

var (
	otelErrorsOnce  sync.Once
	otelInternalErr = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "mcp_otel_internal_errors_total",
		Help: "OpenTelemetry SDK internal errors (trace export failures, drops).",
	})
)

// ConfigureOTelDiagnostics routes OpenTelemetry SDK internal errors, such as
// failed OTLP exports, to the service log and a Prometheus counter, so a
// silently unreachable collector is visible. It is safe to call repeatedly.
func ConfigureOTelDiagnostics() {
	otelErrorsOnce.Do(func() {
		_ = prometheus.Register(otelInternalErr)
		otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
			otelInternalErr.Inc()
			log.Printf("otel internal error: %v", err)
		}))
	})
}
