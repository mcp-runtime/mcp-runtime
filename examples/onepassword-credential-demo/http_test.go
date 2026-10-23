package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mcpauth "github.com/Agent-Hellboy/mcp-auth/auth-client/go/mcpauth"
)

func TestHTTPRequiresOAuthAndBindsToolsToSession(t *testing.T) {
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.RawURLEncoding.EncodeToString
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{"kty": "RSA", "kid": "test", "alg": "RS256", "use": "sig", "n": b64(private.N.Bytes()), "e": b64(big.NewInt(int64(private.E)).Bytes())}}})
	}))
	defer jwks.Close()
	resource := "https://demo.example/demo/mcp"
	d := newDemo(resource, &mcpauth.JWTVerifier{Issuer: "https://auth.example", Audience: resource, JWKSURL: jwks.URL, RequiredScopes: map[string]bool{"tools:read": true}})
	handler := d.handler()
	token := func(audience, subject, session string, expiry time.Time) string {
		header, _ := json.Marshal(map[string]any{"alg": "RS256", "typ": "at+jwt", "kid": "test"})
		claims, _ := json.Marshal(map[string]any{"iss": "https://auth.example", "aud": audience, "sub": subject, "jti": session, "exp": expiry.Unix(), "scope": "tools:read"})
		input := b64(header) + "." + b64(claims)
		digest := sha256.Sum256([]byte(input))
		signature, err := rsa.SignPKCS1v15(rand.Reader, private, crypto.SHA256, digest[:])
		if err != nil {
			t.Fatal(err)
		}
		return input + "." + b64(signature)
	}
	call := func(bearer, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", resource, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		r.Header.Set("MCP-Protocol-Version", "2025-11-25")
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`
	if w := call("", initialize); w.Code != 401 || !strings.Contains(w.Header().Get("WWW-Authenticate"), "resource_metadata") {
		t.Fatalf("anonymous status %d", w.Code)
	}
	for _, bearer := range []string{"invalid", token("https://other.example/mcp", "alice", "sid-a", time.Now().Add(time.Hour)), token(resource, "alice", "sid-a", time.Now().Add(-time.Hour)), token(resource, "alice", "", time.Now().Add(time.Hour))} {
		if w := call(bearer, initialize); w.Code != 401 {
			t.Fatalf("invalid identity status %d", w.Code)
		}
	}
	bearer := token(resource, "alice", "sid-a", time.Now().Add(time.Hour))
	if w := call(bearer, initialize); w.Code != 200 || !strings.Contains(w.Body.String(), "onepassword-credential-demo") {
		t.Fatalf("initialize %d: %s", w.Code, w.Body.String())
	}
	connect := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"connect_upstream","arguments":{}}}`
	w := call(bearer, connect)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "connect_url") {
		t.Fatalf("connect %d: %s", w.Code, w.Body.String())
	}
	d.mu.Lock()
	if len(d.pending) != 1 {
		t.Fatal("connection ticket not created")
	}
	for _, p := range d.pending {
		if p.identity.subject != "alice" || p.identity.session != "sid-a" {
			t.Fatal("ticket not bound to verified OAuth identity")
		}
	}
	d.mu.Unlock()
}
