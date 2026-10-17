package platformauth

import (
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
)

func exchangeSource(t *testing.T, secret []byte, p Principal, ttl time.Duration, audiences ...string) string {
	t.Helper()
	now := time.Now()
	token, err := signUntil(secret, p, now, now.Add(ttl), audiences)
	if err != nil {
		t.Fatal(err)
	}
	return "Bearer " + token
}

func TestExchangeBoundsAudienceExpiryAndRefreshesIdentity(t *testing.T) {
	secret := []byte("exchange-test-secret")
	for _, target := range []string{AudienceRuntime, AudienceAnalytics} {
		for _, ttl := range []time.Duration{20 * time.Second, 15 * time.Minute} {
			t.Run(target+ttl.String(), func(t *testing.T) {
				resolver := &lifecyclePrincipalStub{current: Principal{Subject: "user", Role: RoleUser, Teams: []PrincipalTeam{{ID: "current-team", Role: "member"}}}}
				source := exchangeSource(t, secret, Principal{Subject: "user", Role: RoleAdmin}, ttl, AudiencePlatform)
				req := httptest.NewRequest(http.MethodGet, ExchangePath(target)+"?audience=platform-api", nil)
				req.Header.Set("Authorization", source)
				// The existing CLI duplicates its saved token into both fields.
				req.Header.Set("X-Api-Key", strings.TrimPrefix(source, "Bearer "))
				req.Header.Set("X-Forwarded-Uri", "/api/v1/auth/me")
				req.Header.Set("X-Forwarded-Host", "attacker.invalid")
				rec := httptest.NewRecorder()
				ExchangeHandler{Auth: Authenticator{Secret: secret, Audience: AudiencePlatform, Principals: resolver}, Target: target}.ServeHTTP(rec, req)
				if rec.Code != 200 || resolver.calls != 1 {
					t.Fatalf("status=%d body=%s calls=%d", rec.Code, rec.Body.String(), resolver.calls)
				}
				if rec.Header().Get("X-Api-Key") != "" {
					t.Fatal("source bearer copy leaked to backend as API key")
				}
				bearer := strings.TrimPrefix(rec.Header().Get("Authorization"), "Bearer ")
				claims, err := Verify(secret, bearer, target)
				if err != nil {
					t.Fatal(err)
				}
				original, err := Verify(secret, strings.TrimPrefix(source, "Bearer "), AudiencePlatform)
				if err != nil {
					t.Fatal(err)
				}
				if len(claims.Audience) != 1 || claims.Audience[0] != target || claims.Role != RoleUser || len(claims.Teams) != 1 || claims.Teams[0].ID != "current-team" {
					t.Fatalf("claims=%+v", claims)
				}
				if claims.ExpiresAt.After(original.ExpiresAt.Time) || claims.ExpiresAt.Sub(claims.IssuedAt.Time) > ExchangeTTL {
					t.Fatal("exchange extended source lifetime or exceeded TTL")
				}
				for _, other := range []string{AudiencePlatform, AudienceRuntime, AudienceAnalytics} {
					if other != target {
						if _, err := Verify(secret, bearer, other); err == nil {
							t.Fatalf("replay accepted at %s", other)
						}
					}
				}
				if rec.Header().Get("Cache-Control") != "no-store" || rec.Body.Len() != 0 {
					t.Fatal("token response must not be cached or copied into a body")
				}
			})
		}
	}
}

