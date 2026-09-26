package runtimeapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"mcp-runtime-api/internal/platformclient"
	sentinelaccess "mcp-runtime/pkg/access"
)

type agentIdentityStub struct {
	identityStore
	created bool
	agent   platformclient.Agent
}

type missingAgentIdentityStub struct{ agentIdentityStub }

func (missingAgentIdentityStub) GetAgent(context.Context, string) (platformclient.Agent, bool, error) {
	return platformclient.Agent{}, false, nil
}

func (agentIdentityStub) Configured() bool { return true }
func (s *agentIdentityStub) CreateAgent(_ context.Context, slug, name, createdBy string) (platformclient.Agent, error) {
	s.created = true
	return platformclient.Agent{ID: "agt_01arz3ndektsv4rrffq69g5fav", TeamID: "team-id", TeamSlug: slug, Name: name, Status: "active", CreatedBy: createdBy}, nil
}
func (agentIdentityStub) ListAgents(_ context.Context, slug, status, query, cursor string, limit int) (platformclient.AgentPage, error) {
	return platformclient.AgentPage{Agents: []platformclient.Agent{{ID: "agt_01arz3ndektsv4rrffq69g5fav", TeamSlug: slug, Status: "active"}}}, nil
}
func (s *agentIdentityStub) GetAgent(_ context.Context, id string) (platformclient.Agent, bool, error) {
	if s.agent.ID == "" {
		return platformclient.Agent{ID: id, TeamID: "team-id", TeamSlug: "core", Status: "active"}, true, nil
	}
	return s.agent, true, nil
}

func TestRequireActiveAgent(t *testing.T) {
	store := &agentIdentityStub{}
	cases := []struct {
		name, id, team string
		store          identityStore
		want           error
	}{
		{name: "empty subject", store: store},
		{name: "active matching", id: "agt_01arz3ndektsv4rrffq69g5fav", team: "team-id", store: store},
		{name: "wrong team", id: "agt_01arz3ndektsv4rrffq69g5fav", team: "other", store: store, want: errAgentNotActive},
		{name: "directory unavailable", id: "agt_01arz3ndektsv4rrffq69g5fav", team: "team-id", want: errAgentDirectoryUnavailable},
		{name: "missing team", id: "agt_01arz3ndektsv4rrffq69g5fav", store: store, want: errAgentNotActive},
		{name: "malformed ID", id: "legacy-agent", team: "team-id", store: store, want: errAgentNotActive},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := requireActiveAgent(t.Context(), tc.store, tc.id, tc.team)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
	store.agent = platformclient.Agent{ID: "agt_01arz3ndektsv4rrffq69g5fav", TeamID: "team-id", Status: "inactive"}
	if err := requireActiveAgent(t.Context(), store, store.agent.ID, store.agent.TeamID); !errors.Is(err, errAgentNotActive) {
		t.Fatalf("inactive agent error = %v, want %v", err, errAgentNotActive)
	}
}

func TestRequireActiveAgentRejectsUnknownAndUnavailableDirectory(t *testing.T) {
	unknown := &missingAgentIdentityStub{}
	validID := "agt_01arz3ndektsv4rrffq69g5fav"
	if err := requireActiveAgent(t.Context(), unknown, validID, "team-id"); !errors.Is(err, errAgentNotActive) {
		t.Fatalf("unknown agent error = %v, want unknown-agent error", err)
	}
	if err := requireActiveAgent(t.Context(), nil, validID, "team-id"); !errors.Is(err, errAgentDirectoryUnavailable) {
		t.Fatalf("unavailable directory error = %v, want directory-unavailable error", err)
	}

}

func TestRequireActiveAgentRejectsMalformedGeneratedIDs(t *testing.T) {
	store := &agentIdentityStub{}
	for _, id := range []string{
		"legacy-agent",
		"agt_81arz3ndektsv4rrffq69g5fav",
		"agt_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		"agt_01arz3ndektsv4rrffq69g5fa",
	} {
		t.Run(id, func(t *testing.T) {
			if err := requireActiveAgent(t.Context(), store, id, "team-id"); !errors.Is(err, errAgentNotActive) {
				t.Fatalf("malformed agent ID error = %v, want %v", err, errAgentNotActive)
			}
		})
	}
}

type auditCollector struct{ events []auditEvent }

func (a *auditCollector) WriteAudit(_ context.Context, event auditEvent) {
	a.events = append(a.events, event)
}

func TestDeactivateAgentRevokesOnlyMatchingSessionsAndAuditsEach(t *testing.T) {
	newSession := func(name, namespace, agentID, teamID string, revoked bool) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "mcpruntime.org/v1alpha1", "kind": "MCPAgentSession",
			"metadata": map[string]any{"name": name, "namespace": namespace},
			"spec":     map[string]any{"subject": map[string]any{"agentID": agentID, "teamID": teamID}, "revoked": revoked},
		}}
	}
	objects := []runtime.Object{
		newSession("match", "ns-a", "agent-a", "team-a", false),
		newSession("other-team", "ns-b", "agent-a", "team-b", false),
		newSession("other-agent", "ns-a", "agent-b", "team-a", false),
		newSession("already-revoked", "ns-a", "agent-a", "team-a", true),
	}
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), objects...)
	audit := &auditCollector{}
	server := &RuntimeServer{accessMgr: sentinelaccess.NewManager(dyn, nil), audit: audit}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/agents/agent-a/deactivate", nil)
	if err := server.revokeAgentSessions(request.Context(), request, principal{Role: roleAdmin, Subject: "admin-id"}, platformclient.Agent{ID: "agent-a", TeamID: "team-a"}); err != nil {
		t.Fatal(err)
	}
	match, err := server.accessMgr.GetSession(request.Context(), "match", "ns-a")
	if err != nil || !match.Spec.Revoked {
		t.Fatalf("matching session revoked=%v err=%v", match != nil && match.Spec.Revoked, err)
	}
	otherTeam, _ := server.accessMgr.GetSession(request.Context(), "other-team", "ns-b")
	otherAgent, _ := server.accessMgr.GetSession(request.Context(), "other-agent", "ns-a")
	if otherTeam.Spec.Revoked || otherAgent.Spec.Revoked {
		t.Fatal("deactivation revoked a session outside the agent and team")
	}
	if len(audit.events) != 1 || audit.events[0].Action != "agent.session_revoked" || audit.events[0].AgentID != "agent-a" || audit.events[0].Namespace != "ns-a" {
		t.Fatalf("audit events = %#v, want one matching session revocation", audit.events)
	}
}

