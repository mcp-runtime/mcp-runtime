package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type readinessFixture struct {
	readiness *kafkaReadiness
	registry  *prometheus.Registry
	clock     *fakeClock
	lookups   []string
	dials     []string
	mu        sync.Mutex
}

func newReadinessFixture(t *testing.T, brokers []string, lookupErr, dialErr error) *readinessFixture {
	t.Helper()
	f := &readinessFixture{
		registry: prometheus.NewRegistry(),
		clock:    &fakeClock{now: time.Unix(1_700_000_000, 0)},
	}
	f.readiness = newKafkaReadiness(brokers, newKafkaReadinessMetrics(f.registry))
	f.readiness.now = f.clock.Now
	f.readiness.lookupHost = func(_ context.Context, host string) ([]string, error) {
		f.mu.Lock()
		f.lookups = append(f.lookups, host)
		f.mu.Unlock()
		f.clock.Advance(30 * time.Millisecond)
		if lookupErr != nil {
			return nil, lookupErr
		}
		return []string{"10.0.0.7"}, nil
	}
	f.readiness.dial = func(_ context.Context, _, address string) (net.Conn, error) {
		f.mu.Lock()
		f.dials = append(f.dials, address)
		f.mu.Unlock()
		f.clock.Advance(5 * time.Millisecond)
		if dialErr != nil {
			return nil, dialErr
		}
		client, server := net.Pipe()
		_ = server.Close()
		return client, nil
	}
	return f
}

func TestKafkaReadinessPendingBeforeFirstCheck(t *testing.T) {
	t.Parallel()

	f := newReadinessFixture(t, []string{"kafka:9092"}, nil, nil)
	ok, reason := f.readiness.Status()
	if ok || reason != readinessReasonPending {
		t.Fatalf("Status() = %v, %q; want false, %q", ok, reason, readinessReasonPending)
	}

	recorder := httptest.NewRecorder()
	(&ingestServer{readiness: f.readiness}).handleReady(recorder, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(recorder.Body.String(), `"reason":"pending"`) {
		t.Fatalf("body = %q, want reason pending", recorder.Body.String())
	}
}

func TestKafkaReadinessSuccessIsCachedAndRecorded(t *testing.T) {
	t.Parallel()

	f := newReadinessFixture(t, []string{"kafka.mcp-observability.svc.cluster.local:9092"}, nil, nil)
	f.readiness.check(context.Background())

	ok, reason := f.readiness.Status()
	if !ok || reason != "" {
		t.Fatalf("Status() = %v, %q; want true, empty", ok, reason)
	}
	if got := strings.Join(f.dials, ","); got != "10.0.0.7:9092" {
		t.Fatalf("dials = %q, want resolved address 10.0.0.7:9092", got)
	}

	// /ready serves the cached state without another lookup or dial.
	recorder := httptest.NewRecorder()
	(&ingestServer{readiness: f.readiness}).handleReady(recorder, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if len(f.lookups) != 1 || len(f.dials) != 1 {
		t.Fatalf("lookups=%d dials=%d after /ready, want 1 each", len(f.lookups), len(f.dials))
	}

	families := gatherMetrics(t, f.registry)
	for _, phase := range []string{"dns", "connect", "total"} {
		if count := histogramCount(families, "mcp_ingest_kafka_readiness_check_duration_seconds", "phase", phase); count != 1 {
			t.Fatalf("duration{phase=%q} count = %d, want 1", phase, count)
		}
	}
	if sum := histogramSum(families, "mcp_ingest_kafka_readiness_check_duration_seconds", "phase", "dns"); sum < 0.029 || sum > 0.031 {
		t.Fatalf("dns duration sum = %v, want ~0.030", sum)
	}
	if v := gaugeValue(families, "mcp_ingest_kafka_ready"); v != 1 {
		t.Fatalf("mcp_ingest_kafka_ready = %v, want 1", v)
	}
	if _, found := findMetricFamily(families, "mcp_ingest_kafka_readiness_check_failures_total"); found {
		t.Fatal("failure counter should have no series after a successful check")
	}
}

func TestKafkaReadinessFailureReasons(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		brokers   []string
		lookupErr error
		dialErr   error
		want      string
	}{
		{name: "no brokers", brokers: []string{" ", ""}, want: readinessReasonNoBrokers},
		{name: "dns not found", brokers: []string{"kafka:9092"}, lookupErr: &net.DNSError{Err: "no such host", Name: "kafka", IsNotFound: true}, want: readinessReasonDNS},
		{name: "dns timeout", brokers: []string{"kafka:9092"}, lookupErr: &net.DNSError{Err: "i/o timeout", Name: "kafka", IsTimeout: true}, want: readinessReasonDNSTimeout},
		{name: "connect refused", brokers: []string{"kafka:9092"}, dialErr: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, want: readinessReasonRefused},
		{name: "connect deadline", brokers: []string{"kafka:9092"}, dialErr: fmt.Errorf("dial: %w", context.DeadlineExceeded), want: readinessReasonTimeout},
		{name: "other dial error", brokers: []string{"kafka:9092"}, dialErr: errors.New("boom"), want: readinessReasonDial},
		{name: "invalid broker address", brokers: []string{"kafka"}, want: readinessReasonDial},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := newReadinessFixture(t, tc.brokers, tc.lookupErr, tc.dialErr)
			f.readiness.check(context.Background())

			ok, reason := f.readiness.Status()
			if ok || reason != tc.want {
				t.Fatalf("Status() = %v, %q; want false, %q", ok, reason, tc.want)
			}

			recorder := httptest.NewRecorder()
			(&ingestServer{readiness: f.readiness}).handleReady(recorder, httptest.NewRequest(http.MethodGet, "/ready", nil))
			if recorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
			}
			body := recorder.Body.String()
			if !strings.Contains(body, `"error":"kafka_unavailable"`) || !strings.Contains(body, `"reason":"`+tc.want+`"`) {
				t.Fatalf("body = %q, want kafka_unavailable with reason %q", body, tc.want)
			}

			families := gatherMetrics(t, f.registry)
			if v := counterValue(families, "mcp_ingest_kafka_readiness_check_failures_total", "reason", tc.want); v != 1 {
				t.Fatalf("failures{reason=%q} = %v, want 1", tc.want, v)
			}
			if v := gaugeValue(families, "mcp_ingest_kafka_ready"); v != 0 {
				t.Fatalf("mcp_ingest_kafka_ready = %v, want 0", v)
			}
		})
	}
}

