package main

import (
	"sync"
	"time"
)

// idemStatus is the result of reserving a scoped key in an idempotencyStore.
type idemStatus int

const (
	// idemStarted means the caller owns the key and must complete, tombstone
	// or release it.
	idemStarted idemStatus = iota
	// idemReplay means a stored reply for the identical request exists.
	idemReplay
	// idemConflict means the key was used for a different request.
	idemConflict
	// idemInFlight means the first request has not finished.
	idemInFlight
	// idemUnknownOutcome means the first request's outcome is ambiguous; the
	// tool must not be run again under this key.
	idemUnknownOutcome
	// idemNotReplayable means the first request ran but its reply cannot be
	// replayed (stream or over the size cap).
	idemNotReplayable
	// idemCapacity means the store's entry or in-flight bound is reached.
	idemCapacity
)

// idempotentReply is the minimal replayable response. Upstream response headers
// other than Content-Type are intentionally not stored.
type idempotentReply struct {
	Status      int
	ContentType string
	Body        []byte
}

type idemState int

const (
	idemStateInFlight idemState = iota
	idemStateDone
	idemStateUnknown
	idemStateNotReplayable
)

// idemScope identifies who a key belongs to, as opaque hashes. Principal is the
// verified principal; Session is the principal plus session. Quotas are applied
// per Session scope and in aggregate per Principal.
type idemScope struct {
	Principal string
	Session   string
}

type idempotencyEntry struct {
	scope     idemScope
	seq       uint64
	scopedKey string
	reqHash   string
	state     idemState
	reply     idempotentReply
	expires   time.Time
}

// idempotencyStore holds scoped-key reservations. Keys passed to it are already
// scoped hashes; implementations never see raw client keys or payloads. The
// in-memory implementation below is the only one shipped. A store shared across
// gateway replicas is planned and would implement this same interface; it must
// keep the same atomic begin semantics and the "never re-execute after an
// ambiguous outcome" rule.
type idempotencyStore interface {
	// begin atomically reserves scopedKey for reqHash or reports why not. For
	// idemReplay the returned entry carries the stored reply.
	begin(scope idemScope, scopedKey, reqHash string) (*idempotencyEntry, idemStatus)
	// complete stores a replayable reply, or a not-replayable tombstone when
	// the reply exceeds the store's size cap.
	complete(e *idempotencyEntry, reply idempotentReply)
	// tombstone records an outcome that must never be re-executed:
	// idemUnknownOutcome or idemNotReplayable.
	tombstone(e *idempotencyEntry, kind idemStatus)
	// release forgets a reservation whose request was not executed.
	release(e *idempotencyEntry)
}

type idempotencyLimits struct {
	TTL         time.Duration
	MaxEntries  int
	MaxInflight int
	MaxReply    int

	// Per-scope quotas next to the global caps. A zero value means no limit.
	MaxScopeEntries      int
	MaxScopeInflight     int
	MaxPrincipalEntries  int
	MaxPrincipalInflight int
	MaxScopeTombstones   int
}

// defaultIdempotencyLimits bounds one gateway sidecar to about 8 MiB of stored
// replies. See docs/internals/gateway-idempotency.md.
var defaultIdempotencyLimits = idempotencyLimits{
	TTL:         5 * time.Minute,
	MaxEntries:  256,
	MaxInflight: 32,
	MaxReply:    32 << 10,

	MaxScopeEntries:      32,
	MaxScopeInflight:     4,
	MaxPrincipalEntries:  64,
	MaxPrincipalInflight: 8,
	MaxScopeTombstones:   8,
}

type memoryIdempotencyStore struct {
	mu       sync.Mutex
	limits   idempotencyLimits
	now      func() time.Time
	entries  map[string]*idempotencyEntry
	inflight int
	seq      uint64
}

func newIdempotencyStore(limits idempotencyLimits, now func() time.Time) *memoryIdempotencyStore {
	if now == nil {
		now = time.Now
	}
	return &memoryIdempotencyStore{limits: limits, now: now, entries: map[string]*idempotencyEntry{}}
}

func (s *memoryIdempotencyStore) MaxReply() int { return s.limits.MaxReply }

// sweepLocked drops expired entries. The store is small and bounded, so a
// linear scan per reservation is cheap.
// sweepLocked drops expired completed entries and tombstones. In-flight
// entries are never swept: a long-running body must not let a duplicate start
// the key. They leave only through complete, tombstone or release.
func (s *memoryIdempotencyStore) sweepLocked(now time.Time) {
	for k, e := range s.entries {
		if e.state != idemStateInFlight && !now.Before(e.expires) {
			s.dropLocked(k, e)
		}
	}
}

