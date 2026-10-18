package credentialapi

import (
	"errors"
	"strconv"
	"sync"
	"time"

	cb "mcp-runtime/pkg/credentialbroker"
)

var errRateLimited = errors.New("credential resolution rate limited")

// Defaults. Source limits apply to unauthenticated callers before the
// TokenReview create; server limits apply after the Pod-bound token verifies.
const (
	sourceRate      = 10.0 // requests per second per source address
	sourceBurst     = 20.0
	serverRate      = 20.0 // requests per second per MCPServer UID
	serverBurst     = 40.0
	limiterMaxKeys  = 4096
	failureTTL      = 5 * time.Second
	failureMaxItems = 1024
)

type bucket struct {
	tokens float64
	last   time.Time
}

type tokenBuckets struct {
	mu      sync.Mutex
	rate    float64
	burst   float64
	maxKeys int
	items   map[string]*bucket
}

func (t *tokenBuckets) allow(key string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	b := t.items[key]
	if b == nil {
		if len(t.items) >= t.maxKeys {
			t.evict(now)
		}
		b = &bucket{tokens: t.burst, last: now}
		t.items[key] = b
	}
	b.tokens += now.Sub(b.last).Seconds() * t.rate
	if b.tokens > t.burst {
		b.tokens = t.burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// evict drops idle (fully refilled) buckets, then one arbitrary bucket so the
// map stays bounded under a flood of distinct keys.
func (t *tokenBuckets) evict(now time.Time) {
	for k, b := range t.items {
		if b.tokens+now.Sub(b.last).Seconds()*t.rate >= t.burst {
			delete(t.items, k)
		}
	}
	for k := range t.items {
		if len(t.items) < t.maxKeys {
			return
		}
		delete(t.items, k)
	}
}

// Limits holds rate limiters and a short, bounded negative cache of provider
// failures. It never stores secret values: only a key and an expiry.
type Limits struct {
	source, server *tokenBuckets
	Now            func() time.Time

	mu       sync.Mutex
	failures map[string]time.Time
}

func NewLimits() *Limits {
	return &Limits{
		source:   &tokenBuckets{rate: sourceRate, burst: sourceBurst, maxKeys: limiterMaxKeys, items: map[string]*bucket{}},
		server:   &tokenBuckets{rate: serverRate, burst: serverBurst, maxKeys: limiterMaxKeys, items: map[string]*bucket{}},
		Now:      time.Now,
		failures: map[string]time.Time{},
	}
}

var (
	defaultLimitsOnce sync.Once
	defaultLimits     *Limits
)

func (h Handler) limits() *Limits {
	if h.Limits != nil {
		return h.Limits
	}
	defaultLimitsOnce.Do(func() { defaultLimits = NewLimits() })
	return defaultLimits
}

func (l *Limits) AllowSource(addr string) bool { return l.source.allow(addr, l.Now()) }
func (l *Limits) AllowServer(uid string) bool  { return l.server.allow(uid, l.Now()) }

func (l *Limits) failureKey(serverUID string, s cb.Source) string {
	return serverUID + "|" + s.Namespace + "/" + s.Name + "@" + strconv.FormatInt(s.Revision, 10)
}

// RecentFailure reports whether a provider read for key failed within failureTTL.
func (l *Limits) RecentFailure(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	until, ok := l.failures[key]
	if !ok {
		return false
	}
	if !l.Now().Before(until) {
		delete(l.failures, key)
		return false
	}
	return true
}

func (l *Limits) RecordFailure(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.Now()
	if len(l.failures) >= failureMaxItems {
		for k, until := range l.failures {
			if !now.Before(until) {
				delete(l.failures, k)
			}
		}
		for k := range l.failures {
			if len(l.failures) < failureMaxItems {
				break
			}
			delete(l.failures, k)
		}
	}
	l.failures[key] = now.Add(failureTTL)
}
