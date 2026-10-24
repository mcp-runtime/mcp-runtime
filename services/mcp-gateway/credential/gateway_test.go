package credential

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type env struct {
	t        *testing.T
	as       *FakeAS
	up       *FakeUpstream
	cfg      *Config
	store    *Store
	gw       *httptest.Server
	validate func(r *http.Request) bool
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, as: NewFakeAS(t), up: NewFakeUpstream(t)}
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	var key *rsa.PrivateKey = newKey(t)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	e.up.Behavior = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/check" {
			if e.validate != nil && !e.validate(r) {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.Header.Get("X-Demo-Password"), "rotated") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost {
			recs := e.up.Records() // the fake already consumed the body
			if strings.Contains(recs[len(recs)-1].Body, "initialize") {
				w.Header().Set("Mcp-Session-Id", "upstream-session-1")
			}
		}
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}
	e.validate = func(r *http.Request) bool {
		return r.Header.Get("X-Demo-Username") == "alice" && r.Header.Get("X-Demo-Password") == tSentinel
	}
	raw := map[string]any{
		"allow_insecure_http":      true,
		"authorization_server":     map[string]any{"issuer": e.as.Issuer()},
		"session_binding_key_file": write("session.key", strings.Repeat("k", 40)),
		"services": []any{map[string]any{
			"id": "demo", "resource_url": tResource,
			"upstream_url":    e.up.Server.URL + "/mcp",
			"validation_url":  e.up.Server.URL + "/auth/check",
			"required_scopes": []string{"tools:read"},
			"resource_client": map[string]any{"client_id": "gateway-demo", "key_id": "gw-1", "private_key_file": write("client.pem", pemKey)},
			"form_fields": []any{
				map[string]any{"name": "username", "label": "Username"},
				map[string]any{"name": "password", "label": "Password", "secret": true},
			},
			"headers": []any{
				map[string]any{"name": "X-Demo-Username", "type": "direct", "field": "username"},
				map[string]any{"name": "X-Demo-Password", "type": "direct", "field": "password"},
			},
		}},
	}
	data, _ := json.Marshal(raw)
	cfg, err := ParseConfig(data)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	e.cfg = cfg
	e.store = &Store{TTL: time.Hour, Max: 100}
	e.gw = httptest.NewServer(New(cfg, e.store, nil))
	t.Cleanup(e.gw.Close)
	return e
}

// signIn stores values the way the sign-in page does and returns the subject.
func (e *env) signIn(user, pass string) (string, error) {
	svc := e.cfg.Services[0]
	gw := New(e.cfg, e.store, nil)
	random, err := gw.authenticateForm(nil2ctx(), svc, SubjectPrefix(svc.ID), map[string]string{"username": user, "password": pass})
	if err != nil {
		return "", err
	}
	return SubjectPrefix(svc.ID) + random, nil
}

