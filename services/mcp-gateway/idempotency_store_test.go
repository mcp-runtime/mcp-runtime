package main

import (
	"testing"
	"time"
)

func TestIdempotencyStoreExpiresAndBoundsEntries(t *testing.T) {
	now := time.Unix(1000, 0)
	st := newIdempotencyStore(idempotencyLimits{TTL: time.Minute, MaxEntries: 2, MaxInflight: 1, MaxReply: 16}, func() time.Time { return now })

	a, st1 := st.begin("a", "h")
	if st1 != idemStarted || a == nil {
		t.Fatalf("begin a = %v", st1)
	}
	if _, s := st.begin("b", "h"); s != idemCapacity {
		t.Fatalf("second concurrent begin = %v, want in-flight capacity", s)
	}
	st.complete(a, idempotentReply{Status: 200, Body: []byte("ok")})
	b, s := st.begin("b", "h")
	if s != idemStarted {
		t.Fatalf("begin b = %v", s)
	}
	st.complete(b, idempotentReply{Status: 200})
	if _, s := st.begin("c", "h"); s != idemCapacity {
		t.Fatalf("begin c with full store = %v, want capacity", s)
	}
	if _, s := st.begin("a", "h"); s != idemReplay {
		t.Fatalf("begin a = %v, want replay", s)
	}
	if _, s := st.begin("a", "other"); s != idemConflict {
		t.Fatalf("begin a other hash = %v, want conflict", s)
	}
	now = now.Add(2 * time.Minute)
	c, s := st.begin("c", "h")
	if s != idemStarted {
		t.Fatalf("after TTL begin c = %v, want started", s)
	}
	st.release(c)
	if _, s := st.begin("a", "h"); s != idemStarted {
		t.Fatalf("expired a = %v, want started", s)
	}
}

func TestIdempotencyStoreRefusesOversizedReply(t *testing.T) {
	st := newIdempotencyStore(idempotencyLimits{TTL: time.Minute, MaxEntries: 4, MaxInflight: 2, MaxReply: 4}, time.Now)
	e, _ := st.begin("a", "h")
	st.complete(e, idempotentReply{Status: 200, Body: []byte("12345")})
	if _, s := st.begin("a", "h"); s != idemNotReplayable {
		t.Fatalf("oversized stored reply status = %v, want not replayable", s)
	}
}
