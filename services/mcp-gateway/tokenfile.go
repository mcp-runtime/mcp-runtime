package main

import (
	"os"
	"strings"
	"sync"
	"time"
)

// tokenRefreshInterval bounds how stale the cached token can be. The kubelet
// rotates projected service account tokens well before they expire, so
// re-reading once a minute always picks up the current one.
const tokenRefreshInterval = time.Minute

// tokenFileSource reads a projected service account token and caches it
// briefly so each analytics event does not hit the filesystem.
type tokenFileSource struct {
	path string

	mu      sync.Mutex
	token   string
	readAt  time.Time
	lastErr error
}

func newTokenFileSource(path string) *tokenFileSource {
	if path == "" {
		return nil
	}
	return &tokenFileSource{path: path}
}

// Token returns the current token, or "" when no token file is configured.
func (t *tokenFileSource) Token() (string, error) {
	if t == nil {
		return "", nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.readAt.IsZero() && time.Since(t.readAt) < tokenRefreshInterval {
		return t.token, t.lastErr
	}
	data, err := os.ReadFile(t.path)
	t.readAt = time.Now()
	if err != nil {
		// Keep serving the last good token if the file is briefly unreadable
		// during a kubelet rotation; surface the error only when there is none.
		t.lastErr = err
		if t.token != "" {
			t.lastErr = nil
		}
		return t.token, t.lastErr
	}
	t.token = strings.TrimSpace(string(data))
	t.lastErr = nil
	return t.token, nil
}
