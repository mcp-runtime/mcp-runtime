package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	mcpauth "github.com/Agent-Hellboy/mcp-auth/auth-client/go/mcpauth"
)

func TestUpstreamCredentialsGateOAuthAuthorization(t *testing.T) {
	d := newDemo("https://demo.example/demo/mcp", &mcpauth.JWTVerifier{Issuer: "https://auth.example"})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != "demo-user" || p != "demo-password" {
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write([]byte(`{"subject":"demo-user"}`))
	}))
	defer upstream.Close()
	d.upstream = upstream.URL
	verifier := strings.Repeat("v", 43)
	sum := sha256.Sum256([]byte(verifier))
	values := url.Values{"response_type": {"code"}, "client_id": {"mcp-auth-demo"}, "redirect_uri": {"https://auth.example/identity/callback"}, "state": {"broker-state"}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}}
	w := httptest.NewRecorder()
	d.handler().ServeHTTP(w, httptest.NewRequest("GET", "https://demo.example/demo/mcp/login/authorize?"+values.Encode(), nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `autocomplete="current-password"`) {
		t.Fatalf("pre-token credential login not available: %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Location") != "" {
		t.Fatal("authorization completed before credentials were supplied")
	}
}

type providerAttempt struct {
	id, csrf, verifier string
	cookies            []*http.Cookie
}

func beginProvider(t *testing.T, d *demo) providerAttempt {
	t.Helper()
	verifier := strings.Repeat("v", 43)
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{"response_type": {"code"}, "client_id": {brokerClientID}, "redirect_uri": {d.callbackURL()}, "state": {"broker-state"}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}}
	w := httptest.NewRecorder()
	d.handler().ServeHTTP(w, httptest.NewRequest("GET", d.origin+d.basePath+"/login/authorize?"+q.Encode(), nil))
	if w.Code != 200 {
		t.Fatalf("begin: %d %s", w.Code, w.Body.String())
	}
	field := func(name string) string {
		m := regexp.MustCompile(`name="` + name + `" value="([^"]+)"`).FindStringSubmatch(w.Body.String())
		if len(m) != 2 {
			t.Fatalf("missing %s", name)
		}
		return m[1]
	}
	return providerAttempt{id: field("request"), csrf: field("csrf"), verifier: verifier, cookies: w.Result().Cookies()}
}
func approveProvider(d *demo, a providerAttempt, username, password, origin string, withCookie bool) *httptest.ResponseRecorder {
	values := url.Values{"request": {a.id}, "csrf": {a.csrf}, "username": {username}, "password": {password}}
	r := httptest.NewRequest("POST", d.origin+d.basePath+"/login/approve", strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", origin)
	if withCookie {
		for _, cookie := range a.cookies {
			r.AddCookie(cookie)
		}
	}
	w := httptest.NewRecorder()
	d.handler().ServeHTTP(w, r)
	return w
}
func exchangeProvider(d *demo, code, verifier string) *httptest.ResponseRecorder {
	values := url.Values{"grant_type": {"authorization_code"}, "client_id": {brokerClientID}, "redirect_uri": {d.callbackURL()}, "code": {code}, "code_verifier": {verifier}}
	r := httptest.NewRequest("POST", d.origin+d.basePath+"/login/token", strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	d.handler().ServeHTTP(w, r)
	return w
}
func completeProvider(t *testing.T, d *demo) string {
	t.Helper()
	a := beginProvider(t, d)
	w := approveProvider(d, a, demoUsername, demoPassword, d.origin, true)
	if w.Code != 303 {
		t.Fatalf("approve: %d %s", w.Code, w.Body.String())
	}
	u, _ := url.Parse(w.Header().Get("Location"))
	token := exchangeProvider(d, u.Query().Get("code"), a.verifier)
	if token.Code != 200 {
		t.Fatalf("exchange: %d %s", token.Code, token.Body.String())
	}
	var response struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.Unmarshal(token.Body.Bytes(), &response)
	r := httptest.NewRequest("GET", d.origin+d.basePath+"/login/userinfo", nil)
	r.Header.Set("Authorization", "Bearer "+response.AccessToken)
	info := httptest.NewRecorder()
	d.handler().ServeHTTP(info, r)
	var identity struct {
		Subject string `json:"sub"`
	}
	_ = json.Unmarshal(info.Body.Bytes(), &identity)
	if info.Code != 200 || identity.Subject == "" {
		t.Fatal("missing opaque credential identity")
	}
	return identity.Subject
}

func TestProviderRequiresValidatedCredentialsPKCEAndOriginalBrowser(t *testing.T) {
	d := newDemo("https://demo.example/demo/mcp", &mcpauth.JWTVerifier{Issuer: "https://auth.example"})
	upstream := httptest.NewServer(testUpstreamHandler())
	defer upstream.Close()
	d.upstream = upstream.URL
	a := beginProvider(t, d)
	for _, tc := range []struct {
		origin string
		cookie bool
	}{{"https://evil.example", true}, {d.origin, false}} {
		if w := approveProvider(d, a, demoUsername, demoPassword, tc.origin, tc.cookie); w.Code != 403 {
			t.Fatalf("unbound approval: %d", w.Code)
		}
	}
	if w := approveProvider(d, a, demoUsername, "wrong", d.origin, true); w.Code != 401 || w.Header().Get("Location") != "" {
		t.Fatalf("invalid upstream login: %d", w.Code)
	}
	if len(d.codes) != 0 || len(d.logins) != 0 {
		t.Fatal("credentials or grant created before upstream validation")
	}
	w := approveProvider(d, a, demoUsername, demoPassword, d.origin, true)
	if w.Code != 303 {
		t.Fatalf("valid approval %d: %s", w.Code, w.Body.String())
	}
	u, _ := url.Parse(w.Header().Get("Location"))
	code := u.Query().Get("code")
	if code == "" || u.Query().Get("state") != "broker-state" {
		t.Fatal("broker callback not preserved")
	}
	if w := exchangeProvider(d, code, strings.Repeat("x", 43)); w.Code != 400 {
		t.Fatal("incorrect PKCE accepted")
	}
	w = exchangeProvider(d, code, a.verifier)
	if w.Code != 200 {
		t.Fatalf("correct exchange: %d", w.Code)
	}
	if strings.Contains(w.Body.String(), demoUsername) || strings.Contains(w.Body.String(), demoPassword) {
		t.Fatal("credential appeared in provider token response")
	}
	if w := exchangeProvider(d, code, a.verifier); w.Code != 400 {
		t.Fatal("authorization code was reusable")
	}
	if w := approveProvider(d, a, demoUsername, demoPassword, d.origin, true); w.Code != 410 {
		t.Fatal("login approval was reusable")
	}
}
