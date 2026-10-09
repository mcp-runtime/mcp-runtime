package agentadapter

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestHeaderModeProxyForwardsConfiguredCredentials(t *testing.T) {
	t.Setenv("EXAMPLE_TOKEN", "synthetic-example")
	t.Setenv("EXAMPLE_PAT", "synthetic-pat")
	t.Setenv("EXAMPLE_AUTH", "Bearer synthetic")
	var calls atomic.Int32
	var example, pat, auth, session string
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		example = r.Header.Get("X-Example-Credential")
		pat = r.Header.Get("Private-Token")
		auth = r.Header.Get("Authorization")
		session = r.Header.Get("Mcp-Session-Id")
		if r.Header.Get("Accept") == "text/event-stream" {
			w.Header().Set("content-type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"ok\":true}\n\n")
			return
		}
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	t.Cleanup(upstream.Close)
	target, err := url.Parse(upstream.URL + "/pilot/mcp")
	if err != nil {
		t.Fatal(err)
	}
	base := upstream.Client().Transport.(*http.Transport).Clone()
	handler, err := NewHTTPProxyHandler(ProxyConfig{
		AuthMode:   AuthModeHeader,
		RuntimeURL: target,
		CredentialHeaders: map[string]CredentialSource{
			"X-Example-Credential": {Env: "EXAMPLE_TOKEN"},
			"Private-Token":        {Env: "EXAMPLE_PAT"},
			"Authorization":        {Env: "EXAMPLE_AUTH"},
		},
		Transport: &RuntimeTransport{Base: base},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8099/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Session-Id", "client-session")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || calls.Load() != 1 {
		t.Fatalf("status=%d calls=%d body=%s", rec.Code, calls.Load(), rec.Body.String())
	}
	if example != "synthetic-example" || pat != "synthetic-pat" || auth != "Bearer synthetic" || session != "client-session" {
		t.Fatalf("example=%q pat=%q auth=%q session=%q", example, pat, auth, session)
	}
	if strings.Contains(rec.Body.String(), "synthetic") {
		t.Fatal("response leaked a credential")
	}

	stream := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8099/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list"}}`))
	stream.Header.Set("accept", "text/event-stream")
	streamRec := httptest.NewRecorder()
	handler.ServeHTTP(streamRec, stream)
	if !strings.Contains(streamRec.Body.String(), "data:") || calls.Load() != 2 {
		t.Fatalf("stream status=%d calls=%d body=%s", streamRec.Code, calls.Load(), streamRec.Body.String())
	}
}

func TestHeaderModeDoesNotFollowRedirect(t *testing.T) {
	t.Setenv("EXAMPLE_TOKEN", "synthetic-example")
	var calls atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Redirect(w, r, "https://other.example/steal", http.StatusFound)
	}))
	t.Cleanup(upstream.Close)
	target, err := url.Parse(upstream.URL + "/pilot/mcp")
	if err != nil {
		t.Fatal(err)
	}
	base := upstream.Client().Transport.(*http.Transport).Clone()
	handler, err := NewHTTPProxyHandler(ProxyConfig{
		AuthMode:          AuthModeHeader,
		RuntimeURL:        target,
		CredentialHeaders: map[string]CredentialSource{"X-Example-Credential": {Env: "EXAMPLE_TOKEN"}},
		Transport:         &RuntimeTransport{Base: base},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8099/mcp", strings.NewReader(`{"method":"initialize"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || calls.Load() != 1 {
		t.Fatalf("status=%d calls=%d", rec.Code, calls.Load())
	}
}

func TestCredentialConflictsFailBeforeIO(t *testing.T) {
	t.Setenv("EXAMPLE_TOKEN", "synthetic-example")
	var calls atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))
	t.Cleanup(upstream.Close)
	target, err := url.Parse(upstream.URL + "/pilot/mcp")
	if err != nil {
		t.Fatal(err)
	}
	base := upstream.Client().Transport.(*http.Transport).Clone()
	transport := (&RuntimeTransport{Base: base}).withCredentialHeaders(target, map[string]CredentialSource{
		"X-Example-Credential": {Env: "EXAMPLE_TOKEN"},
	})
	req := httptest.NewRequest(http.MethodGet, target.String(), nil)
	req.Header.Set("X-Example-Credential", "other-user")
	if _, err := transport.RoundTrip(req); err == nil || calls.Load() != 0 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
	conflict := (&RuntimeTransport{Base: base, AuthHeader: "Bearer other"}).withCredentialHeaders(target, map[string]CredentialSource{
		"Authorization": {Env: "EXAMPLE_TOKEN"},
	})
	if _, err := conflict.RoundTrip(httptest.NewRequest(http.MethodGet, target.String(), nil)); err == nil || calls.Load() != 0 {
		t.Fatalf("auth conflict err=%v calls=%d", err, calls.Load())
	}
}

func TestFileCredentialRereadAndSizeLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("first-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := CredentialSource{File: path}
	got, err := source.value()
	if err != nil || got != "first-token" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	if err := os.WriteFile(path, []byte("rotated-token\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = source.value()
	if err != nil || got != "rotated-token" {
		t.Fatalf("rotated=%q err=%v", got, err)
	}
	if err := os.WriteFile(path, append(bytesOf('a', 8192), []byte("\r\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := source.value(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytesOf('b', 8193), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := source.value(); err == nil || strings.Contains(err.Error(), "bbbb") {
		t.Fatalf("oversized err=%v", err)
	}
	missing := CredentialSource{File: filepath.Join(dir, "missing")}
	if _, err := missing.value(); err == nil {
		t.Fatal("missing file succeeded")
	}
}

func TestHeaderModeRejectsCertificateAndInsecureTLS(t *testing.T) {
	target, _ := url.Parse("https://mcp.example.com/pilot/mcp")
	cfg := ProxyConfig{AuthMode: AuthModeHeader, RuntimeURL: target, CertificateIdentity: true}
	if err := cfg.Validate(); err == nil {
		t.Fatal("certificate identity was accepted")
	}
	insecure := ProxyConfig{
		AuthMode:   AuthModeHeader,
		RuntimeURL: target,
		CredentialHeaders: map[string]CredentialSource{
			"X-Example-Credential": {Env: "EXAMPLE_TOKEN"},
		},
		Transport: &RuntimeTransport{Base: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}},
	}
	if err := insecure.Validate(); err == nil {
		t.Fatal("insecure TLS was accepted")
	}
}

func TestPassthroughHeaderModeForwardsClientCredential(t *testing.T) {
	var got string
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Example-Credential")
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)
	target, err := url.Parse(upstream.URL + "/pilot/mcp")
	if err != nil {
		t.Fatal(err)
	}
	base := upstream.Client().Transport.(*http.Transport).Clone()
	handler, err := NewHTTPProxyHandler(ProxyConfig{AuthMode: AuthModeHeader, RuntimeURL: target, Transport: &RuntimeTransport{Base: base}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8099/mcp", strings.NewReader(`{"method":"initialize"}`))
	req.Header.Set("X-Example-Credential", "client-supplied")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || got != "client-supplied" {
		t.Fatalf("status=%d got=%q", rec.Code, got)
	}
}

func bytesOf(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}
