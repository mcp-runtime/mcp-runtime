package main

import (
	"context"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/trace"
	policypkg "mcp-runtime/pkg/policy"
)

const maxMetricTools = 128

// Tool labels come only from the active policy inventory. Arbitrary client tool
// names must not create unbounded Prometheus series. The first 128 configured
// names have individual series; unknown/overflow names share a fixed bucket.
func metricToolName(policy *policypkg.Document, name string) string {
	if policy != nil {
		for i, tool := range policy.Tools {
			if i >= maxMetricTools {
				break
			}
			if string(tool.Name) == name && len(name) <= 128 {
				return name
			}
		}
	}
	return "__other__"
}

func (m *gatewayMetrics) recordToolCall(ctx context.Context, scope gatewayMetricScope, policy *policypkg.Document,
	name string, decision policypkg.Decision, status int, duration time.Duration) {
	if m == nil || name == "" {
		return
	}
	tool := metricToolName(policy, name)
	m.toolCallsTotal.WithLabelValues(scope.Namespace, scope.Server, tool, metricDecision(decision), metricReason(decision), strconv.Itoa(status)).Inc()
	observer := m.toolDurationSeconds.WithLabelValues(scope.Namespace, scope.Server, tool, metricDecision(decision))
	sc := trace.SpanContextFromContext(ctx)
	if sc.IsSampled() {
		if exemplar, ok := observer.(prometheus.ExemplarObserver); ok {
			exemplar.ObserveWithExemplar(duration.Seconds(), prometheus.Labels{"trace_id": sc.TraceID().String()})
			return
		}
	}
	observer.Observe(duration.Seconds())
}
