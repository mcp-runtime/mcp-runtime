package runtimeapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	mcpaccess "mcp-runtime/pkg/access"
	"mcp-runtime/pkg/internalapi"
	"mcp-runtime/pkg/policy"
	"mcp-runtime/pkg/sessionstate"
)

type overviewIdentityStub struct {
	identityStore
	counts internalapi.OverviewCounts
}

func (s overviewIdentityStub) OverviewCounts(context.Context) (internalapi.OverviewCounts, error) {
	return s.counts, nil
}

func TestWorkspaceMembershipDoesNotExpandAdminAuthority(t *testing.T) {
	s := &RuntimeServer{}
	for _, role := range []string{roleAdmin, roleUser} {
		r := httptest.NewRequest(http.MethodGet, "/runtime/workspace/teams?user=other&namespace=other", nil)
		r = r.WithContext(withPrincipal(r.Context(), principal{Role: role, Subject: "me", Teams: []principalTeam{{ID: "team", Slug: "actual", Role: teamRoleMember}}}))
		w := httptest.NewRecorder()
		s.HandleWorkspaceTeams(w, r)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"slug":"actual"`) || strings.Contains(w.Body.String(), "other") {
			t.Fatalf("status=%d body=%s", w.Code, w.Body)
		}
	}
}

func TestOverviewEnforcesAdminForDirectRequestsAndUsesTotals(t *testing.T) {
	s := &RuntimeServer{identity: overviewIdentityStub{counts: internalapi.OverviewCounts{Teams: 321, Users: 1234, Agents: 999, ActiveSessions: 543}}}
	for _, handle := range []http.HandlerFunc{s.HandleOverviewDirectory, s.HandleOverviewServers} {
		for _, role := range []string{"", roleUser} {
			r := httptest.NewRequest(http.MethodGet, "/admin/overview/directory", nil)
			want := http.StatusUnauthorized
			if role != "" {
				r = r.WithContext(withPrincipal(r.Context(), principal{Role: role}))
				want = http.StatusForbidden
			}
			w := httptest.NewRecorder()
			handle(w, r)
			if w.Code != want {
				t.Fatalf("status=%d want=%d", w.Code, want)
			}
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/admin/overview/directory", nil)
	r = r.WithContext(withPrincipal(r.Context(), principal{Role: roleAdmin}))
	w := httptest.NewRecorder()
	s.HandleOverviewDirectory(w, r)
	var counts internalapi.OverviewCounts
	if err := json.Unmarshal(w.Body.Bytes(), &counts); err != nil {
		t.Fatal(err)
	}
	if counts.Users != 1234 || counts.ActiveSessions != 543 {
		t.Fatalf("counts=%+v", counts)
	}
}

func TestWorkspaceSeparatesVisibilityManagementAndAttribution(t *testing.T) {
	owned := &mcpv1alpha1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "owned", Namespace: "mcp-team-acme", Labels: map[string]string{platformUserIDLabel: "member"}}}
	other := &mcpv1alpha1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "visible", Namespace: "mcp-team-acme", Labels: map[string]string{platformUserIDLabel: "another"}}}
	foreign := &mcpv1alpha1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "secret", Namespace: "mcp-team-other"}}
	s := newRuntimeServerWithMCPServers(t, owned, other, foreign)
	for _, role := range []string{teamRoleMember, teamRoleOwner} {
		p := principal{Role: roleUser, Subject: "member", Namespace: "mcp-team-acme", AllowedNamespaces: []string{"mcp-team-acme"}, Teams: []principalTeam{{ID: "team", Slug: "acme", Namespace: "mcp-team-acme", Role: role}}}
		r := httptest.NewRequest(http.MethodGet, "/runtime/workspace/servers?namespace=mcp-team-other", nil)
		r = r.WithContext(withPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		s.HandleWorkspaceServers(w, r)
		var body struct {
			Servers     []workspaceServer `json:"servers"`
			Attribution string            `json:"deployment_attribution"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusOK || len(body.Servers) != 2 || body.Attribution != "unavailable" {
			t.Fatalf("status=%d body=%s", w.Code, w.Body)
		}
		for _, server := range body.Servers {
			want := server.Name == "owned" || role == teamRoleOwner
			if server.CanManage != want {
				t.Fatalf("role=%s server=%+v", role, server)
			}
		}
	}
}

func TestWorkspaceRequiresAuthenticatedReadOnlyRequests(t *testing.T) {
	s := &RuntimeServer{}
	for _, handle := range []http.HandlerFunc{s.HandleWorkspaceTeams, s.HandleWorkspaceServers, s.HandleWorkspaceAccess} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(method, "/runtime/workspace/teams", nil)
			handle(w, r)
			want := http.StatusUnauthorized
			if method == http.MethodPost {
				want = http.StatusMethodNotAllowed
			}
			if w.Code != want {
				t.Fatalf("%s status=%d", method, w.Code)
			}
		}
	}
}

type workspaceSessionClient struct {
	fakeSessionClient
	rows []sessionstate.Session
}

func (c *workspaceSessionClient) ListSessions(context.Context, string) ([]sessionstate.Session, error) {
	return c.rows, nil
}

func TestWorkspacePersonalAccessNeverExpandsForAdmin(t *testing.T) {
	grants := []*mcpaccess.MCPAccessGrant{
		{ObjectMeta: metav1.ObjectMeta{Name: "mine", Namespace: "team"}, Spec: mcpaccess.MCPAccessGrantSpec{Subject: mcpaccess.SubjectRef{HumanID: "me"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "team", Namespace: "team"}, Spec: mcpaccess.MCPAccessGrantSpec{Subject: mcpaccess.SubjectRef{TeamID: "actual"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "team"}, Spec: mcpaccess.MCPAccessGrantSpec{Subject: mcpaccess.SubjectRef{HumanID: "another"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "team"}, Spec: mcpaccess.MCPAccessGrantSpec{Subject: mcpaccess.SubjectRef{AgentID: "agent", TeamID: "actual"}}},
	}
	objects := make([]runtime.Object, 0, len(grants))
	for _, grant := range grants {
		var obj mcpv1alpha1.MCPAccessGrant
		raw, err := json.Marshal(grant)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &obj); err != nil {
			t.Fatal(err)
		}
		objects = append(objects, &obj)
	}
	manager := newTestAccessManagerWithObjects(t, objects...)
	client := &workspaceSessionClient{rows: []sessionstate.Session{
		{Namespace: "team", Binding: policy.Binding{Name: "mine", HumanID: "me"}},
		{Namespace: "team", Binding: policy.Binding{Name: "other", HumanID: "another"}},
	}}
	manager.WithSessionBackend(&platformSessionBackend{client: client, access: manager})
	s := &RuntimeServer{accessMgr: manager}
	for _, role := range []string{roleAdmin, roleUser} {
		p := principal{Role: role, Subject: "me", Namespace: "team", AllowedNamespaces: []string{"team"}, Teams: []principalTeam{{ID: "actual", Namespace: "team"}}}
		r := httptest.NewRequest(http.MethodGet, "/runtime/workspace/access?user=another", nil)
		r = r.WithContext(withPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		s.HandleWorkspaceAccess(w, r)
		var body struct {
			Grants   []mcpaccess.GrantSummary   `json:"grants"`
			Sessions []mcpaccess.SessionSummary `json:"sessions"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusOK || len(body.Grants) != 2 || len(body.Sessions) != 1 || body.Sessions[0].Name != "mine" {
			t.Fatalf("role=%s status=%d body=%s", role, w.Code, w.Body)
		}
	}
}
