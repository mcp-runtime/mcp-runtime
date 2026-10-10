package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	// kafkaReadinessInterval is how often the background checker probes the
	// configured Kafka brokers.
	kafkaReadinessInterval = 5 * time.Second
	// kafkaReadinessTimeout bounds one full check (DNS plus TCP connect) across
	// all configured brokers.
	kafkaReadinessTimeout = 2 * time.Second
	// kafkaReadinessMaxStaleness is the oldest cached result /ready trusts. It
	// covers a few missed intervals so one slow check does not flap readiness,
	// while a wedged checker still turns the pod unready.
	kafkaReadinessMaxStaleness = 3*kafkaReadinessInterval + kafkaReadinessTimeout
	// kafkaReadinessSlowThreshold logs per-phase timings for a check that is
	// slow but still succeeds, so DNS versus connect latency is visible in logs.
	kafkaReadinessSlowThreshold = time.Second
)

// Readiness failure reasons. They are bounded metric label values and are
// also returned in the /ready body.
const (
	readinessReasonPending    = "pending"
	readinessReasonStale      = "stale"
	readinessReasonNoBrokers  = "no_brokers"
	readinessReasonDNS        = "dns"
	readinessReasonDNSTimeout = "dns_timeout"
	readinessReasonTimeout    = "connect_timeout"
	readinessReasonRefused    = "refused"
	readinessReasonDial       = "dial_error"
)

type kafkaReadinessMetrics struct {
	duration *prometheus.HistogramVec
	failures *prometheus.CounterVec
	ready    prometheus.Gauge
}

func newKafkaReadinessMetrics(registerer prometheus.Registerer) *kafkaReadinessMetrics {
	m := &kafkaReadinessMetrics{
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "mcp_ingest_kafka_readiness_check_duration_seconds",
			Help:    "Duration of the ingest Kafka readiness check by phase (dns, connect, total).",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 5},
		}, []string{"phase"}),
		failures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mcp_ingest_kafka_readiness_check_failures_total",
			Help: "Failed ingest Kafka readiness checks by reason.",
		}, []string{"reason"}),
		ready: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mcp_ingest_kafka_ready",
			Help: "1 when the last ingest Kafka readiness check succeeded, 0 otherwise.",
		}),
	}
	if registerer != nil {
		registerer.MustRegister(m.duration, m.failures, m.ready)
	}
	return m
}

func (m *kafkaReadinessMetrics) observe(phase string, d time.Duration) {
	if m != nil {
		m.duration.WithLabelValues(phase).Observe(d.Seconds())
	}
}

// kafkaReadiness checks broker reachability in the background and caches the
// result, so /ready answers immediately instead of making a DNS lookup and TCP
// dial inside the kubelet's probe timeout.
type kafkaReadiness struct {
	brokers      []string
	interval     time.Duration
	timeout      time.Duration
	maxStaleness time.Duration
	lookupHost   func(ctx context.Context, host string) ([]string, error)
	dial         func(ctx context.Context, network, address string) (net.Conn, error)
	now          func() time.Time
	metrics      *kafkaReadinessMetrics

	mu        sync.RWMutex
	checked   bool
	ok        bool
	reason    string
	checkedAt time.Time
}

func newKafkaReadiness(brokers []string, metrics *kafkaReadinessMetrics) *kafkaReadiness {
	dialer := &net.Dialer{}
	return &kafkaReadiness{
		brokers:      brokers,
		interval:     kafkaReadinessInterval,
		timeout:      kafkaReadinessTimeout,
		maxStaleness: kafkaReadinessMaxStaleness,
		lookupHost:   net.DefaultResolver.LookupHost,
		dial:         dialer.DialContext,
		now:          time.Now,
		metrics:      metrics,
	}
}

// Run checks immediately, then on every interval until ctx is cancelled.
func (k *kafkaReadiness) Run(ctx context.Context) {
	k.check(ctx)
	ticker := time.NewTicker(k.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			k.check(ctx)
		}
	}
}

