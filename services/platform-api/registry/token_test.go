package registry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mcp-platform-api/internal/apiauth"
	"mcp-runtime/pkg/registryauth"
)

type fakePullAuth struct{}

func (fakePullAuth) AuthenticateRegistryPullCredential(_ context.Context, user, password string) (PullScope, bool, error) {
	return PullScope{Namespace: "mcp-team-acme", Prefixes: []string{"acme"}, Repositories: []string{"mcp-gateway"}}, user == "mcp-pull-mcp-team-acme" && password == "mcpp_test", nil
}

func registryTestDeps(t *testing.T) (TokenDependencies, []byte) {
	t.Helper()
	key, cert, err := registryauth.GenerateMaterial()
	if err != nil {
		t.Fatal(err)
	}
	signer, err := registryauth.NewSigner(key, cert)
	if err != nil {
		t.Fatal(err)
	}
	return TokenDependencies{Signer: signer, Pull: fakePullAuth{}, AuthenticateRequest: func(r *http.Request) (apiauth.Principal, bool, error) {
		user, pass, ok := r.BasicAuth()
		if !ok || pass != "test-password" {
			return apiauth.Principal{}, false, nil
		}
		role := apiauth.RoleUser
		if user == "admin" {
			role = apiauth.RoleAdmin
		}
		return apiauth.Principal{Role: role, Subject: user, Teams: []apiauth.PrincipalTeam{{Slug: "acme", Namespace: "mcp-team-acme"}}}, true, nil
	}}, cert
}

func TestNativeRegistryScopesAndPullOnlyCredentials(t *testing.T) {
	deps, _ := registryTestDeps(t)
	for _, tc := range []struct {
		name, scope, user, password string
		status                      int
	}{
		{"anonymous", "repository:acme/app:pull", "", "", 401},
		{"own repository", "repository:acme/app:pull,push", "alice", "test-password", 200},
		{"cross team", "repository:other/app:pull", "alice", "test-password", 403},
		{"platform repository", "repository:mcp-platform-api:pull", "alice", "test-password", 403},
		{"catalog", "registry:catalog:*", "alice", "test-password", 403},
		{"administrator catalog", "registry:catalog:*", "admin", "test-password", 200},
		{"wildcard", "repository:acme/app:*", "alice", "test-password", 403},
		{"delete", "repository:acme/app:delete", "admin", "test-password", 403},
		{"malformed", "repository:acme/../other:pull", "alice", "test-password", 400},
		{"node own pull", "repository:acme/app:pull", "mcp-pull-mcp-team-acme", "mcpp_test", 200},
		{"node gateway pull", "repository:mcp-gateway:pull", "mcp-pull-mcp-team-acme", "mcpp_test", 200},
		{"node push", "repository:acme/app:push", "mcp-pull-mcp-team-acme", "mcpp_test", 403},
		{"node cross team", "repository:other/app:pull", "mcp-pull-mcp-team-acme", "mcpp_test", 403},
		{"wrong node username", "repository:acme/app:pull", "wrong", "mcpp_test", 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/registry/token?service="+registryauth.Service+"&scope="+tc.scope, nil)
			if tc.user != "" {
				req.SetBasicAuth(tc.user, tc.password)
			}
			req.Header.Set("X-Forwarded-Uri", "/v2/acme/app/manifests/latest")
			rec := httptest.NewRecorder()
			HandleToken(rec, req, deps)
			if rec.Code != tc.status {
				t.Fatalf("status %d want %d", rec.Code, tc.status)
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("credential response is cacheable")
			}
			if rec.Code == 200 {
				var body struct {
					Token   string `json:"token"`
					Expires int    `json:"expires_in"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Token == "" || body.Expires != 300 {
					t.Fatal("invalid token response")
				}
			}
		})
	}
	req := httptest.NewRequest("GET", "/registry/token?service=attacker", nil)
	rec := httptest.NewRecorder()
	HandleToken(rec, req, deps)
	if rec.Code != 400 {
		t.Fatal("caller changed token audience")
	}
	req = httptest.NewRequest("GET", "/registry/token?service="+registryauth.Service+"&scope="+strings.Repeat("x", 8193), nil)
	rec = httptest.NewRecorder()
	HandleToken(rec, req, deps)
	if rec.Code != 400 {
		t.Fatal("unbounded scope accepted")
	}
}
