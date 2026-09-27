package serviceutil

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// RequestMetrics records request throughput, errors, and latency for service
// HTTP handlers using bounded service, operation, status, and server labels.
type RequestMetrics struct {
	total    *prometheus.CounterVec
	errors   *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

func NewRequestMetrics(registerer prometheus.Registerer) *RequestMetrics {
	labels := []string{"service", "operation", "status", "server"}
	metrics := &RequestMetrics{
		total: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mcp_request_total",
			Help: "Total HTTP and MCP requests handled by MCP Runtime services.",
		}, labels),
		errors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mcp_request_errors_total",
			Help: "Total failed HTTP and MCP requests handled by MCP Runtime services.",
		}, labels),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "mcp_request_duration_seconds",
			Help:    "HTTP and MCP request duration in seconds.",
			Buckets: prometheus.DefBuckets,
		}, labels),
	}
	if registerer != nil {
		registerer.MustRegister(metrics.total, metrics.errors, metrics.duration)
	}
	return metrics
}

func DefaultRequestMetrics() *RequestMetrics {
	return NewRequestMetrics(prometheus.DefaultRegisterer)
}

// Middleware records requests by ServeMux's matched route pattern. Unmatched
// paths share one label value so arbitrary URLs never become metric labels.
func (m *RequestMetrics) Middleware(service string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		operation := r.Pattern
		if operation == "" {
			operation = "unmatched"
		}
		m.Record(service, operation, recorder.status, "none", time.Since(start))
	})
}

func (m *RequestMetrics) Record(service, operation string, status int, server string, duration time.Duration) {
	if m == nil {
		return
	}
	if service == "" {
		service = "unknown"
	}
	if operation == "" {
		operation = "unknown"
	}
	if server == "" {
		server = "none"
	}
	if status < 100 || status > 599 {
		status = http.StatusInternalServerError
	}
	labels := []string{service, operation, strconv.Itoa(status), server}
	m.total.WithLabelValues(labels...).Inc()
	if status >= http.StatusInternalServerError {
		m.errors.WithLabelValues(labels...).Inc()
	}
	m.duration.WithLabelValues(labels...).Observe(duration.Seconds())
}
