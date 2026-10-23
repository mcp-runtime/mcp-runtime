package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestConnectionIsBoundToOAuthSessionAndExpires(t *testing.T) {
	now := time.Now()
	s := newDemo("https://demo.example/demo/mcp", nil)
	s.now = func() time.Time { return now }
	id := identity{subject: "alice", session: "sid-a", expires: now.Add(time.Hour)}
	ticket, err := s.ticket(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.save(ticket, "demo-user", "demo-password"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.credentials(id); !ok {
		t.Fatal("approved session has no credentials")
	}
	if _, ok := s.credentials(identity{subject: "alice", session: "sid-b", expires: id.expires}); ok {
		t.Fatal("another session received credentials")
	}
	if _, ok := s.credentials(identity{subject: "bob", session: "sid-a", expires: id.expires}); ok {
		t.Fatal("another subject received credentials")
	}
	if err := s.save(ticket, "demo-user", "demo-password"); err == nil {
		t.Fatal("ticket was reusable")
	}
	now = now.Add(16 * time.Minute)
	if _, ok := s.credentials(id); ok {
		t.Fatal("expired credentials were retained")
	}
}

func TestInvalidOrExpiredTicketCannotSaveCredentials(t *testing.T) {
	s := newDemo("https://demo.example/demo/mcp", nil)
	now := time.Now()
	s.now = func() time.Time { return now }
	ticket, _ := s.ticket(identity{subject: "alice", session: "sid", expires: now.Add(time.Hour)})
	if err := s.save(ticket, "wrong", "wrong"); err == nil {
		t.Fatal("invalid upstream login accepted")
	}
	if _, ok := s.credentials(identity{subject: "alice", session: "sid"}); ok {
		t.Fatal("invalid login was cached")
	}
	now = now.Add(6 * time.Minute)
	if err := s.save(ticket, "demo-user", "demo-password"); err == nil {
		t.Fatal("expired connection accepted")
	}
}

func TestCredentialFormRequiresSameOriginAndCookieAndNeverEchoesSecrets(t *testing.T) {
	s := newDemo("https://demo.example/demo/mcp", nil)
	ticket, _ := s.ticket(identity{subject: "alice", session: "sid", expires: time.Now().Add(time.Hour)})
	get := httptest.NewRecorder()
	s.page(get, httptest.NewRequest("GET", "https://demo.example/demo/mcp/connect?ticket="+ticket, nil))
	if get.Code != http.StatusOK {
		t.Fatalf("form status %d", get.Code)
	}
	// no-referrer makes browsers send Origin: null for a form navigation.
	// Keep Origin on same-origin submissions while excluding cross-origin referrers.
	if get.Header().Get("Referrer-Policy") != "same-origin" {
		t.Fatal("form policy must preserve its same-origin POST Origin")
	}
	if !strings.Contains(get.Body.String(), `autocomplete="current-password"`) {
		t.Fatal("form does not support password managers")
	}
	cookies := get.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly {
		t.Fatal("missing protected CSRF cookie")
	}
	again := httptest.NewRecorder()
	s.page(again, httptest.NewRequest("GET", "https://demo.example/demo/mcp/connect?ticket="+ticket, nil))
	if len(again.Result().Cookies()) != 1 || again.Result().Cookies()[0].Value != cookies[0].Value {
		t.Fatal("opening the connection link twice invalidated the first form")
	}
	post := func(origin string, cookie bool) *httptest.ResponseRecorder {
		values := url.Values{"ticket": {ticket}, "csrf": {cookies[0].Value}, "username": {"demo-user"}, "password": {"demo-password"}}
		r := httptest.NewRequest("POST", "https://demo.example/demo/mcp/connect", strings.NewReader(values.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", origin)
		if cookie {
			r.AddCookie(cookies[0])
		}
		w := httptest.NewRecorder()
		s.page(w, r)
		return w
	}
	if w := post("https://evil.example", true); w.Code != 403 {
		t.Fatalf("cross-origin status %d", w.Code)
	}
	if w := post("https://demo.example", false); w.Code != 403 {
		t.Fatalf("missing-cookie status %d", w.Code)
	}
	w := post("https://demo.example", true)
	if w.Code != 200 {
		t.Fatalf("valid post status %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "demo-password") || strings.Contains(w.Body.String(), "demo-user") {
		t.Fatal("credential reflected")
	}
}

func TestUpstreamUsesSavedBasicCredentialWithoutForwardingOAuthToken(t *testing.T) {
	var calls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		u, p, ok := r.BasicAuth()
		if !ok || u != "demo-user" || p != "demo-password" {
			t.Error("wrong upstream authorization")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"authenticated": true, "service": "test-upstream"})
	}))
	defer upstream.Close()
	s := newDemo("https://demo.example/demo/mcp", nil)
	s.upstream = upstream.URL
	id := identity{subject: "alice", session: "sid", expires: time.Now().Add(time.Hour)}
	if _, err := s.callUpstream(context.Background(), id); err == nil {
		t.Fatal("unconnected session reached upstream")
	}
	if calls != 0 {
		t.Fatal("unauthorized upstream call")
	}
	ticket, _ := s.ticket(id)
	_ = s.save(ticket, "demo-user", "demo-password")
	for range 2 {
		if _, err := s.callUpstream(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("got %d calls", calls)
	}
}