func (e *env) call(method, token, body string, hdr map[string]string) *http.Response {
	req, _ := http.NewRequest(method, e.gw.URL+"/demo/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	return resp
}

func (e *env) upstreamMCP() []Record {
	var out []Record
	for _, r := range e.up.Records() {
		if r.URL == "/mcp" {
			out = append(out, r)
		}
	}
	return out
}

const initBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`

func TestInjectsExactlyTheValuesFromSignIn(t *testing.T) {
	e := newEnv(t)
	sub, err := e.signIn("alice", tSentinel)
	if err != nil {
		t.Fatal(err)
	}
	tok := e.as.Token(TokenOpts{Subject: sub})
	resp := e.call("POST", tok, initBody, map[string]string{"X-Demo-Password": "attacker", "Cookie": "a=b", "X-Forwarded-For": "1.2.3.4"})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	recs := e.upstreamMCP()
	if len(recs) != 1 {
		t.Fatalf("upstream saw %d requests", len(recs))
	}
	h := recs[0].Header
	if h.Get("X-Demo-Username") != "alice" || h.Get("X-Demo-Password") != tSentinel {
		t.Fatalf("injected headers wrong: %v", h)
	}
	for _, name := range []string{"Authorization", "Cookie", "X-Forwarded-For"} {
		if h.Get(name) != "" {
			t.Fatalf("%s leaked upstream", name)
		}
	}
	if strings.Contains(fmt.Sprint(resp.Header), tSentinel) || strings.Contains(string(body), tSentinel) {
		t.Fatal("a credential value was returned to the client")
	}
	sid := resp.Header.Get("Mcp-Session-Id")
	if sid == "" || sid == "upstream-session-1" {
		t.Fatalf("session id not bound: %q", sid)
	}
	// Later requests carry the bound session id; the upstream sees its own.
	resp = e.call("POST", tok, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, map[string]string{"Mcp-Session-Id": sid})
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("second call %d", resp.StatusCode)
	}
	if got := e.upstreamMCP()[1].Header.Get("Mcp-Session-Id"); got != "upstream-session-1" {
		t.Fatalf("upstream session = %q", got)
	}
}

func TestSignInRefusesValuesTheUpstreamRejects(t *testing.T) {
	e := newEnv(t)
	if _, err := e.signIn("alice", "wrong"); err != ErrRejected {
		t.Fatalf("err = %v, want ErrRejected", err)
	}
	if e.store.Len() != 0 {
		t.Fatal("rejected values were stored")
	}
}

func TestSignInReportsUnavailableUpstream(t *testing.T) {
	e := newEnv(t)
	e.up.Server.Close()
	if _, err := e.signIn("alice", tSentinel); err != ErrUnavailable {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestMissingValuesAskToSignInAgain(t *testing.T) {
	e := newEnv(t)
	tok := e.as.Token(TokenOpts{Subject: SubjectPrefix("demo") + "never-stored"})
	resp := e.call("POST", tok, initBody, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("WWW-Authenticate"), "invalid_token") {
		t.Fatalf("WWW-Authenticate = %q", resp.Header.Get("WWW-Authenticate"))
	}
	if len(e.upstreamMCP()) != 0 {
		t.Fatal("request reached the upstream without credentials")
	}
}

func TestPeopleAreIsolated(t *testing.T) {
	e := newEnv(t)
	e.validate = func(r *http.Request) bool { return r.Header.Get("X-Demo-Password") != "" }
	a, _ := e.signIn("alice", "pw-alice")
	b, _ := e.signIn("bob", "pw-bob")
	for sub, want := range map[string]string{a: "pw-alice", b: "pw-bob"} {
		resp := e.call("POST", e.as.Token(TokenOpts{Subject: sub}), initBody, nil)
		resp.Body.Close()
		recs := e.upstreamMCP()
		if got := recs[len(recs)-1].Header.Get("X-Demo-Password"); got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	}
}

func TestSessionIDFromAnotherPersonIsRefused(t *testing.T) {
	e := newEnv(t)
	e.validate = func(*http.Request) bool { return true }
	a, _ := e.signIn("alice", "x")
	b, _ := e.signIn("bob", "y")
	resp := e.call("POST", e.as.Token(TokenOpts{Subject: a}), initBody, nil)
	resp.Body.Close()
	sid := resp.Header.Get("Mcp-Session-Id")
	resp = e.call("POST", e.as.Token(TokenOpts{Subject: b}), `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, map[string]string{"Mcp-Session-Id": sid})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403", resp.StatusCode)
	}
}

func TestRevokedSessionIsBlockedEvenWithStoredValues(t *testing.T) {
	e := newEnv(t)
	sub, _ := e.signIn("alice", tSentinel)
	tok := e.as.Token(TokenOpts{Subject: sub, SessionID: "s-1"})
	resp := e.call("POST", tok, initBody, nil)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	e.as.Revoke("s-1")
	resp = e.call("POST", tok, initBody, nil)
	resp.Body.Close()
	if resp.StatusCode < 400 {
		t.Fatalf("revoked session got %d", resp.StatusCode)
	}
	if n := len(e.upstreamMCP()); n != 1 {
		t.Fatalf("upstream saw %d requests", n)
	}
}

func TestUpstreamRejectionForgetsValues(t *testing.T) {
	e := newEnv(t)
	e.validate = func(*http.Request) bool { return true }
	sub, _ := e.signIn("alice", "rotated-token")
	tok := e.as.Token(TokenOpts{Subject: sub})
	resp := e.call("POST", tok, initBody, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", resp.StatusCode)
	}
	resp = e.call("POST", tok, initBody, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("after rejection status %d, want 401", resp.StatusCode)
	}
}

func TestBearerIsRequiredAndRedirectsAreNotFollowed(t *testing.T) {
	e := newEnv(t)
	resp := e.call("POST", "", initBody, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no bearer: %d", resp.StatusCode)
	}
	sub, _ := e.signIn("alice", tSentinel)
	e.up.Behavior = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mcp" {
			http.Redirect(w, r, "https://evil.example/", http.StatusFound)
			return
		}
		w.WriteHeader(200)
	}
	resp = e.call("POST", e.as.Token(TokenOpts{Subject: sub}), initBody, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway || resp.Header.Get("Location") != "" {
		t.Fatalf("redirect handling: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestProtectedResourceMetadataAndHealth(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/healthz", "/readyz", "/.well-known/oauth-protected-resource/demo/mcp"} {
		resp, err := http.Get(e.gw.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d", p, resp.StatusCode)
		}
	}
}
