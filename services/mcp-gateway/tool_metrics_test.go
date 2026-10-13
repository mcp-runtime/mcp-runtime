package main

import (
	"context"
	"fmt"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.opentelemetry.io/otel/trace"
	policypkg "mcp-runtime/pkg/policy"
	"testing"
	"time"
)

func TestToolMetricsBoundUnknownNamesAndLinkSampledTraces(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := newGatewayMetrics(registry)
	policy := &policypkg.Document{Tools: []policypkg.Tool{{Name: "echo"}}}
	scope := gatewayMetricScope{Namespace: "team-a", Server: "buddy"}
	sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: trace.FlagsSampled})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)
	metrics.recordToolCall(ctx, scope, policy, "echo", policypkg.Decision{Allowed: true, Reason: "allowed"}, 200, 2*time.Second)
	if got := testutil.ToFloat64(metrics.toolCallsTotal.WithLabelValues("team-a", "buddy", "echo", "allow", "allowed", "200")); got != 1 {
		t.Fatalf("counter=%v", got)
	}
	for i := 0; i < 1000; i++ {
		metrics.recordToolCall(context.Background(), scope, policy, fmt.Sprint("untrusted-", i), policypkg.Decision{Reason: "tool_denied"}, 403, time.Millisecond)
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() == "mcp_gateway_tool_call_total" && len(family.Metric) != 2 {
			t.Fatalf("unbounded names created %d series", len(family.Metric))
		}
		if family.GetName() == "mcp_gateway_tool_call_duration_seconds" {
			found := false
			for _, metric := range family.Metric {
				for _, bucket := range metric.Histogram.Bucket {
					if bucket.Exemplar != nil {
						found = true
						if bucket.Exemplar.Label[0].GetValue() != sc.TraceID().String() {
							t.Fatal("wrong trace exemplar")
						}
					}
				}
			}
			if !found {
				t.Fatal("sampled trace exemplar missing")
			}
		}
	}
}

func TestToolMetricsConfiguredLimit(t *testing.T) {
	policy := &policypkg.Document{}
	for i := 0; i < maxMetricTools+1; i++ {
		policy.Tools = append(policy.Tools, policypkg.Tool{Name: policypkg.ToolName(fmt.Sprint("tool-", i))})
	}
	if metricToolName(policy, "tool-127") != "tool-127" || metricToolName(policy, "tool-128") != "__other__" {
		t.Fatal("inventory limit not enforced")
	}
}
