package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"mcp-runtime/pkg/platformauth"
)

func TestProductionNoDatabasePrincipalResolverFailsClosedForHumanBearer(t *testing.T) {
	secret := []byte("lifecycle-test-secret")
	token, err := platformauth.Sign(secret, platformauth.Principal{Subject: "account", Role: platformauth.RoleAdmin}, time.Hour, []string{platformauth.AudiencePlatform})
	if err != nil {
		t.Fatal(err)
	}
	a := platformauth.Authenticator{Secret: secret, Audience: platformauth.AudiencePlatform, Principals: storeAPIKeyResolver{}, ServiceAPIKeys: map[string]struct{}{"service": {}}, AdminAPIKeys: map[string]struct{}{"service": {}}}
	r := httptest.NewRequest(http.MethodGet, "/private", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	if _, ok, err := a.AuthenticateRequest(r); ok || err == nil {
		t.Fatalf("no-DB human token accepted: ok=%t err=%v", ok, err)
	}
	r.Header.Del("Authorization")
	r.Header.Set("x-api-key", "service")
	p, ok, err := a.AuthenticateRequest(r)
	if err != nil || !ok || p.UserID() != "" || !p.IsService {
		t.Fatalf("service identity changed: principal=%+v ok=%t err=%v", p, ok, err)
	}
}
