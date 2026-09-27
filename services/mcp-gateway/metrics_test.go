package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	policypkg "mcp-runtime/pkg/policy"
)

func TestPolicyReloadMetrics(t *testing.T) {
	failBefore := testutil.ToFloat64(policyReloadTotal.WithLabelValues("failure"))
	recordPolicyReloadFailure()
	if got := testutil.ToFloat64(policyReloadTotal.WithLabelValues("failure")) - failBefore; got != 1 {
		t.Fatalf("failure counter delta = %v, want 1", got)
	}

	successBefore := testutil.ToFloat64(policyReloadTotal.WithLabelValues("success"))
	at := time.Unix(1_700_000_000, 0)
	recordPolicyReloadSuccess("sha256:revA", "v1", at)
	if got := testutil.ToFloat64(policyReloadTotal.WithLabelValues("success")) - successBefore; got != 1 {
		t.Fatalf("success counter delta = %v, want 1", got)
	}
	if got := testutil.ToFloat64(policyLastSuccessTimestamp); got != float64(at.Unix()) {
		t.Fatalf("last success timestamp = %v, want %v", got, at.Unix())
	}
	if got := testutil.ToFloat64(policyActiveRevisionInfo.WithLabelValues("sha256:revA", "v1")); got != 1 {
		t.Fatalf("active revision info = %v, want 1", got)
	}

	// A new active revision replaces the prior series (Reset on success).
	recordPolicyReloadSuccess("sha256:revB", "v1", at)
	if got := testutil.ToFloat64(policyActiveRevisionInfo.WithLabelValues("sha256:revA", "v1")); got != 0 {
		t.Fatalf("stale revision info = %v, want 0 after new revision activated", got)
	}
}

func TestGatewayExportsCommonMCPRequestMetrics(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := newGatewayMetrics(registry)
	metrics.recordRequest(
		gatewayMetricScope{Server: "demo"},
		httptest.NewRequest(http.MethodPost, "http://proxy.example.com/mcp", nil),
		"tools/call",
		policypkg.Decision{Allowed: true, Status: http.StatusOK},
		http.StatusOK,
		time.Millisecond,
		0,
		0,
	)

	body := gatherMetricsText(t, registry)
	for _, want := range []string{
		"mcp_request_total",
		"mcp_request_duration_seconds_bucket",
		`service="mcp-gateway"`,
		`operation="tools/call"`,
		`server="demo"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output missing %q", want)
		}
	}
}