func TestExchangeRejectsInvalidSourceAndInactivePrincipal(t *testing.T) {
	secret := []byte("exchange-test-secret")
	user := Principal{Subject: "user", Role: RoleUser}
	missingExpiry := ClaimsFromPrincipal(user)
	missingExpiry.Issuer = Issuer
	missingExpiry.Audience = jwt.ClaimStrings{AudiencePlatform}
	noExpiry, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, missingExpiry).SignedString(secret)
	for _, tc := range []struct {
		name, authorization string
		resolverErr         error
		status              int
		service             bool
	}{
		{"runtime-reexchange", exchangeSource(t, secret, user, time.Minute, AudienceRuntime), nil, 401, false},
		{"analytics-reexchange", exchangeSource(t, secret, user, time.Minute, AudienceAnalytics), nil, 401, false},
		{"multi-audience", exchangeSource(t, secret, user, time.Minute, AudiencePlatform, AudienceRuntime), nil, 401, false},
		{"expired", exchangeSource(t, secret, user, -time.Minute, AudiencePlatform), nil, 401, false},
		{"missing-expiry", "Bearer " + noExpiry, nil, 401, false},
		{"forged", exchangeSource(t, []byte("wrong-key"), user, time.Minute, AudiencePlatform), nil, 401, false},
		{"malformed", "Bearer invalid", nil, 401, false},
		{"deactivated", exchangeSource(t, secret, user, time.Minute, AudiencePlatform), sql.ErrNoRows, 401, false},
		{"directory-unavailable", exchangeSource(t, secret, user, time.Minute, AudiencePlatform), errors.New("unavailable"), 503, false},
		{"service-principal", exchangeSource(t, secret, user, time.Minute, AudiencePlatform), nil, 401, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolver := &lifecyclePrincipalStub{current: user, err: tc.resolverErr}
			resolver.current.IsService = tc.service
			req := httptest.NewRequest(http.MethodGet, ExchangePath(AudienceRuntime), nil)
			req.Header.Set("Authorization", tc.authorization)
			rec := httptest.NewRecorder()
			ExchangeHandler{Auth: Authenticator{Secret: secret, Audience: AudiencePlatform, Principals: resolver}, Target: AudienceRuntime}.ServeHTTP(rec, req)
			if rec.Code != tc.status || rec.Header().Get("Authorization") != "" {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestExchangePassThroughNeverMintsIdentity(t *testing.T) {
	resolver := &lifecyclePrincipalStub{err: errors.New("must not resolve keys or anonymous requests")}
	h := ExchangeHandler{Auth: Authenticator{Secret: []byte("test-secret"), Audience: AudiencePlatform, Principals: resolver}, Target: AudienceRuntime}
	for _, key := range []string{"", "valid-only-at-destination", "invalid-key"} {
		req := httptest.NewRequest(http.MethodGet, ExchangePath(AudienceRuntime), nil)
		if key != "" {
			req.Header.Set("X-Api-Key", key)
			req.Header.Set("Authorization", "Bearer must-not-be-forwarded")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 || rec.Header().Get("Authorization") != "" || resolver.calls != 0 {
			t.Fatalf("key pass-through minted or resolved identity")
		}
		if rec.Header().Get("X-Api-Key") != key {
			t.Fatal("static API key pass-through changed credential")
		}
	}
	for _, tc := range []struct {
		target, method string
		principals     PrincipalResolver
		want           int
	}{
		{AudiencePlatform, http.MethodGet, resolver, 503},
		{AudienceRuntime, http.MethodPost, resolver, 405},
		{AudienceRuntime, http.MethodGet, nil, 503},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(tc.method, "/", nil)
		ExchangeHandler{Auth: Authenticator{Secret: []byte("test-secret"), Audience: AudiencePlatform, Principals: tc.principals}, Target: tc.target}.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("status=%d want=%d", rec.Code, tc.want)
		}
	}
}

func TestExchangeBearerRejectsRedirectAndMissingCredential(t *testing.T) {
	for _, status := range []int{200, 302, 401, 503} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != ExchangePath(AudienceRuntime) || r.Header.Get("Authorization") != "Bearer platform-token" || r.Header.Get("Cookie") != "" {
				t.Error("unexpected exchange request")
			}
			w.Header().Set("Location", "/redirect")
			w.WriteHeader(status)
		}))
		u, _ := url.Parse(server.URL)
		if _, err := ExchangeBearer(t.Context(), server.Client(), u, "Bearer platform-token", AudienceRuntime); err == nil {
			t.Fatalf("status %d without exchanged header accepted", status)
		}
		server.Close()
	}
}

func TestForwardAuthHeaderReplacementPreservesCredentialBoundaries(t *testing.T) {
	secret := []byte("forward-auth-secret")
	user := Principal{Subject: "human", Role: RoleUser}
	resolver := &lifecyclePrincipalStub{current: user}
	h := ExchangeHandler{Auth: Authenticator{Secret: secret, Audience: AudiencePlatform, Principals: resolver}, Target: AudienceRuntime}
	backend := Authenticator{Secret: secret, Audience: AudienceRuntime, Principals: resolver,
		ServiceAPIKeys: map[string]struct{}{"service-admin": {}, "service-user": {}}, AdminAPIKeys: map[string]struct{}{"service-admin": {}}}
	source := exchangeSource(t, secret, user, time.Minute, AudiencePlatform)
	for _, tc := range []struct {
		name, authorization, key string
		want                     bool
		service                  bool
		role                     string
	}{
		{"cli-human", source, strings.TrimPrefix(source, "Bearer "), true, false, RoleUser},
		{"bearer-human", source, "", true, false, RoleUser},
		{"cli-static-admin", "Bearer service-admin", "service-admin", true, true, RoleAdmin},
		{"cli-static-user", "Bearer service-user", "service-user", true, true, RoleUser},
		{"key-priority", source, "service-user", true, true, RoleUser},
		{"invalid-key", "Bearer bogus", "bogus", false, false, ""},
		{"anonymous-protected", "", "", false, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/tools", nil)
			req.Header.Set("Authorization", tc.authorization)
			req.Header.Set("X-Api-Key", tc.key)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != 200 {
				t.Fatalf("forward auth status=%d", rec.Code)
			}
			// Match Traefik authResponseHeaders replacement, including deletion
			// when an authentication response has no value for the header.
			for _, header := range []string{"Authorization", "X-Api-Key"} {
				req.Header.Del(header)
				if value := rec.Header().Get(header); value != "" {
					req.Header.Set(header, value)
				}
			}
			p, ok, err := backend.AuthenticateRequest(req)
			if err != nil || ok != tc.want {
				t.Fatalf("allowed=%t err=%v want=%t", ok, err, tc.want)
			}
			if ok && (p.IsService != tc.service || p.Role != tc.role || tc.service && p.UserID() != "") {
				t.Fatalf("unexpected identity %+v", p)
			}
			if !tc.service && ok && req.Header.Get("X-Api-Key") != "" {
				t.Fatal("source token leaked as API key")
			}
		})
	}
}