func TestKafkaReadinessFallsBackToNextBroker(t *testing.T) {
	t.Parallel()

	f := newReadinessFixture(t, []string{"kafka-0:9092", "10.0.0.9:9092"}, &net.DNSError{Err: "no such host", Name: "kafka-0", IsNotFound: true}, nil)
	f.readiness.check(context.Background())

	if ok, reason := f.readiness.Status(); !ok {
		t.Fatalf("Status() = false, %q; want ready via second broker", reason)
	}
	if got := strings.Join(f.lookups, ","); got != "kafka-0" {
		t.Fatalf("lookups = %q, want only kafka-0 (IP brokers skip DNS)", got)
	}
	if got := strings.Join(f.dials, ","); got != "10.0.0.9:9092" {
		t.Fatalf("dials = %q, want 10.0.0.9:9092", got)
	}
}

func TestKafkaReadinessExpiresStaleResult(t *testing.T) {
	t.Parallel()

	f := newReadinessFixture(t, []string{"kafka:9092"}, nil, nil)
	f.readiness.check(context.Background())
	if ok, _ := f.readiness.Status(); !ok {
		t.Fatal("expected ready after a successful check")
	}

	f.clock.Advance(f.readiness.maxStaleness)
	if ok, _ := f.readiness.Status(); !ok {
		t.Fatal("expected ready exactly at the staleness bound")
	}
	f.clock.Advance(time.Millisecond)
	if ok, reason := f.readiness.Status(); ok || reason != readinessReasonStale {
		t.Fatalf("Status() = %v, %q; want false, %q", ok, reason, readinessReasonStale)
	}

	// A fresh success clears the stale state.
	f.readiness.check(context.Background())
	if ok, reason := f.readiness.Status(); !ok {
		t.Fatalf("Status() = false, %q after refresh; want ready", reason)
	}
}

