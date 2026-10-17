package auth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newLoginAttemptTracker(now func() time.Time) *AttemptTracker {
	return NewAttemptTracker(now, DefaultAttemptConfig())
}

const loginLockoutDuration = 5 * time.Minute

func TestLoginClientIDUsesForwardedFor(t *testing.T) {
	t.Setenv("MCP_TRUSTED_PROXY_CIDRS", "10.0.0.2/32")
	req := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
	req.RemoteAddr = "10.0.0.2:12345"
	req.Header.Set("x-forwarded-for", "203.0.113.10, 10.0.0.2")

	if got := loginClientID(req); got != "203.0.113.10" {
		t.Fatalf("loginClientID() = %q, want forwarded client", got)
	}
}

func TestLoginClientIDEmptyForwardedForFallsBackToRemoteAddr(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
	req.RemoteAddr = "198.51.100.20:54321"
	req.Header.Set("x-forwarded-for", " , ")

	if got := loginClientID(req); got != "198.51.100.20" {
		t.Fatalf("loginClientID() = %q, want remote addr host", got)
	}
}

func TestLoginClientIDUnknownWhenNoAddressAvailable(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
	req.RemoteAddr = ""

	if got := loginClientID(req); got != loginClientUnknownIP {
		t.Fatalf("loginClientID() = %q, want %q", got, loginClientUnknownIP)
	}
}

func TestLoginAttemptTrackerPrunesIdleClientsPeriodically(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tracker := newLoginAttemptTracker(func() time.Time { return now })

	tracker.RecordFailure("client-old")
	now = now.Add(loginAttemptIdleTTL + time.Second)
	tracker.RecordFailure("client-new")

	if _, ok := tracker.clients["client-old"]; ok {
		t.Fatal("idle login attempt client was not pruned")
	}
	if _, ok := tracker.clients["client-new"]; !ok {
		t.Fatal("new login attempt client missing")
	}
}

func TestLoginAttemptTrackerDoesNotPruneOnEveryRequest(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tracker := newLoginAttemptTracker(func() time.Time { return now })
	tracker.RecordFailure("client")
	firstPrune := tracker.lastPrune

	now = now.Add(loginAttemptPruneInterval / 2)
	tracker.RecordFailure("client")

	if !tracker.lastPrune.Equal(firstPrune) {
		t.Fatalf("last prune = %s, want %s", tracker.lastPrune, firstPrune)
	}
}

func TestLoginAttemptTrackerCapsClientsAndPreservesLockedClients(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tracker := newLoginAttemptTracker(func() time.Time { return now })
	tracker.clients["locked"] = &loginClientState{
		lastSeen:    now,
		lockedUntil: now.Add(loginLockoutDuration),
	}

	for i := 0; i < loginAttemptMaxClients; i++ {
		now = now.Add(time.Millisecond)
		if !tracker.Allow(fmt.Sprintf("client-%d", i)) {
			t.Fatalf("client-%d should be allowed on first attempt", i)
		}
	}

	if got := len(tracker.clients); got > loginAttemptMaxClients {
		t.Fatalf("login attempt clients = %d, want <= %d", got, loginAttemptMaxClients)
	}
	if _, ok := tracker.clients["locked"]; !ok {
		t.Fatal("active lockout was evicted before unlocked clients")
	}
	if _, ok := tracker.clients["client-0"]; ok {
		t.Fatal("oldest unlocked client was not evicted")
	}
}

func TestSecureCookieCanBeForcedByConfig(t *testing.T) {
	server := &Server{ForceSecureCookie: true}

	req := httptest.NewRequest(http.MethodGet, "http://platform.example.com/", nil)
	req.Header.Set("X-Forwarded-Proto", "http")

	if !server.secureCookie(req) {
		t.Fatal("secureCookie() = false, want true when forceSecureCookie=true")
	}
}
