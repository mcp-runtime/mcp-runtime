package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func decodeAuthBody(t *testing.T, body string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode auth body %q: %v", body, err)
	}
	return out
}

func TestHandleLoginReturnsCSRFTokenMatchingSession(t *testing.T) {
	previousHook := authLoginHooks.Password
	authLoginHooks.Password = func(context.Context, string, string, string) (sessionPrincipal, string, time.Time, error) {
		return sessionPrincipal{Role: "user", Subject: "user-1", AuthType: "platform_jwt"}, "platform-token", time.Now().Add(15 * time.Minute), nil
	}
	defer func() { authLoginHooks.Password = previousHook }()

	store := newUISessionStore(time.Now)
	rec := httptest.NewRecorder()
	handleLogin("", "api-secret", "http://api.example", store).ServeHTTP(
		rec,
		httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(`{"email":"a@b.c","password":"pw"}`)),
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	body := decodeAuthBody(t, rec.Body.String())
	token, _ := body["csrf_token"].(string)
	if token == "" {
		t.Fatal("login response must carry a csrf_token")
	}

	// The token the client receives must be the one the session verifies with.
	var sessionID string
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			sessionID = c.Value
		}
	}
	if sessionID == "" {
		t.Fatal("login did not set a session cookie")
	}
	sess, ok := store.Get(sessionID)
	if !ok {
		t.Fatal("session not stored")
	}
	if sess.CSRFToken != token {
		t.Fatalf("csrf_token %q does not match session token %q", token, sess.CSRFToken)
	}
	// The session id itself must never be handed to JavaScript.
	if strings.Contains(rec.Body.String(), sessionID) {
		t.Fatal("login body leaked the session id")
	}
}

func TestHandleStatusReturnsCSRFTokenForAuthenticatedSession(t *testing.T) {
	store := newUISessionStore(time.Now)
	sess, err := store.Create(context.Background(), uiSession{
		Principal:          sessionPrincipal{Role: "user", Subject: "user-1"},
		UpstreamAuthHeader: "Bearer t",
	})
	if err != nil {
		t.Fatalf("createSession() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/auth/status", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	rec := httptest.NewRecorder()
	handleStatus(store).ServeHTTP(rec, req)

	body := decodeAuthBody(t, rec.Body.String())
	if body["authenticated"] != true {
		t.Fatalf("authenticated = %v", body["authenticated"])
	}
	// A reloaded tab recovers its token here rather than being unable to write.
	if body["csrf_token"] != sess.CSRFToken {
		t.Fatalf("csrf_token = %v, want %q", body["csrf_token"], sess.CSRFToken)
	}
}

func TestHandleStatusOmitsCSRFTokenWhenSignedOut(t *testing.T) {
	store := newUISessionStore(time.Now)
	rec := httptest.NewRecorder()
	handleStatus(store).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth/status", nil))

	body := decodeAuthBody(t, rec.Body.String())
	if body["authenticated"] != false {
		t.Fatalf("authenticated = %v, want false", body["authenticated"])
	}
	if _, present := body["csrf_token"]; present {
		t.Fatal("signed-out status must not carry a csrf_token")
	}
}

func TestLogoutInvalidatesCSRFToken(t *testing.T) {
	store := newUISessionStore(time.Now)
	sess, err := store.Create(context.Background(), uiSession{UpstreamAuthHeader: "Bearer t"})
	if err != nil {
		t.Fatalf("createSession() error = %v", err)
	}
	oldToken := sess.CSRFToken

	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	handleLogout(store).ServeHTTP(httptest.NewRecorder(), req)

	if _, ok := store.Get(sess.ID); ok {
		t.Fatal("logout must delete the session")
	}

	// A write replayed with the old token after logout must not be accepted.
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("upstream must not be called after logout")
	}))
	t.Cleanup(upstream.Close)
	base, err := parseRuntimeUpstream(upstream.URL)
	if err != nil {
		t.Fatalf("parseRuntimeUpstream() error = %v", err)
	}
	proxy := newSessionProxyWithUpstreams(testExchangeBase(t), base, base, store)

	writeReq := httptest.NewRequest(http.MethodPost, "/api/ui/v1/user/api-keys", strings.NewReader(`{"name":"k"}`))
	writeReq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	writeReq.Header.Set(csrfHeaderName, oldToken)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, writeReq)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}