func TestKafkaReadinessRecoversAfterFailure(t *testing.T) {
	t.Parallel()

	f := newReadinessFixture(t, []string{"kafka:9092"}, nil, nil)
	var fail bool
	dial := f.readiness.dial
	f.readiness.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		if fail {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
		}
		return dial(ctx, network, address)
	}

	fail = true
	f.readiness.check(context.Background())
	if ok, reason := f.readiness.Status(); ok || reason != readinessReasonRefused {
		t.Fatalf("Status() = %v, %q; want false, refused", ok, reason)
	}
	fail = false
	f.readiness.check(context.Background())
	if ok, reason := f.readiness.Status(); !ok || reason != "" {
		t.Fatalf("Status() = %v, %q; want true after recovery", ok, reason)
	}
}

func TestKafkaReadinessCheckHonorsTimeout(t *testing.T) {
	t.Parallel()

	f := newReadinessFixture(t, []string{"kafka:9092"}, nil, nil)
	f.readiness.timeout = 20 * time.Millisecond
	f.readiness.lookupHost = func(ctx context.Context, host string) ([]string, error) {
		<-ctx.Done()
		return nil, &net.DNSError{Err: ctx.Err().Error(), Name: host, IsTimeout: true}
	}

	done := make(chan struct{})
	go func() {
		f.readiness.check(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("check did not return after its timeout")
	}
	if ok, reason := f.readiness.Status(); ok || reason != readinessReasonDNSTimeout {
		t.Fatalf("Status() = %v, %q; want false, %q", ok, reason, readinessReasonDNSTimeout)
	}
}

func TestKafkaReadinessRunChecksImmediatelyAndStops(t *testing.T) {
	t.Parallel()

	f := newReadinessFixture(t, []string{"kafka:9092"}, nil, nil)
	f.readiness.interval = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		f.readiness.Run(ctx)
		close(done)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if ok, _ := f.readiness.Status(); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Run did not perform an initial check")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after context cancellation")
	}
}

func gatherMetrics(t *testing.T, registry *prometheus.Registry) []*dto.MetricFamily {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("Gather() error = %v", err)
	}
	return families
}

func findMetricFamily(families []*dto.MetricFamily, name string) (*dto.MetricFamily, bool) {
	for _, family := range families {
		if family.GetName() == name {
			return family, true
		}
	}
	return nil, false
}

func findMetric(families []*dto.MetricFamily, name, label, value string) *dto.Metric {
	family, ok := findMetricFamily(families, name)
	if !ok {
		return nil
	}
	for _, metric := range family.GetMetric() {
		if label == "" {
			return metric
		}
		for _, pair := range metric.GetLabel() {
			if pair.GetName() == label && pair.GetValue() == value {
				return metric
			}
		}
	}
	return nil
}

func histogramCount(families []*dto.MetricFamily, name, label, value string) uint64 {
	if metric := findMetric(families, name, label, value); metric != nil {
		return metric.GetHistogram().GetSampleCount()
	}
	return 0
}

func histogramSum(families []*dto.MetricFamily, name, label, value string) float64 {
	if metric := findMetric(families, name, label, value); metric != nil {
		return metric.GetHistogram().GetSampleSum()
	}
	return 0
}

func counterValue(families []*dto.MetricFamily, name, label, value string) float64 {
	if metric := findMetric(families, name, label, value); metric != nil {
		return metric.GetCounter().GetValue()
	}
	return 0
}

func gaugeValue(families []*dto.MetricFamily, name string) float64 {
	if metric := findMetric(families, name, "", ""); metric != nil {
		return metric.GetGauge().GetValue()
	}
	return -1
}
