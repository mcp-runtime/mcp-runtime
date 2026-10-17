package platformauth

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type lifecyclePrincipalStub struct {
	current Principal
	err     error
	calls   int
}

func (s *lifecyclePrincipalStub) PrincipalForUserID(context.Context, string) (Principal, error) {
	s.calls++
	return s.current, s.err
}

func TestBearerUsesCurrentAccountAndRejectsDeactivation(t *testing.T) {
	secret := []byte("test-lifecycle-secret")
	token, err := Sign(secret, Principal{Role: RoleAdmin, Subject: "user", AuthType: "platform_jwt", Teams: []PrincipalTeam{{ID: "old-team", Role: "owner"}}}, time.Hour, []string{AudienceRuntime})
	if err != nil {
		t.Fatal(err)
	}
	resolver := &lifecyclePrincipalStub{current: Principal{Role: RoleUser, Subject: "user", Teams: []PrincipalTeam{{ID: "new-team", Role: "member"}}}}
	a := Authenticator{Secret: secret, Audience: AudienceRuntime, Principals: resolver}
	r := httptest.NewRequest(http.MethodGet, "/private", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	p, ok, err := a.AuthenticateRequest(r)
	if err != nil || !ok || p.Role != RoleUser || len(p.Teams) != 1 || p.Teams[0].ID != "new-team" {
		t.Fatalf("principal=%+v ok=%t err=%v", p, ok, err)
	}
	resolver.err = sql.ErrNoRows
	if _, ok, err = a.AuthenticateRequest(r); ok || err != nil {
		t.Fatalf("deactivated principal allowed ok=%t err=%v", ok, err)
	}
	resolver.err = errors.New("directory offline")
	if _, ok, err = a.AuthenticateRequest(r); ok || err == nil {
		t.Fatalf("directory outage accepted ok=%t err=%v", ok, err)
	}
	resolver.err = nil
	resolver.current.Subject = "another"
	if _, ok, err = a.AuthenticateRequest(r); ok || err == nil {
		t.Fatalf("mismatched subject accepted")
	}
}

func TestStaticAdministratorRemainsServiceIdentity(t *testing.T) {
	resolver := &lifecyclePrincipalStub{err: errors.New("should not resolve")}
	a := Authenticator{ServiceAPIKeys: map[string]struct{}{"service": {}}, AdminAPIKeys: map[string]struct{}{"service": {}}, Principals: resolver}
	r := httptest.NewRequest(http.MethodGet, "/admin", nil)
	r.Header.Set("x-api-key", "service")
	p, ok, err := a.AuthenticateRequest(r)
	if err != nil || !ok || !p.IsService || p.UserID() != "" || resolver.calls != 0 {
		t.Fatalf("principal=%+v ok=%t err=%v calls=%d", p, ok, err, resolver.calls)
	}
}

func TestHTTPCurrentPrincipalDoesNotCacheSuccess(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/internal/identity/principal" || r.Header.Get("Authorization") != "Bearer internal" {
			t.Error("invalid resolver request")
		}
		if calls > 1 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"principal":{"subject":"user","role":"user"}}`))
	}))
	defer server.Close()
	r := &HTTPUserKeyResolver{BaseURL: server.URL, Token: "internal", Client: server.Client()}
	p, err := r.PrincipalForUserID(t.Context(), "user")
	if err != nil || p.Subject != "user" {
		t.Fatalf("principal=%+v err=%v", p, err)
	}
	if _, err = r.PrincipalForUserID(t.Context(), "user"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deactivation not observed: %v", err)
	}
}