// Status returns the cached readiness and, when not ready, a bounded reason.
func (k *kafkaReadiness) Status() (bool, string) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	if !k.checked {
		return false, readinessReasonPending
	}
	if k.now().Sub(k.checkedAt) > k.maxStaleness {
		return false, readinessReasonStale
	}
	if !k.ok {
		return false, k.reason
	}
	return true, ""
}

func (k *kafkaReadiness) check(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, k.timeout)
	defer cancel()

	start := k.now()
	timings, err := k.probe(ctx)
	elapsed := k.now().Sub(start)
	k.metrics.observe("total", elapsed)

	if parent.Err() != nil {
		// Shutting down; do not record a spurious failure.
		return
	}

	reason := ""
	if err != nil {
		reason = classifyReadinessError(err)
		if k.metrics != nil {
			k.metrics.failures.WithLabelValues(reason).Inc()
		}
	}
	if k.metrics != nil {
		if err == nil {
			k.metrics.ready.Set(1)
		} else {
			k.metrics.ready.Set(0)
		}
	}

	k.mu.Lock()
	wasOK, wasChecked := k.ok, k.checked
	k.checked = true
	k.ok = err == nil
	k.reason = reason
	k.checkedAt = k.now()
	k.mu.Unlock()

	switch {
	case err != nil && (wasOK || !wasChecked):
		log.Printf("kafka readiness: not ready reason=%s elapsed=%s %s err=%v", reason, elapsed, timings, err)
	case err == nil && wasChecked && !wasOK:
		log.Printf("kafka readiness: ready elapsed=%s %s", elapsed, timings)
	case err == nil && elapsed >= kafkaReadinessSlowThreshold:
		log.Printf("kafka readiness: slow check elapsed=%s %s", elapsed, timings)
	}
}

// probe resolves and dials each broker until one accepts a TCP connection.
// DNS and connect are timed separately so slow probes can be attributed.
func (k *kafkaReadiness) probe(ctx context.Context) (string, error) {
	var lastErr error
	var timings []string
	for _, broker := range k.brokers {
		broker = strings.TrimSpace(broker)
		if broker == "" {
			continue
		}
		host, port, err := net.SplitHostPort(broker)
		if err != nil {
			lastErr = err
			continue
		}

		addrs := []string{host}
		if net.ParseIP(host) == nil {
			dnsStart := k.now()
			addrs, err = k.lookupHost(ctx, host)
			dnsElapsed := k.now().Sub(dnsStart)
			k.metrics.observe("dns", dnsElapsed)
			timings = append(timings, fmt.Sprintf("%s dns=%s", broker, dnsElapsed))
			if err != nil {
				lastErr = err
				continue
			}
		}

		for _, addr := range addrs {
			connectStart := k.now()
			conn, err := k.dial(ctx, "tcp", net.JoinHostPort(addr, port))
			connectElapsed := k.now().Sub(connectStart)
			k.metrics.observe("connect", connectElapsed)
			timings = append(timings, fmt.Sprintf("%s connect=%s", broker, connectElapsed))
			if err == nil {
				_ = conn.Close()
				return strings.Join(timings, " "), nil
			}
			lastErr = err
		}
	}
	if lastErr == nil {
		return "", errNoKafkaBrokers
	}
	return strings.Join(timings, " "), lastErr
}

var errNoKafkaBrokers = errors.New("no Kafka brokers configured")

func classifyReadinessError(err error) string {
	var dnsErr *net.DNSError
	var netErr net.Error
	switch {
	case errors.Is(err, errNoKafkaBrokers):
		return readinessReasonNoBrokers
	case errors.As(err, &dnsErr):
		if dnsErr.IsTimeout {
			return readinessReasonDNSTimeout
		}
		return readinessReasonDNS
	case errors.Is(err, context.DeadlineExceeded):
		return readinessReasonTimeout
	case errors.As(err, &netErr) && netErr.Timeout():
		return readinessReasonTimeout
	case errors.Is(err, syscall.ECONNREFUSED):
		return readinessReasonRefused
	default:
		return readinessReasonDial
	}
}
