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

func TestGatewayInjectsHeaderForEntireMCPProtocol(t *testing.T) {
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
	d := newDemo(resource, &mcpauth.JWTVerifier{Issuer: "https://auth.example", Audience: resource, JWKSURL: jwks.URL, RequiredScopes: map[string]bool{"tools:read": true}, TokenTypes: []string{"at+jwt"}, RequireType: true})
	upstreamHandler := testUpstreamHandler()
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mcp" {
			calls++
			u, p, ok := r.BasicAuth()
			if !ok || u != demoUsername || p != demoPassword || r.Header.Get("Cookie") != "" {
				t.Error("gateway failed to replace bearer with upstream Basic header")
			}
		}
		upstreamHandler.ServeHTTP(w, r)
	}))
	defer upstream.Close()
	d.upstream = upstream.URL
	subject := completeProvider(t, d)
	token := func(audience, subject string, expiry time.Time) string {
		header, _ := json.Marshal(map[string]any{"alg": "RS256", "typ": "at+jwt", "kid": "test"})
		claims, _ := json.Marshal(map[string]any{"iss": "https://auth.example", "aud": audience, "sub": subject, "exp": expiry.Unix(), "scope": "tools:read"})
		input := b64(header) + "." + b64(claims)
		digest := sha256.Sum256([]byte(input))
		sig, err := rsa.SignPKCS1v15(rand.Reader, private, crypto.SHA256, digest[:])
		if err != nil {
			t.Fatal(err)
		}
		return input + "." + b64(sig)
	}
	handler := d.handler()
	call := func(bearer, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", resource, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		r.Header.Set("MCP-Protocol-Version", "2025-11-25")
		r.Header.Set("Cookie", "client-cookie=must-not-forward")
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`
	for _, bearer := range []string{"", "invalid", token("https://other.example/mcp", d.config.SubjectPrefix+subject, time.Now().Add(time.Hour)), token(resource, d.config.SubjectPrefix+subject, time.Now().Add(-time.Hour))} {
		if w := call(bearer, initialize); w.Code != 401 {
			t.Fatalf("invalid token status %d", w.Code)
		}
	}
	for _, other := range []string{"keycloak:user", d.config.SubjectPrefix + "unconnected"} {
		if w := call(token(resource, other, time.Now().Add(time.Hour)), initialize); w.Code != 403 {
			t.Fatalf("unconnected login status %d", w.Code)
		}
	}
	if calls != 0 {
		t.Fatal("upstream reached before OAuth and credential binding were verified")
	}
	bearer := token(resource, d.config.SubjectPrefix+subject, time.Now().Add(time.Hour))
	if w := call(bearer, initialize); w.Code != 200 || !strings.Contains(w.Body.String(), "header-authenticated-upstream") {
		t.Fatalf("initialize: %d %s", w.Code, w.Body.String())
	}
	for range 2 {
		w := call(bearer, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"upstream_status","arguments":{}}}`)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `\"authenticated\":true`) || strings.Contains(w.Body.String(), demoPassword) {
			t.Fatalf("tool result %d %s", w.Code, w.Body.String())
		}
	}
	refreshed := token(resource, d.config.SubjectPrefix+subject, time.Now().Add(2*time.Hour))
	if w := call(refreshed, initialize); w.Code != 200 {
		t.Fatal("refreshed OAuth token could not reuse its credential connection")
	}
	before := calls
	now := time.Now().Add(2 * time.Hour)
	d.now = func() time.Time { return now }
	if w := call(refreshed, initialize); w.Code != 403 || calls != before {
		t.Fatal("expired credential cache was used")
	}
}
