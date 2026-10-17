package auth

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPasswordLoginPreservesIdentityExpiryAndCredentialContract(t *testing.T) {
	client := Client{HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://platform.invalid/prefix/api/v1/auth/login" || r.Method != "POST" || r.Header.Get("x-mcp-source") != "ui" {
			t.Fatalf("unexpected auth request: %v", r)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"access_token":"synthetic","expires_in":60,"user":{"id":" user-a ","email":" a@example.invalid ","role":""}}`))}, nil
	})}}
	before := time.Now().UTC()
	principal, token, expiry, err := client.Password(context.Background(), "https://platform.invalid/prefix", "a@example.invalid", "synthetic")
	if err != nil || principal.Subject != "user-a" || principal.Email != "a@example.invalid" || principal.Role != "user" || principal.AuthType != "platform_jwt" || token != "synthetic" || expiry.Before(before.Add(time.Minute)) || expiry.After(time.Now().Add(time.Minute)) {
		t.Fatalf("principal=%+v,expiry=%v,err=%v", principal, expiry, err)
	}
}

func TestPasswordLoginRejectsIncompleteSuccess(t *testing.T) {
	for _, response := range []string{`{}`, `{"access_token":"synthetic"}`, `{"access_token":"synthetic","expires_in":-1}`} {
		client := Client{HTTP: &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(response))}, nil
		})}}
		if _, _, _, err := client.Password(context.Background(), "https://platform.invalid", "a@example.invalid", "synthetic"); err == nil {
			t.Fatalf("incomplete token accepted: %s", response)
		}
	}
}
