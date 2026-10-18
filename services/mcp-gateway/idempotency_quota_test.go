package main

import (
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"
)

func sc(principal, session string) idemScope {
	return idemScope{Principal: principal, Session: principal + "/" + session}
}

func quotaLimits() idempotencyLimits {
	return idempotencyLimits{
		TTL: time.Minute, MaxEntries: 100, MaxInflight: 50, MaxReply: 64,
		MaxScopeEntries: 3, MaxScopeInflight: 2, MaxPrincipalEntries: 100, MaxPrincipalInflight: 3, MaxScopeTombstones: 2,
	}
}

func mustBegin(t *testing.T, st *memoryIdempotencyStore, scope idemScope, key string) *idempotencyEntry {
	t.Helper()
	e, s := st.begin(scope, key, "h")
	if s != idemStarted {
		t.Fatalf("begin(%s) = %v, want started", key, s)
	}
	return e
}

func TestIdempotencyScopeQuotaDoesNotBlockOtherScopes(t *testing.T) {
	st := newIdempotencyStore(quotaLimits(), nil)
	a := sc("pa", "s1")
	mustBegin(t, st, a, "a1")
	mustBegin(t, st, a, "a2")
	if _, s := st.begin(a, "a3", "h"); s != idemCapacity {
		t.Fatalf("third in-flight in one scope = %v, want capacity", s)
	}
	mustBegin(t, st, sc("pb", "s1"), "b1") // other principal unaffected

	// Aggregate per principal across sessions.
	st = newIdempotencyStore(quotaLimits(), nil)
	mustBegin(t, st, sc("pa", "s1"), "k1")
	mustBegin(t, st, sc("pa", "s1"), "k2")
	mustBegin(t, st, sc("pa", "s2"), "k3")
	if _, s := st.begin(sc("pa", "s3"), "k4", "h"); s != idemCapacity {
		t.Fatalf("principal aggregate in-flight = %v, want capacity", s)
	}
	mustBegin(t, st, sc("pb", "s1"), "k5")
}

func TestIdempotencyScopeEntryQuotaEvictsOwnOldestCompleted(t *testing.T) {
	st := newIdempotencyStore(quotaLimits(), nil)
	a := sc("pa", "s1")
	for _, k := range []string{"a1", "a2", "a3"} {
		e := mustBegin(t, st, a, k)
		st.complete(e, idempotentReply{Status: 200})
	}
	b := mustBegin(t, st, sc("pb", "s1"), "b1")
	st.complete(b, idempotentReply{Status: 200})
	mustBegin(t, st, a, "a4") // evicts a1, not another scope's entry
	if _, s := st.begin(sc("pb", "s1"), "b1", "h"); s != idemReplay {
		t.Fatalf("other scope's entry = %v, want replay", s)
	}
	if _, s := st.begin(a, "a2", "h"); s != idemReplay {
		t.Fatalf("a2 = %v, want replay", s)
	}
}

func TestIdempotencyGlobalPressureEvictsOldestCompleted(t *testing.T) {
	l := quotaLimits()
	l.MaxEntries = 3
	st := newIdempotencyStore(l, nil)
	for _, k := range []string{"a", "b", "c"} {
		e := mustBegin(t, st, sc("p"+k, "s"), k)
		st.complete(e, idempotentReply{Status: 200})
	}
	mustBegin(t, st, sc("pd", "s"), "d")
	for _, k := range []string{"b", "c"} {
		if _, s := st.begin(sc("p"+k, "s"), k, "h"); s != idemReplay {
			t.Fatalf("%s = %v, want replay", k, s)
		}
	}
	if _, s := st.begin(sc("pa", "s"), "a", "h"); s != idemStarted {
		t.Fatalf("oldest entry a = %v, want evicted (started)", s)
	}
}

func TestIdempotencyInflightAndTombstonesSurviveEviction(t *testing.T) {
	l := quotaLimits()
	l.MaxEntries = 3
	st := newIdempotencyStore(l, nil)
	victim := mustBegin(t, st, sc("victim", "s"), "v-inflight")
	_ = victim
	tomb := mustBegin(t, st, sc("victim", "s"), "v-tomb")
	st.tombstone(tomb, idemUnknownOutcome)
	done := mustBegin(t, st, sc("attacker", "s"), "x0")
	st.complete(done, idempotentReply{Status: 200})

	for i := 1; i <= 20; i++ {
		e, s := st.begin(sc("attacker", fmt.Sprint(i)), fmt.Sprintf("x%d", i), "h")
		if s == idemStarted {
			st.complete(e, idempotentReply{Status: 200})
		} else if s != idemCapacity {
			t.Fatalf("attacker begin = %v", s)
		}
	}
	if _, s := st.begin(sc("victim", "s"), "v-inflight", "h"); s != idemInFlight {
		t.Fatalf("victim in-flight entry = %v, want still in flight", s)
	}
	if _, s := st.begin(sc("victim", "s"), "v-tomb", "h"); s != idemUnknownOutcome {
		t.Fatalf("victim tombstone = %v, want unknown outcome", s)
	}
}

