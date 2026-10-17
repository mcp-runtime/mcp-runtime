package platformstore

import (
	"context"
	"strings"
	"testing"
	"time"

	"mcp-runtime/pkg/platformauth"
)

func TestEnsureTeamPasswordUserTrimsBeforeLengthCheck(t *testing.T) {
	store := NewForTest(nil)
	_, err := store.EnsureTeamPasswordUser(context.Background(), "member@example.com", " 12345678901 ")
	if err == nil {
		t.Fatal("expected short password error")
	}
	if !strings.Contains(err.Error(), "at least 12 characters") {
		t.Fatalf("expected password length error, got %v", err)
	}
}

func TestLoginAccessTokenIsPlatformOnly(t *testing.T) {
	secret := []byte("login-audience-test")
	store := NewForTest(secret)
	token, err := store.CreateAccessToken(User{ID: "user", Email: "user@example.com", Role: RoleUser}, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := platformauth.Verify(secret, token, platformauth.AudiencePlatform); err != nil {
		t.Fatal(err)
	}
	for _, audience := range []string{platformauth.AudienceRuntime, platformauth.AudienceAnalytics} {
		if _, err := platformauth.Verify(secret, token, audience); err == nil {
			t.Fatalf("login token accepted directly at %s", audience)
		}
	}
}
