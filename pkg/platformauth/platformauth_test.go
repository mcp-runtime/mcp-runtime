package platformauth

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
)

func TestSignVerifySingleAudience(t *testing.T) {
	secret := []byte("test-secret")
	want := Principal{
		Role:              RoleUser,
		Subject:           "user-1",
		Email:             "user@example.com",
		Namespace:         "team-a",
		AllowedNamespaces: []string{"team-a", "mcp-servers"},
		Teams:             []PrincipalTeam{{ID: "team-1", Slug: "a", Name: "A", Namespace: "team-a", Role: "owner"}},
		AuthType:          "platform_jwt",
	}
	token, err := Sign(secret, want, time.Minute, []string{AudiencePlatform})
	if err != nil {
		t.Fatal(err)
	}
	for _, audience := range []string{AudiencePlatform} {
		claims, err := Verify(secret, token, audience)
		if err != nil {
			t.Fatalf("Verify(%q): %v", audience, err)
		}
		got := ToPrincipal(claims)
		if got.Subject != want.Subject || got.Email != want.Email || got.TeamRole("a") != "owner" || !got.HasNamespace("mcp-servers") {
			t.Fatalf("principal mismatch: %#v", got)
		}
	}
}

func TestVerifyRejectsInvalidClaims(t *testing.T) {
	secret := []byte("test-secret")
	p := Principal{Subject: "user-1"}
	token, err := Sign(secret, p, time.Minute, []string{AudiencePlatform})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(secret, token, AudienceRuntime); err == nil {
		t.Fatal("expected wrong audience rejection")
	}

	expired, err := Sign(secret, p, time.Nanosecond, []string{AudiencePlatform})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	if _, err := Verify(secret, expired, AudiencePlatform); err == nil {
		t.Fatal("expected expired token rejection")
	}

	claims := Claims{RegisteredClaims: jwt.RegisteredClaims{
		Issuer:    "other",
		Subject:   p.Subject,
		Audience:  jwt.ClaimStrings{AudiencePlatform},
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
	}}
	badIssuer, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(secret, badIssuer, AudiencePlatform); err == nil {
		t.Fatal("expected issuer rejection")
	}

	noneToken, err := jwt.NewWithClaims(jwt.SigningMethodNone, claims).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(secret, noneToken, AudiencePlatform); err == nil {
		t.Fatal("expected none algorithm rejection")
	}
}

func TestClaimsPrincipalRoundTrip(t *testing.T) {
	want := Principal{
		Role:              RoleAdmin,
		Subject:           "admin-1",
		Email:             "admin@example.com",
		Namespace:         "team-admin",
		AllowedNamespaces: []string{"team-admin"},
		Teams:             []PrincipalTeam{{ID: "1", Slug: "admin", Name: "Admin", Namespace: "team-admin", Role: "owner"}},
		AuthType:          "user_api_key",
		APIKeyID:          "key-1",
		IsService:         true,
	}
	got := ToPrincipal(ClaimsFromPrincipal(want))
	if got.Subject != want.Subject || got.APIKeyID != want.APIKeyID || got.Teams[0] != want.Teams[0] || !got.IsService {
		t.Fatalf("round trip mismatch: %#v", got)
	}
}

func TestServiceAPIKeyDoesNotBecomeAdminWhenAdminKeysEmpty(t *testing.T) {
	auth := Authenticator{
		ServiceAPIKeys: map[string]struct{}{"service-key": {}},
		AdminAPIKeys:   map[string]struct{}{},
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("x-api-key", "service-key")

	p, ok, err := auth.AuthenticateRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected service key to authenticate")
	}
	if p.Role != RoleUser {
		t.Fatalf("role = %q, want %q", p.Role, RoleUser)
	}
}

func TestAdminAPIKeyRequiresExplicitAdminKeyEntry(t *testing.T) {
	auth := Authenticator{
		ServiceAPIKeys: map[string]struct{}{"admin-key": {}},
		AdminAPIKeys:   map[string]struct{}{"admin-key": {}},
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("x-api-key", "admin-key")

	p, ok, err := auth.AuthenticateRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected admin key to authenticate")
	}
	if p.Role != RoleAdmin {
		t.Fatalf("role = %q, want %q", p.Role, RoleAdmin)
	}
}
