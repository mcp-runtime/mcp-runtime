package platformclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"mcp-runtime/pkg/apihttp"
)

func TestCreatePasswordUserPreservesConflict(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apihttp.WriteEnvelope(w, 409, "conflict", "a user with this email already exists; add them as a member instead")
	}))
	defer server.Close()
	client := &Client{BaseURL: server.URL, Token: "test-token", HTTP: server.Client()}
	_, err := client.CreatePasswordUser(context.Background(), "existing@example.com", "password123", "user")
	var upstream *apihttp.Error
	if !errors.As(err, &upstream) || upstream.Status != 409 || upstream.Code != "conflict" || upstream.Message != "a user with this email already exists; add them as a member instead" {
		t.Fatalf("error=%v", err)
	}
}

func TestCreateTeamUserDoesNotExposeMalformedUpstreamBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte("SQL error with private user data"))
	}))
	defer server.Close()
	client := &Client{BaseURL: server.URL, Token: "test-token", HTTP: server.Client()}
	_, _, err := client.CreateTeamUser(context.Background(), "acme", "member@example.com", "password123", "member")
	var upstream *apihttp.Error
	if !errors.As(err, &upstream) || upstream.Status != 500 || upstream.Message != "platform identity request failed" {
		t.Fatalf("error=%v", err)
	}
}
