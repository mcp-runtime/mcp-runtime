package platforminternal

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"mcp-platform-api/internal/platformstore"
	"mcp-runtime/pkg/platformauth"
)

type fakeStore struct {
	audit platformstore.AuditEvent
}

func (f *fakeStore) AuthenticateUserAPIKey(context.Context, string) (platformauth.Principal, bool, error) {
	return platformauth.Principal{
		Subject:           "user-1",
		Email:             "user@example.com",
		Teams:             []platformauth.PrincipalTeam{{ID: "team-1", Slug: "core", Namespace: "mcp-team-core", Role: "owner"}},
		AllowedNamespaces: []string{"mcp-team-core", "mcp-servers"},
		AuthType:          "user_api_key",
		APIKeyID:          "key-1",
	}, true, nil
}

func (f *fakeStore) PrincipalForUserID(context.Context, string) (platformauth.Principal, error) {
	return platformauth.Principal{
		Subject: "user-1",
		Email:   "user@example.com",
		Role:    platformauth.RoleUser,
	}, nil
}

func (f *fakeStore) ResolveUserIDs(context.Context, []string) (map[string]string, error) {
	return map[string]string{"user-1": "user@example.com"}, nil
}

func (f *fakeStore) ResolveTeamIDs(context.Context, []string) (map[string]string, error) {
	return map[string]string{"team-1": "core"}, nil
}

func (f *fakeStore) WriteAudit(_ context.Context, event platformstore.AuditEvent) {
	f.audit = event
}

func (f *fakeStore) CreateTeam(_ context.Context, slug, name, _ string) (platformstore.Team, error) {
	return platformstore.Team{ID: "team-1", Slug: slug, Name: name, Namespace: "mcp-team-" + slug}, nil
}

func (f *fakeStore) ListTeams(context.Context) ([]platformstore.Team, error) {
	return []platformstore.Team{{ID: "team-1", Slug: "core", Name: "Core", Namespace: "mcp-team-core"}}, nil
}

func (f *fakeStore) GetTeamBySlug(_ context.Context, slug string) (platformstore.Team, bool, error) {
	return platformstore.Team{ID: "team-1", Slug: slug, Name: "Core", Namespace: "mcp-team-core"}, true, nil
}

func (f *fakeStore) CreateAgent(_ context.Context, slug, name, createdBy string) (platformstore.Agent, error) {
	return platformstore.Agent{ID: "agt_01arz3ndektsv4rrffq69g5fav", TeamID: "team-1", TeamSlug: slug, Name: name, Status: "active", CreatedBy: createdBy}, nil
}

func (f *fakeStore) ListAgents(context.Context, platformstore.AgentListFilter) (platformstore.AgentPage, error) {
	return platformstore.AgentPage{Agents: []platformstore.Agent{}}, nil
}

func (f *fakeStore) GetAgent(_ context.Context, id string) (platformstore.Agent, bool, error) {
	return platformstore.Agent{ID: id, TeamID: "team-1", TeamSlug: "core", Status: "active"}, true, nil
}

func (f *fakeStore) RenameAgent(_ context.Context, id, name string) (platformstore.Agent, error) {
	return platformstore.Agent{ID: id, TeamID: "team-1", TeamSlug: "core", Name: name, Status: "active"}, nil
}

func (f *fakeStore) SetAgentStatus(_ context.Context, id, status, actorID string) (platformstore.Agent, error) {
	return platformstore.Agent{ID: id, TeamID: "team-1", TeamSlug: "core", Status: status, DeactivatedBy: actorID}, nil
}

func TestTeamAgentDirectoryInternalRoutesRequireInternalToken(t *testing.T) {
	mux := newTestServer(&fakeStore{})
	for _, path := range []string{"/internal/identity/teams/core/agents", "/internal/identity/agents/agt_01arz3ndektsv4rrffq69g5fav"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s status = %d, want 401", path, rec.Code)
		}
	}
}

func (f *fakeStore) DeleteTeamBySlug(context.Context, string) error {
	return nil
}

func (f *fakeStore) ListNamespaces(context.Context) ([]map[string]any, error) {
	return []map[string]any{{"namespace": "mcp-team-core"}}, nil
}

func (f *fakeStore) GetNamespace(_ context.Context, namespace string) (map[string]any, bool, error) {
	return map[string]any{"namespace": namespace}, true, nil
}

func (f *fakeStore) ListTeamMemberships(context.Context, string) ([]platformstore.TeamMembership, error) {
	return nil, nil
}

func (f *fakeStore) UpsertTeamMembership(context.Context, string, string, string) (platformstore.TeamMembership, error) {
	return platformstore.TeamMembership{}, nil
}

func (f *fakeStore) DeleteTeamMembership(context.Context, string, string) error {
	return nil
}

func (f *fakeStore) CreatePasswordUser(context.Context, string, string, string) (platformstore.User, error) {
	return platformstore.User{ID: "user-1", Email: "user@example.com", Role: platformstore.RoleUser}, nil
}

