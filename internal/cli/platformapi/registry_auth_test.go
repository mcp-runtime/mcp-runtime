package platformapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestNewPlatformClientWithAPIKeyRequiresHTTPSOrigin(t *testing.T) {
	for _, base := range []string{"", "http://platform.example.com", "https://user:pass@platform.example.com", "https://platform.example.com?x=1", "platform.example.com"} {
		if _, err := NewPlatformClientWithAPIKey(base, "admin"); err == nil {
			t.Fatalf("base %q must be rejected", base)
		}
	}
	if _, err := NewPlatformClientWithAPIKey("https://platform.example.com", " "); err == nil {
		t.Fatal("empty API key must be rejected")
	}
}

func TestNewPlatformClientWithAPIKeySendsKeyToRegistryAdminEndpoint(t *testing.T) {
	client, err := NewPlatformClientWithAPIKey("https://platform.example.com/api/v1/", "admin-key")
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://platform.example.com/api/v1/registry/pull-credentials" {
			t.Fatalf("url = %s", r.URL)
		}
		if r.Header.Get("x-api-key") != "admin-key" {
			t.Fatal("administrator key header missing")
		}
		return &http.Response{StatusCode: http.StatusMethodNotAllowed, Body: http.NoBody, Header: http.Header{}}, nil
	})
	if err := client.CheckRegistryAdmin(context.Background()); err != nil {
		t.Fatal(err)
	}
	client.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: http.NoBody, Header: http.Header{}}, nil
	})
	if err := client.CheckRegistryAdmin(context.Background()); err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("non-admin key must fail preflight, got %v", err)
	}
}
