package agentadapter

import (
	"context"
	"sync"
)

// requestTracker tracks per-request context cancel functions so a graceful
// shutdown can abort all in-flight runtime calls before the HTTP server drains
// or another owner stops waiting on the group.
type requestTracker struct {
	mu      sync.Mutex
	cancels map[uint64]context.CancelCauseFunc
	next    uint64
}

func newRequestTracker() *requestTracker {
	return &requestTracker{cancels: make(map[uint64]context.CancelCauseFunc)}
}

// track derives a cancellable child context from parent and stores its cancel
// func. The caller must call done(id) when the request finishes to release the
// entry; omitting done leaks the entry until cancelAll is called.
func (rt *requestTracker) track(parent context.Context) (context.Context, uint64) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	id := rt.next
	rt.next++
	ctx, cancel := context.WithCancelCause(parent)
	rt.cancels[id] = cancel
	return ctx, id
}

// done cancels the tracked context (no-op when already cancelled) and removes
// the entry from the map.
func (rt *requestTracker) done(id uint64) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if cancel, ok := rt.cancels[id]; ok {
		cancel(nil)
		delete(rt.cancels, id)
	}
}

// cancelAll cancels every in-flight request with the supplied cause and clears
// the map. Subsequent track calls are unaffected.
func (rt *requestTracker) cancelAll(cause error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for id, cancel := range rt.cancels {
		cancel(cause)
		delete(rt.cancels, id)
	}
}

// size returns the number of currently tracked requests (used by tests).
func (rt *requestTracker) size() int {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return len(rt.cancels)
}
