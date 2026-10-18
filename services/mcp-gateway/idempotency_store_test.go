package main

import (
	"testing"
	"time"
)

var testScope = idemScope{Principal: "p", Session: "p/s"}

func TestIdempotencyStoreExpiresAndBoundsEntries(t *testing.T) {
	now := time.Unix(1000, 0)
	st := newIdempotencyStore(idempotencyLimits{TTL: time.Minute, MaxEntries: 2, MaxInflight: 1, MaxReply: 16}, func() time.Time { return now })

	a, st1 := st.begin(testScope, "a", "h")
	if st1 != idemStarted || a == nil {
		t.Fatalf("begin a = %v", st1)
	}
	if _, s := st.begin(testScope, "b", "h"); s != idemCapacity {
		t.Fatalf("second concurrent begin = %v, want in-flight capacity", s)
	}
	st.complete(a, idempotentReply{Status: 200, Body: []byte("ok")})
	b, s := st.begin(testScope, "b", "h")
	if s != idemStarted {
		t.Fatalf("begin b = %v", s)
	}
	st.complete(b, idempotentReply{Status: 200})
	c0, s0 := st.begin(testScope, "c", "h")
	if s0 != idemStarted {
		t.Fatalf("begin c with full store = %v, want started by evicting oldest completed", s0)
	}
	st.release(c0)
	a2, sa := st.begin(testScope, "a", "other")
	if sa != idemStarted {
		t.Fatalf("evicted a = %v, want started", sa)
	}
	st.release(a2)
	if _, s := st.begin(testScope, "b", "h"); s != idemReplay {
		t.Fatalf("begin b = %v, want replay", s)
	}
	if _, s := st.begin(testScope, "b", "other"); s != idemConflict {
		t.Fatalf("begin b other hash = %v, want conflict", s)
	}
	now = now.Add(2 * time.Minute)
	c, s := st.begin(testScope, "c", "h")
	if s != idemStarted {
		t.Fatalf("after TTL begin c = %v, want started", s)
	}
	st.release(c)
	if _, s := st.begin(testScope, "b", "h"); s != idemStarted {
		t.Fatalf("expired b = %v, want started", s)
	}
}

func TestIdempotencyStoreRefusesOversizedReply(t *testing.T) {
	st := newIdempotencyStore(idempotencyLimits{TTL: time.Minute, MaxEntries: 4, MaxInflight: 2, MaxReply: 4}, time.Now)
	e, _ := st.begin(testScope, "a", "h")
	st.complete(e, idempotentReply{Status: 200, Body: []byte("12345")})
	if _, s := st.begin(testScope, "a", "h"); s != idemNotReplayable {
		t.Fatalf("oversized stored reply status = %v, want not replayable", s)
	}
}
