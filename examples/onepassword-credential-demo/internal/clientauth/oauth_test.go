package clientauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDesktopLoginUsesSavedFieldsBeforeOAuthAndKeepsBrowserBinding(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
if [ -n "$OP_SERVICE_ACCOUNT_TOKEN" ] || [ -n "$OP_CONNECT_TOKEN" ] || [ -n "$OP_SESSION_test" ]; then exit 9; fi
case "$2" in
  op://Test/Demo/username) printf 'demo-user\n' ;;
  op://Test/Demo/password) printf 'demo-password\n' ;;
  *) exit 8 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "op"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "must-not-use")
	t.Setenv("OP_CONNECT_TOKEN", "must-not-use")
	t.Setenv("OP_SESSION_test", "must-not-use")
	var origin string
	var callback bool
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/authorize":
			_, _ = w.Write([]byte(`<form><input name="consent_id" value="consent"></form>`))
		case "/auth/authorize/consent":
			_ = r.ParseForm()
			if r.PostForm.Get("decision") != "approve" {
				t.Error("consent was not explicit")
			}
			http.SetCookie(w, &http.Cookie{Name: "broker-binding", Value: "browser", Path: "/auth/", Secure: true, HttpOnly: true})
			http.Redirect(w, r, origin+"/mcp/login/authorize", 303)
		case "/mcp/login/authorize":
			http.SetCookie(w, &http.Cookie{Name: "gateway-binding", Value: "browser", Path: "/mcp/login", Secure: true, HttpOnly: true})
			_, _ = w.Write([]byte(`<form><input name="request" value="request"><input name="csrf" value="csrf"></form>`))
		case "/mcp/login/approve":
			cookie, err := r.Cookie("gateway-binding")
			_ = r.ParseForm()
			if err != nil || cookie.Value != "browser" || r.Header.Get("Origin") != origin || r.PostForm.Get("csrf") != "csrf" || r.PostForm.Get("username") != "demo-user" || r.PostForm.Get("password") != "demo-password" {
				t.Error("desktop did not submit the expected saved Login with its own browser binding")
			}
			http.Redirect(w, r, origin+"/auth/identity/callback", 303)
		case "/auth/identity/callback":
			cookie, err := r.Cookie("broker-binding")
			if err != nil || cookie.Value != "browser" {
				t.Error("broker browser binding was lost")
			}
			http.Redirect(w, r, origin+"/callback?code=opaque&state=client-state&iss="+origin+"/auth", 303)
		case "/callback":
			callback = true
			_, _ = w.Write([]byte("complete"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer s.Close()
	origin = s.URL
	options := Options{Issuer: origin + "/auth", Resource: origin + "/mcp", Mode: "desktop", ApproveConsent: true, UsernameRef: "op://Test/Demo/username", PasswordRef: "op://Test/Demo/password", HTTPClient: s.Client()}
	if err := desktopLogin(context.Background(), options, origin+"/auth/authorize", origin+"/callback"); err != nil {
		t.Fatal(err)
	}
	if !callback {
		t.Fatal("broker OAuth callback did not complete after upstream Login validation")
	}
}

func TestDesktopLoginRejectsExternalRedirectBeforeVaultAccess(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.example/authorize", 302)
	}))
	defer s.Close()
	err := desktopLogin(context.Background(), Options{Issuer: s.URL + "/auth", Resource: s.URL + "/mcp", HTTPClient: s.Client()}, s.URL+"/auth/authorize", s.URL+"/callback")
	if err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("external redirect was followed: %v", err)
	}
}

func TestDesktopLoginRequiresConsentOptIn(t *testing.T) {
	_, _, err := New(Options{Mode: "desktop"})
	if err == nil || !strings.Contains(err.Error(), "approve-consent") {
		t.Fatal("desktop mode silently approved a new OAuth client")
	}
}