func TestIdempotencyTombstoneBudgetFailsClosedPerScope(t *testing.T) {
	st := newIdempotencyStore(quotaLimits(), nil)
	a := sc("pa", "s1")
	for _, k := range []string{"t1", "t2"} {
		st.tombstone(mustBegin(t, st, a, k), idemUnknownOutcome)
	}
	if _, s := st.begin(a, "t3", "h"); s != idemCapacity {
		t.Fatalf("new key with exhausted tombstone budget = %v, want capacity", s)
	}
	if _, s := st.begin(a, "t1", "h"); s != idemUnknownOutcome {
		t.Fatalf("existing tombstone = %v", s)
	}
	mustBegin(t, st, sc("pb", "s1"), "other")
}

func TestIdempotencyInflightEntryIsNotSweptByTTL(t *testing.T) {
	now := time.Unix(1000, 0)
	st := newIdempotencyStore(quotaLimits(), func() time.Time { return now })
	e := mustBegin(t, st, sc("p", "s"), "slow")
	now = now.Add(time.Hour)
	if _, s := st.begin(sc("p", "s"), "slow", "h"); s != idemInFlight {
		t.Fatalf("long-running key = %v, want in flight (a duplicate must not start)", s)
	}
	st.complete(e, idempotentReply{Status: 200})
	if _, s := st.begin(sc("p", "s"), "slow", "h"); s != idemReplay {
		t.Fatalf("after complete = %v, want replay", s)
	}
}

func (s *memoryIdempotencyStore) checkInvariants(t *testing.T) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inflight := 0
	scopes, principals, tombs := map[string]int{}, map[string]int{}, map[string]int{}
	for _, e := range s.entries {
		scopes[e.scope.Session]++
		principals[e.scope.Principal]++
		switch e.state {
		case idemStateInFlight:
			inflight++
		case idemStateUnknown, idemStateNotReplayable:
			tombs[e.scope.Session]++
		}
	}
	l := s.limits
	if len(s.entries) > l.MaxEntries || inflight > l.MaxInflight {
		t.Errorf("global cap exceeded: entries=%d inflight=%d", len(s.entries), inflight)
	}
	for k, n := range scopes {
		if n > l.MaxScopeEntries {
			t.Errorf("scope %s entries=%d", k, n)
		}
	}
	for k, n := range tombs {
		if n > l.MaxScopeTombstones {
			t.Errorf("scope %s tombstones=%d", k, n)
		}
	}
	_ = principals
}

func TestIdempotencyStoreConcurrentHammer(t *testing.T) {
	l := quotaLimits()
	l.MaxEntries, l.MaxInflight, l.MaxPrincipalEntries = 12, 6, 8
	st := newIdempotencyStore(l, nil)
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(g)))
			for i := 0; i < 2000; i++ {
				scope := sc(fmt.Sprint("p", rng.Intn(3)), fmt.Sprint("s", rng.Intn(2)))
				e, s := st.begin(scope, fmt.Sprint("k", rng.Intn(10)), "h")
				if s != idemStarted {
					continue
				}
				switch rng.Intn(4) {
				case 0:
					st.complete(e, idempotentReply{Status: 200, Body: make([]byte, rng.Intn(100))})
				case 1:
					st.tombstone(e, idemUnknownOutcome)
				case 2:
					st.tombstone(e, idemNotReplayable)
				default:
					st.release(e)
				}
				if i%10 == 0 {
					st.checkInvariants(t)
				}
			}
		}(g)
	}
	wg.Wait()
	st.checkInvariants(t)
}

func TestIdempotencyDefaultLimitsAreAllBounded(t *testing.T) {
	l := defaultIdempotencyLimits
	for name, v := range map[string]int{
		"MaxEntries": l.MaxEntries, "MaxInflight": l.MaxInflight, "MaxReply": l.MaxReply,
		"MaxScopeEntries": l.MaxScopeEntries, "MaxScopeInflight": l.MaxScopeInflight,
		"MaxPrincipalEntries": l.MaxPrincipalEntries, "MaxPrincipalInflight": l.MaxPrincipalInflight,
		"MaxScopeTombstones": l.MaxScopeTombstones,
	} {
		if v <= 0 {
			t.Errorf("default %s = %d, must be a positive bound", name, v)
		}
	}
	if l.MaxScopeEntries > l.MaxPrincipalEntries || l.MaxPrincipalEntries >= l.MaxEntries {
		t.Errorf("quotas must nest: scope %d <= principal %d < global %d", l.MaxScopeEntries, l.MaxPrincipalEntries, l.MaxEntries)
	}
}