func (s *memoryIdempotencyStore) dropLocked(k string, e *idempotencyEntry) {
	if e.state == idemStateInFlight {
		s.inflight--
	}
	delete(s.entries, k)
}

// evictOldestLocked removes the oldest completed replayable entry accepted by
// match. In-flight entries and tombstones are never evicted: evicting a
// tombstone would permit re-execution after an ambiguous outcome.
func (s *memoryIdempotencyStore) evictOldestLocked(match func(*idempotencyEntry) bool) bool {
	var oldest *idempotencyEntry
	for _, e := range s.entries {
		if e.state == idemStateDone && match(e) && (oldest == nil || e.seq < oldest.seq) {
			oldest = e
		}
	}
	if oldest == nil {
		return false
	}
	s.dropLocked(oldest.scopedKey, oldest)
	return true
}

func atLimit(n, limit int) bool { return limit > 0 && n >= limit }

func (s *memoryIdempotencyStore) begin(scope idemScope, scopedKey, reqHash string) (*idempotencyEntry, idemStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.sweepLocked(now)
	if e, ok := s.entries[scopedKey]; ok {
		if e.reqHash != reqHash {
			return nil, idemConflict
		}
		switch e.state {
		case idemStateDone:
			return e, idemReplay
		case idemStateUnknown:
			return nil, idemUnknownOutcome
		case idemStateNotReplayable:
			return nil, idemNotReplayable
		default:
			return nil, idemInFlight
		}
	}
	var scopeEntries, scopeInflight, scopeTombs, principalEntries, principalInflight int
	for _, e := range s.entries {
		if e.scope.Principal == scope.Principal {
			principalEntries++
			if e.state == idemStateInFlight {
				principalInflight++
			}
		}
		if e.scope.Session == scope.Session {
			scopeEntries++
			switch e.state {
			case idemStateInFlight:
				scopeInflight++
			case idemStateUnknown, idemStateNotReplayable:
				scopeTombs++
			}
		}
	}
	l := s.limits
	// A new key could end in a tombstone; if this scope's tombstone budget is
	// spent, fail closed for this scope only instead of risking re-execution.
	if atLimit(scopeTombs, l.MaxScopeTombstones) ||
		atLimit(scopeInflight, l.MaxScopeInflight) ||
		atLimit(principalInflight, l.MaxPrincipalInflight) ||
		atLimit(s.inflight, l.MaxInflight) {
		return nil, idemCapacity
	}
	if atLimit(scopeEntries, l.MaxScopeEntries) &&
		!s.evictOldestLocked(func(e *idempotencyEntry) bool { return e.scope.Session == scope.Session }) {
		return nil, idemCapacity
	}
	if atLimit(principalEntries, l.MaxPrincipalEntries) &&
		!s.evictOldestLocked(func(e *idempotencyEntry) bool { return e.scope.Principal == scope.Principal }) {
		return nil, idemCapacity
	}
	if atLimit(len(s.entries), l.MaxEntries) &&
		!s.evictOldestLocked(func(*idempotencyEntry) bool { return true }) {
		return nil, idemCapacity
	}
	e := &idempotencyEntry{scope: scope, scopedKey: scopedKey, reqHash: reqHash, state: idemStateInFlight, expires: now.Add(l.TTL)}
	s.entries[scopedKey] = e
	s.inflight++
	return e, idemStarted
}

func (s *memoryIdempotencyStore) finishLocked(e *idempotencyEntry, state idemState) {
	if cur, ok := s.entries[e.scopedKey]; !ok || cur != e {
		return
	}
	if e.state == idemStateInFlight {
		s.inflight--
	}
	s.seq++
	e.seq = s.seq
	e.state = state
	e.expires = s.now().Add(s.limits.TTL)
}

func (s *memoryIdempotencyStore) complete(e *idempotencyEntry, reply idempotentReply) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(reply.Body) > s.limits.MaxReply {
		s.finishLocked(e, idemStateNotReplayable)
		return
	}
	e.reply = reply
	s.finishLocked(e, idemStateDone)
}

func (s *memoryIdempotencyStore) tombstone(e *idempotencyEntry, kind idemStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if kind == idemNotReplayable {
		s.finishLocked(e, idemStateNotReplayable)
		return
	}
	s.finishLocked(e, idemStateUnknown)
}

func (s *memoryIdempotencyStore) release(e *idempotencyEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.entries[e.scopedKey]; ok && cur == e {
		s.dropLocked(e.scopedKey, e)
	}
}
