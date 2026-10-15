package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"mcp-runtime/pkg/policy"
	"mcp-runtime/pkg/sessionstate"
)

func newTestSessionSync(t *testing.T, handler http.HandlerFunc) *gatewaySessionSync {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("test-projected-token"), 0600); err != nil {
		t.Fatal(err)
	}
	return &gatewaySessionSync{server: sessionstate.Server{Namespace: "team", Name: "server", UID: "server-uid"}, baseURL: server.URL, token: newTokenFileSource(path), client: server.Client(), staleWindow: time.Minute}
}

func testSyncSession(name string) sessionstate.Session {
	return sessionstate.Session{ID: "team/" + name, Namespace: "team", ServerNamespace: "team", ServerName: "server", ServerUID: "server-uid", Binding: policy.Binding{Name: policy.SessionID(name), Namespace: "team", HumanID: "human", AgentID: "agent", TeamID: "team-id", ConsentedTrust: "medium", ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339)}}
}

func TestSessionSyncLargeSnapshotRevocationAndRestore(t *testing.T) {
	var mu sync.Mutex
	epoch := "first"
	revoked := false
	cache := newTestSessionSync(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-projected-token" {
			t.Error("projected credential missing")
		}
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == sessionstate.GatewayPath {
			snapshot := sessionstate.Snapshot{Epoch: epoch, Sequence: 1}
			for i := 0; i < 10000; i++ {
				if !revoked || i != 0 {
					snapshot.Sessions = append(snapshot.Sessions, testSyncSession(fmt.Sprintf("session-%d", i)))
				}
			}
			_ = json.NewEncoder(w).Encode(snapshot)
		} else if r.URL.Path == sessionstate.GatewayPath+"/changes" {
			if r.URL.Query().Get("epoch") != epoch {
				w.WriteHeader(410)
				return
			}
			out := sessionstate.Changes{Epoch: epoch, Sequence: 2}
			if revoked {
				out.Changes = []sessionstate.Change{{Kind: "session", Sequence: 2, Operation: "delete", ID: "team/session-0"}}
			}
			_ = json.NewEncoder(w).Encode(out)
		} else {
			w.WriteHeader(404)
		}
	})
	if _, reason := cache.binding(context.Background(), "team", "session-0"); reason != "policy_unavailable" {
		t.Fatalf("cold reason=%s", reason)
	}
	if err := cache.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if binding, reason := cache.binding(context.Background(), "team", "session-0"); reason != "" || binding == nil {
		t.Fatalf("warm binding=%+v reason=%s", binding, reason)
	}
	mu.Lock()
	revoked = true
	mu.Unlock()
	if err := cache.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, reason := cache.binding(context.Background(), "team", "session-0"); reason != "session_not_found" {
		t.Fatalf("revoked reason=%s", reason)
	}
	mu.Lock()
	epoch = "restored"
	mu.Unlock()
	if err := cache.sync(context.Background()); err != sessionstate.ErrReset {
		t.Fatalf("restore error=%v", err)
	}
	if err := cache.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	cache.mu.Lock()
	cache.lastSync = time.Now().Add(-2 * time.Minute)
	cache.mu.Unlock()
	if _, reason := cache.binding(context.Background(), "team", "session-1"); reason != "policy_unavailable" {
		t.Fatalf("stale reason=%s", reason)
	}
}

func TestSessionSyncRejectsForeignGenerationAndKeepsLastGood(t *testing.T) {
	foreign := false
	cache := newTestSessionSync(t, func(w http.ResponseWriter, r *http.Request) {
		row := testSyncSession("session")
		if foreign {
			row.ServerUID = "old-generation"
		}
		_ = json.NewEncoder(w).Encode(sessionstate.Snapshot{Epoch: "epoch", Sequence: 1, Sessions: []sessionstate.Session{row}})
	})
	if err := cache.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	cache.mu.Lock()
	cache.epoch = ""
	cache.mu.Unlock()
	foreign = true
	if err := cache.sync(context.Background()); err == nil {
		t.Fatal("foreign generation accepted")
	}
	if binding, reason := cache.binding(context.Background(), "team", "session"); binding == nil || reason != "" {
		t.Fatalf("last good binding lost: %+v %s", binding, reason)
	}
}

func TestSessionSyncConcurrentReadsUseIndependentBindings(t *testing.T) {
	cache := newTestSessionSync(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == sessionstate.GatewayPath {
			_ = json.NewEncoder(w).Encode(sessionstate.Snapshot{Epoch: "epoch", Sequence: 1, Sessions: []sessionstate.Session{testSyncSession("session")}})
		} else {
			_ = json.NewEncoder(w).Encode(sessionstate.Changes{Epoch: "epoch", Sequence: 1})
		}
	})
	if err := cache.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				b, reason := cache.binding(context.Background(), "team", "session")
				if b == nil || reason != "" {
					t.Error(reason)
					return
				}
				b.ConsentedTrust = "high"
			}
		}()
	}
	for i := 0; i < 10; i++ {
		if err := cache.sync(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	b, _ := cache.binding(context.Background(), "team", "session")
	if b.ConsentedTrust != "medium" {
		t.Fatal("reader mutated cache")
	}
}

func TestSessionSyncKnownResetOrIdentityRejectionInvalidatesWarmState(t *testing.T) {
	for _, status := range []int{401, 403, 410} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			responseStatus := 200
			cache := newTestSessionSync(t, func(w http.ResponseWriter, r *http.Request) {
				if responseStatus != 200 {
					w.WriteHeader(responseStatus)
					return
				}
				_ = json.NewEncoder(w).Encode(sessionstate.Snapshot{Epoch: "epoch", Sequence: 1, Sessions: []sessionstate.Session{testSyncSession("session")}})
			})
			if err := cache.sync(context.Background()); err != nil {
				t.Fatal(err)
			}
			responseStatus = status
			if err := cache.sync(context.Background()); err == nil {
				t.Fatal("rejection accepted")
			}
			responseStatus = 503
			if err := cache.sync(context.Background()); err == nil {
				t.Fatal("outage accepted")
			}
			if binding, reason := cache.binding(context.Background(), "team", "session"); binding != nil || reason != "policy_unavailable" {
				t.Fatalf("invalid authority remained usable: %+v %s", binding, reason)
			}
		})
	}
}