func (f *fakeStore) OperationsSnapshot(context.Context, platformstore.OperationsFilter) (platformstore.OperationsSnapshot, error) {
	return platformstore.OperationsSnapshot{}, nil
}

func newTestServer(store PlatformStore) http.Handler {
	mux := http.NewServeMux()
	Handler{Store: store, Token: "internal-token"}.Register(mux)
	return mux
}

func TestResolveAuthUsesPlatformAuthenticatorForServiceKeys(t *testing.T) {
	mux := http.NewServeMux()
	handler := Handler{
		Store: &fakeStore{},
		Token: "internal-token",
		AuthenticateRequest: func(r *http.Request) (platformauth.Principal, bool, error) {
			if got := r.Header.Get("x-api-key"); got != "service-key" {
				t.Fatalf("x-api-key = %q, want service-key", got)
			}
			return platformauth.Principal{Role: platformauth.RoleAdmin, AuthType: "service_api_key"}, true, nil
		},
	}
	handler.Register(mux)
	req := httptest.NewRequest(http.MethodPost, "/internal/auth/resolve", bytes.NewBufferString(`{"api_key":"service-key"}`))
	req.Header.Set("Authorization", "Bearer internal-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"role":"admin"`)) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestInternalEndpointsRequireBearerToken(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/internal/auth/resolve", bytes.NewBufferString(`{"api_key":"key"}`))
	rec := httptest.NewRecorder()
	newTestServer(&fakeStore{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestResolveAuthReturnsEnrichedPrincipal(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/internal/auth/resolve", bytes.NewBufferString(`{"api_key":"key"}`))
	req.Header.Set("Authorization", "Bearer internal-token")
	rec := httptest.NewRecorder()
	newTestServer(&fakeStore{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{`"slug":"core"`, `"allowed_namespaces":["mcp-team-core","mcp-servers"]`, `"api_key_id":"key-1"`} {
		if !bytes.Contains(rec.Body.Bytes(), []byte(want)) {
			t.Fatalf("body %s missing %s", rec.Body.String(), want)
		}
	}
}

func TestResolveIDsAndAudit(t *testing.T) {
	store := &fakeStore{}
	handler := newTestServer(store)
	req := httptest.NewRequest(http.MethodPost, "/internal/identity/resolve-ids", bytes.NewBufferString(`{"user_ids":["user-1"],"team_ids":["team-1"]}`))
	req.Header.Set("Authorization", "Bearer internal-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"core"`)) {
		t.Fatalf("resolve status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/internal/audit", bytes.NewBufferString(`{"user_id":"user-1","action":"agent.created","resource":"agent","status":"success","agent_id":"agt_01arz3ndektsv4rrffq69g5fav","team_id":"team-1"}`))
	req.Header.Set("Authorization", "Bearer internal-token")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("audit status = %d", rec.Code)
	}
	if store.audit.UserID != "user-1" || store.audit.Action != "agent.created" || store.audit.AgentID != "agt_01arz3ndektsv4rrffq69g5fav" || store.audit.TeamID != "team-1" {
		t.Fatalf("audit = %#v", store.audit)
	}
}

func TestInternalAgentRoutesReturnDirectoryRecords(t *testing.T) {
	handler := newTestServer(&fakeStore{})
	req := httptest.NewRequest(http.MethodPost, "/internal/identity/teams/core/agents", bytes.NewBufferString(`{"name":"Release Planner","created_by":"user-1"}`))
	req.Header.Set("Authorization", "Bearer internal-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated || !bytes.Contains(rec.Body.Bytes(), []byte(`"id":"agt_01arz3ndektsv4rrffq69g5fav"`)) {
		t.Fatalf("create agent status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/internal/identity/teams/core/agents?status=active&limit=25", nil)
	req.Header.Set("Authorization", "Bearer internal-token")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"agents":[]`)) {
		t.Fatalf("list agents status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestTeamAndNamespaceRoutes(t *testing.T) {
	handler := newTestServer(&fakeStore{})
	tests := []struct {
		method string
		path   string
		body   string
		status int
	}{
		{http.MethodGet, "/internal/identity/teams", "", http.StatusOK},
		{http.MethodPost, "/internal/identity/teams", `{"slug":"core","name":"Core"}`, http.StatusCreated},
		{http.MethodGet, "/internal/identity/teams/core", "", http.StatusOK},
		{http.MethodDelete, "/internal/identity/teams/core", "", http.StatusNoContent},
		{http.MethodGet, "/internal/identity/namespaces", "", http.StatusOK},
		{http.MethodGet, "/internal/identity/namespaces/mcp-team-core", "", http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, bytes.NewBufferString(tt.body))
			req.Header.Set("Authorization", "Bearer internal-token")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tt.status {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func (f *fakeStore) CreateTeamUser(ctx context.Context, slug, email, password, role string) (platformstore.User, platformstore.TeamMembership, error) {
	user, err := f.CreatePasswordUser(ctx, email, password, "user")
	if err != nil {
		return platformstore.User{}, platformstore.TeamMembership{}, err
	}
	membership, err := f.UpsertTeamMembership(ctx, slug, user.ID, role)
	return user, membership, err
}
