package platformclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSetAgentStatusUsesIdentityActionRoute(t *testing.T) {
	tests := []struct {
		name       string
		status     string
		action     string
		wantStatus string
	}{
		{name: "deactivate", status: "inactive", action: "deactivate", wantStatus: "inactive"},
		{name: "reactivate", status: "active", action: "reactivate", wantStatus: "active"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Helper()
				if r.Method != http.MethodPost {
					t.Errorf("method = %s, want POST", r.Method)
				}
				if r.URL.Path != "/internal/identity/agents/agent-123/"+tt.action {
					t.Errorf("path = %s, want action route %q", r.URL.Path, tt.action)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer internal-token" {
					t.Errorf("authorization = %q, want Bearer internal-token", got)
				}
				var body struct {
					ActorID string `json:"actor_id"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode request body: %v", err)
				} else if body.ActorID != "actor-456" {
					t.Errorf("actor_id = %q, want actor-456", body.ActorID)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"agent-123","status":"` + tt.wantStatus + `"}`))
			}))
			defer server.Close()

			client := &Client{BaseURL: server.URL, Token: "internal-token"}
			agent, err := client.SetAgentStatus(context.Background(), "agent-123", tt.status, "actor-456")
			if err != nil {
				t.Fatalf("SetAgentStatus() error = %v", err)
			}
			if agent.ID != "agent-123" || agent.Status != tt.wantStatus {
				t.Fatalf("SetAgentStatus() = %#v, want id agent-123 status %s", agent, tt.wantStatus)
			}
		})
	}
}

func TestSetAgentStatusRejectsUnknownStatus(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	defer server.Close()

	client := &Client{BaseURL: server.URL}
	if _, err := client.SetAgentStatus(context.Background(), "agent-123", "inactive/extra", "actor-456"); err == nil {
		t.Fatal("SetAgentStatus() error = nil, want unsupported status error")
	}
	if called {
		t.Fatal("SetAgentStatus() made an HTTP request for an unsupported status")
	}
}
