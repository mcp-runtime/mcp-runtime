package auth

import (
	"sort"
	"sync"
	"time"
)

// AttemptConfig is resolved by bootstrap and shared by one login limiter.
type AttemptConfig struct {
	Capacity         int
	Refill           time.Duration
	FailureWindow    time.Duration
	FailureThreshold int
	LockoutDuration  time.Duration
}

func DefaultAttemptConfig() AttemptConfig {
	return AttemptConfig{Capacity: 10, Refill: time.Minute, FailureWindow: 15 * time.Minute, FailureThreshold: 5, LockoutDuration: 5 * time.Minute}
}

const (
	loginAttemptIdleTTL       = 30 * time.Minute
	loginAttemptMaxClients    = 4096
	loginAttemptPruneInterval = time.Minute
	loginAttemptEvictionBatch = 256
)

type AttemptTracker struct {
	config    AttemptConfig
	mu        sync.Mutex
	clients   map[string]*loginClientState
	now       func() time.Time
	lastPrune time.Time
}

type loginClientState struct {
	tokens         int
	lastRefill     time.Time
	lastSeen       time.Time
	failures       int
	failuresExpire time.Time
	lockedUntil    time.Time
}

func NewAttemptTracker(now func() time.Time, config AttemptConfig) *AttemptTracker {
	return &AttemptTracker{clients: map[string]*loginClientState{}, now: now, config: config}
}

func (t *AttemptTracker) Allow(clientID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	t.pruneIfDueLocked(now)
	state := t.stateForLocked(clientID, now)
	state.lastSeen = now
	t.refillLoginTokens(state, now)
	if now.Before(state.lockedUntil) {
		return false
	}
	if state.tokens <= 0 {
		return false
	}
	state.tokens--
	t.enforceMaxLocked(now)
	return true
}

func (t *AttemptTracker) RecordFailure(clientID string) int {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	t.pruneIfDueLocked(now)
	state := t.stateForLocked(clientID, now)
	state.lastSeen = now
	if now.After(state.failuresExpire) {
		state.failures = 0
	}
	state.failures++
	state.failuresExpire = now.Add(t.config.FailureWindow)
	if state.failures >= t.config.FailureThreshold {
		state.lockedUntil = now.Add(t.config.LockoutDuration)
	}
	t.enforceMaxLocked(now)
	return state.failures
}

func (t *AttemptTracker) RecordSuccess(clientID string) int {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	t.pruneIfDueLocked(now)
	state := t.clients[clientID]
	if state == nil {
		return 0
	}
	state.lastSeen = now
	prior := state.failures
	state.failures = 0
	state.failuresExpire = time.Time{}
	state.lockedUntil = time.Time{}
	return prior
}

func (t *AttemptTracker) stateForLocked(clientID string, now time.Time) *loginClientState {
	state := t.clients[clientID]
	if state == nil {
		state = &loginClientState{tokens: t.config.Capacity, lastRefill: now, lastSeen: now}
		t.clients[clientID] = state
	}
	return state
}

func (t *AttemptTracker) pruneIfDueLocked(now time.Time) {
	if !t.lastPrune.IsZero() && now.Sub(t.lastPrune) < loginAttemptPruneInterval {
		return
	}
	t.lastPrune = now
	for clientID, state := range t.clients {
		if now.Sub(state.lastSeen) > loginAttemptIdleTTL && !now.Before(state.lockedUntil) {
			delete(t.clients, clientID)
		}
	}
}

func (t *AttemptTracker) enforceMaxLocked(now time.Time) {
	if len(t.clients) <= loginAttemptMaxClients {
		return
	}
	target := loginAttemptMaxClients - loginAttemptEvictionBatch
	if target < 0 {
		target = 0
	}
	type candidate struct {
		clientID string
		lastSeen time.Time
		failures int
		locked   bool
	}
	candidates := make([]candidate, 0, len(t.clients))
	for clientID, state := range t.clients {
		candidates = append(candidates, candidate{
			clientID: clientID,
			lastSeen: state.lastSeen,
			failures: state.failures,
			locked:   now.Before(state.lockedUntil),
		})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].locked != candidates[j].locked {
			return !candidates[i].locked
		}
		if (candidates[i].failures > 0) != (candidates[j].failures > 0) {
			return candidates[i].failures == 0
		}
		return candidates[i].lastSeen.Before(candidates[j].lastSeen)
	})
	for _, entry := range candidates[:len(t.clients)-target] {
		delete(t.clients, entry.clientID)
	}
}

func (t *AttemptTracker) refillLoginTokens(state *loginClientState, now time.Time) {
	if state.lastRefill.IsZero() {
		state.lastRefill = now
	}
	elapsed := now.Sub(state.lastRefill)
	if elapsed < t.config.Refill {
		return
	}
	refill := int(elapsed / t.config.Refill)
	state.tokens += refill
	if state.tokens > t.config.Capacity {
		state.tokens = t.config.Capacity
	}
	state.lastRefill = state.lastRefill.Add(time.Duration(refill) * t.config.Refill)
}
