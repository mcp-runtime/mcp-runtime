package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"mcp-runtime/pkg/events"
)

func TestTokenFileSourceNilAndMissing(t *testing.T) {
	t.Parallel()
	if tok, err := newTokenFileSource("").Token(); tok != "" || err != nil {
		t.Fatalf("nil source = %q, %v", tok, err)
	}
	src := newTokenFileSource(filepath.Join(t.TempDir(), "missing"))
	if _, err := src.Token(); err == nil {
		t.Fatal("expected error for missing token file")
	}
}

func TestTokenFileSourceRefreshesAndKeepsLastGood(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := newTokenFileSource(path)
	if tok, _ := src.Token(); tok != "first" {
		t.Fatalf("token = %q, want first", tok)
	}
	// Rotated token is picked up once the cache expires.
	if err := os.WriteFile(path, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	src.readAt = time.Now().Add(-2 * tokenRefreshInterval)
	if tok, _ := src.Token(); tok != "second" {
		t.Fatalf("token = %q, want second", tok)
	}
	// A transient read failure keeps serving the last good token.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	src.readAt = time.Now().Add(-2 * tokenRefreshInterval)
	if tok, err := src.Token(); tok != "second" || err != nil {
		t.Fatalf("token = %q, err = %v, want last good", tok, err)
	}
}

func TestEmitSendsServiceAccountBearerToken(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("sa-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	var got string
	ingest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusAccepted)
	}))
	defer ingest.Close()

	s := &gatewayServer{
		analyticsURL:   ingest.URL,
		httpClient:     ingest.Client(),
		analyticsToken: newTokenFileSource(path),
	}
	s.emit(t.Context(), events.Envelope{Source: "x", EventType: "y"})
	if got != "Bearer sa-token" {
		t.Fatalf("Authorization = %q, want Bearer sa-token", got)
	}
}

func TestEmitCountsUnauthorizedFailure(t *testing.T) {
	t.Parallel()
	ingest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer ingest.Close()

	metrics := newGatewayMetrics(nil)
	s := &gatewayServer{analyticsURL: ingest.URL, httpClient: ingest.Client(), metrics: metrics}
	s.emit(t.Context(), events.Envelope{Source: "x", EventType: "y"})

	if got := testutil.ToFloat64(metrics.analyticsEmitFailures.WithLabelValues("unauthorized")); got != 1 {
		t.Fatalf("unauthorized failures = %v, want 1", got)
	}
}
