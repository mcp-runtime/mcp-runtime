package spiffe_identity

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

func verifiedTLS(uris ...*url.URL) *tls.ConnectionState {
	cert := &x509.Certificate{URIs: uris}
	return &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{cert},
		VerifiedChains:   [][]*x509.Certificate{{cert}},
	}
}

func TestStripsSpoofedHeadersAndInjectsVerifiedIdentity(t *testing.T) {
	var seen *http.Request
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { seen = r })
	cfg := CreateConfig()
	cfg.StripHeaders = []string{"X-Untrusted-Identity"}
	h, err := New(context.Background(), next, cfg, "test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	// Client tries to smuggle identity headers, including the verified one.
	req.Header.Set("X-MCP-Verified-SPIFFE-ID", "spiffe://example.org/ns/admin/session/victim")
	req.Header.Set("X-Untrusted-Identity", "attacker")
	req.TLS = verifiedTLS(mustURL(t, "spiffe://example.org/ns/team-a/session/session-1"))

	h.ServeHTTP(httptest.NewRecorder(), req)

	if seen == nil {
		t.Fatal("next handler not called")
	}
	if got := seen.Header.Get("X-MCP-Verified-SPIFFE-ID"); got != "spiffe://example.org/ns/team-a/session/session-1" {
		t.Fatalf("verified header = %q, want the cert-derived identity (spoof must be overwritten)", got)
	}
	if got := seen.Header.Get("X-Untrusted-Identity"); got != "" {
		t.Fatalf("X-Untrusted-Identity = %q, want stripped", got)
	}
}

func TestStripsVerifiedHeaderWhenNoClientCertificate(t *testing.T) {
	var seen *http.Request
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { seen = r })
	h, _ := New(context.Background(), next, CreateConfig(), "test")

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	// Forged header with no client certificate (e.g. plaintext / bypass attempt).
	req.Header.Set("X-MCP-Verified-SPIFFE-ID", "spiffe://example.org/ns/admin/session/victim")

	h.ServeHTTP(httptest.NewRecorder(), req)

	if got := seen.Header.Get("X-MCP-Verified-SPIFFE-ID"); got != "" {
		t.Fatalf("verified header = %q, want stripped (no verified cert to inject)", got)
	}
}

func TestTrustDomainFilter(t *testing.T) {
	var seen *http.Request
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { seen = r })
	cfg := CreateConfig()
	cfg.TrustDomain = "example.org"
	h, _ := New(context.Background(), next, cfg, "test")

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.TLS = verifiedTLS(mustURL(t, "spiffe://attacker.org/ns/team-a/session/session-1"))

	h.ServeHTTP(httptest.NewRecorder(), req)

	if got := seen.Header.Get("X-MCP-Verified-SPIFFE-ID"); got != "" {
		t.Fatalf("verified header = %q, want empty for wrong trust domain", got)
	}
}

func TestUnverifiedChainIsIgnored(t *testing.T) {
	var seen *http.Request
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { seen = r })
	h, _ := New(context.Background(), next, CreateConfig(), "test")

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	// PeerCertificates present but no VerifiedChains — must not be trusted.
	cert := &x509.Certificate{URIs: []*url.URL{mustURL(t, "spiffe://example.org/ns/team-a/session/session-1")}}
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}

	h.ServeHTTP(httptest.NewRecorder(), req)

	if got := seen.Header.Get("X-MCP-Verified-SPIFFE-ID"); got != "" {
		t.Fatalf("verified header = %q, want empty for unverified chain", got)
	}
}

// With rejectInvalidCertificate, a presented certificate that carries no SPIFFE
// ID in the trust domain is refused instead of silently falling back to OAuth.
func TestRejectInvalidCertificate(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true })
	cfg := CreateConfig()
	cfg.TrustDomain = "example.org"
	cfg.RejectInvalidCertificate = true
	h, err := New(context.Background(), next, cfg, "test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.TLS = verifiedTLS(mustURL(t, "spiffe://other.example/ns/team-a/session/s1"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || called {
		t.Fatalf("status = %d, called = %v; want 401 without reaching the gateway", rec.Code, called)
	}

	// Without the option the same request continues, unauthenticated, to OAuth.
	cfg.RejectInvalidCertificate = false
	h, _ = New(context.Background(), next, cfg, "test")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if !called {
		t.Fatal("without rejectInvalidCertificate the request should continue to OAuth")
	}
}

// Clients without a certificate are ordinary OAuth clients: the middleware
// removes only the internal assertion and leaves their other headers, which
// the gateway treats as untrusted input on the OAuth path.
func TestNoCertificateKeepsGovernanceHeaders(t *testing.T) {
	var seen *http.Request
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { seen = r })
	h, _ := New(context.Background(), next, CreateConfig(), "test")

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("X-MCP-Verified-SPIFFE-ID", "spiffe://example.org/ns/admin/session/victim")
	req.Header.Set("X-Application-Context", "context-1")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if got := seen.Header.Get("X-MCP-Verified-SPIFFE-ID"); got != "" {
		t.Fatalf("verified header = %q, want stripped", got)
	}
	if got := seen.Header.Get("X-Application-Context"); got != "context-1" {
		t.Fatalf("X-Application-Context = %q, want preserved", got)
	}
}
