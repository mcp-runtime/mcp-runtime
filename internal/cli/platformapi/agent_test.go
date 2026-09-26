package platformapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAgentAndGrantSessionEndpoints(t *testing.T) {
	const agentID = "agt_01arz3ndektsv4rrffq69g5fav"
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/runtime/teams/acme/agents":
			if r.URL.Query().Get("status") != "active" || r.URL.Query().Get("q") != "build" || r.URL.Query().Get("limit") != "25" {
				t.Errorf("agent list query = %q", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"agents":[{"id":"` + agentID + `","team_slug":"acme","name":"Build","status":"active"}],"next_cursor":"next"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/runtime/agents/"+agentID:
			_, _ = w.Write([]byte(`{"agent":{"id":"` + agentID + `","team_slug":"acme","name":"Build","status":"active"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/runtime/teams/acme/agents":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["name"] != "Build" {
				t.Errorf("create body = %#v", body)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"agent":{"id":"` + agentID + `","team_slug":"acme","name":"Build","status":"active"}}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/runtime/agents/"+agentID:
			_, _ = w.Write([]byte(`{"agent":{"id":"` + agentID + `","team_slug":"acme","name":"Release","status":"active"}}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/deactivate"):
			_, _ = w.Write([]byte(`{"agent":{"id":"` + agentID + `","team_slug":"acme","name":"Release","status":"inactive"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/runtime/grants/mcp-team-acme/build/revoke-sessions":
			_, _ = w.Write([]byte(`{"grant":"build","revokedSessions":2}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := &PlatformClient{baseURL: server.URL, token: "test-token", http: server.Client(), apiPrefix: "/api/v1"}

	page, err := client.ListAgents(context.Background(), "acme", "active", "build", "", 25)
	if err != nil || len(page.Agents) != 1 || page.Agents[0].ID != agentID || page.NextCursor != "next" {
		t.Fatalf("ListAgents() = %#v, %v", page, err)
	}
	agent, err := client.CreateAgent(context.Background(), "acme", "Build")
	if err != nil || agent.ID != agentID {
		t.Fatalf("CreateAgent() = %#v, %v", agent, err)
	}
	agent, err = client.GetAgent(context.Background(), agentID)
	if err != nil || agent.Name != "Build" {
		t.Fatalf("GetAgent() = %#v, %v", agent, err)
	}
	agent, err = client.RenameAgent(context.Background(), agentID, "Release")
	if err != nil || agent.Name != "Release" {
		t.Fatalf("RenameAgent() = %#v, %v", agent, err)
	}
	agent, err = client.SetAgentActive(context.Background(), agentID, false)
	if err != nil || agent.Status != "inactive" {
		t.Fatalf("SetAgentActive() = %#v, %v", agent, err)
	}
	revoked, err := client.RevokeGrantSessions(context.Background(), "mcp-team-acme", "build")
	if err != nil || revoked != 2 {
		t.Fatalf("RevokeGrantSessions() = %d, %v", revoked, err)
	}
	if len(calls) != 6 {
		t.Fatalf("got %d requests: %#v", len(calls), calls)
	}
}