func TestAgentDirectoryTeamAuthorization(t *testing.T) {
	store := &agentIdentityStub{}
	server := &RuntimeServer{identity: store}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/teams/core/agents", strings.NewReader(`{"name":"release planner"}`))
	recorder := httptest.NewRecorder()
	server.HandleRuntimeTeamAgents(recorder, request, principal{Role: roleUser, Subject: "member-id", Teams: []principalTeam{{Slug: "core", Role: teamRoleMember}}}, "core")
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("member create status = %d, want 403", recorder.Code)
	}
	if store.created {
		t.Fatal("unauthorized request reached the identity store")
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/runtime/teams/core/agents", strings.NewReader(`{"name":"release planner"}`))
	recorder = httptest.NewRecorder()
	server.HandleRuntimeTeamAgents(recorder, request, principal{Role: roleUser, Subject: "owner-id", Teams: []principalTeam{{Slug: "core", Role: teamRoleOwner}}}, "core")
	if recorder.Code != http.StatusCreated {
		t.Fatalf("owner create status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if !store.created {
		t.Fatal("authorized create did not reach identity store")
	}
}

func TestAgentDirectoryTeamMemberCanList(t *testing.T) {
	server := &RuntimeServer{identity: &agentIdentityStub{}}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/teams/core/agents?status=active&limit=10", nil)
	recorder := httptest.NewRecorder()
	server.HandleRuntimeTeamAgents(recorder, request, principal{Role: roleUser, Subject: "member-id", Teams: []principalTeam{{Slug: "core", Role: teamRoleMember}}}, "core")
	if recorder.Code != http.StatusOK {
		t.Fatalf("member list status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
}
