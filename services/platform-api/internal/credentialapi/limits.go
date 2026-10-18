package credentialapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"sync"
	"time"

	cb "mcp-runtime/pkg/credentialbroker"
	"mcp-runtime/pkg/sessionstate"
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
	reviewRate      = 50.0 // cold TokenReview creates per second, all sources
	reviewBurst     = 100.0
	authCacheTTL    = 10 * time.Second // verified Pod token identity
	authBadTTL      = 5 * time.Second  // rejected token
	authCacheMax    = 4096
	authRenewWindow = 60 * time.Second // expired-but-recently-valid tokens may renew
	renewRate       = 1.0              // per-token renewal reviews per second
	renewBurst      = 3.0
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
	authOK   map[string]authEntry
	authBad  map[string]time.Time
	review   *tokenBuckets // global TokenReview budget for cold tokens
	renew    *tokenBuckets // per-token renewal budget, separate from review
}

type authEntry struct {
	server sessionstate.Server
	until  time.Time
}

func NewLimits() *Limits {
	return &Limits{
		source:   &tokenBuckets{rate: sourceRate, burst: sourceBurst, maxKeys: limiterMaxKeys, items: map[string]*bucket{}},
		server:   &tokenBuckets{rate: serverRate, burst: serverBurst, maxKeys: limiterMaxKeys, items: map[string]*bucket{}},
		Now:      time.Now,
		failures: map[string]time.Time{},
		authOK:   map[string]authEntry{},
		authBad:  map[string]time.Time{},
		renew:    &tokenBuckets{rate: renewRate, burst: renewBurst, maxKeys: authCacheMax, items: map[string]*bucket{}},
		review:   &tokenBuckets{rate: reviewRate, burst: reviewBurst, maxKeys: 1, items: map[string]*bucket{}},
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

// AllowColdReview charges the global TokenReview budget and the per-source
// budget. It applies only to tokens not already verified or rejected recently,
// so traffic with unknown tokens can never throttle a cached valid caller.
func (l *Limits) AllowColdReview(addr string) bool {
	now := l.Now()
	return l.source.allow(addr, now) && l.review.allow("all", now)
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

type authState int

const (
	authUnknown   authState = iota
	authValid               // fresh verified identity
	authRejected            // recent definite rejection
	authRenewable           // expired within authRenewWindow of a valid verdict
)

// CachedAuth returns the cached verdict state for a token hash.
func (l *Limits) CachedAuth(key string) (sessionstate.Server, authState) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.Now()
	if until, ok := l.authBad[key]; ok {
		if now.Before(until) {
			return sessionstate.Server{}, authRejected
		}
		delete(l.authBad, key)
	}
	if e, ok := l.authOK[key]; ok {
		if now.Before(e.until) {
			return e.server, authValid
		}
		if now.Before(e.until.Add(authRenewWindow - authCacheTTL)) {
			return e.server, authRenewable
		}
		delete(l.authOK, key)
	}
	return sessionstate.Server{}, authUnknown
}

// AllowRenewal charges only the token's own renewal budget, never the cold-token
// or per-source budgets.
func (l *Limits) AllowRenewal(key string) bool { return l.renew.allow(key, l.Now()) }

// ForgetAuth drops a token's valid verdict after a definite rejection.
func (l *Limits) ForgetAuth(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.authOK, key)
}

func (l *Limits) RecordAuth(key string, server sessionstate.Server, valid bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.Now()
	if len(l.authOK)+len(l.authBad) >= authCacheMax {
		for k, e := range l.authOK {
			if !now.Before(e.until.Add(authRenewWindow - authCacheTTL)) {
				delete(l.authOK, k)
			}
		}
		for k, until := range l.authBad {
			if !now.Before(until) {
				delete(l.authBad, k)
			}
		}
		// Prefer evicting rejected tokens so a flood cannot push out valid callers.
		for k := range l.authBad {
			if len(l.authOK)+len(l.authBad) < authCacheMax {
				break
			}
			delete(l.authBad, k)
		}
		for k := range l.authOK {
			if len(l.authOK)+len(l.authBad) < authCacheMax {
				break
			}
			delete(l.authOK, k)
		}
	}
	if valid {
		l.authOK[key] = authEntry{server, now.Add(authCacheTTL)}
	} else {
		l.authBad[key] = now.Add(authBadTTL)
	}
}
func (l *Limits) AllowServer(uid string) bool { return l.server.allow(uid, l.Now()) }

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
