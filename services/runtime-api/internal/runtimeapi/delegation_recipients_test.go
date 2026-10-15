package runtimeapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mcp-runtime-api/internal/platformclient"
)

func TestDelegationRecipientsRequiresResourceManagementBeforeDirectory(t *testing.T) {
	reads := 0
	directory := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reads++; http.Error(w, "unexpected directory read", 500) }))
	defer directory.Close()
	s := &RuntimeServer{identity: &platformclient.Client{BaseURL: directory.URL, Token: "service", HTTP: directory.Client()}}
	for _, p := range []principal{
		{Role: roleUser, Subject: "member", Namespace: "mcp-team-a", Teams: []principalTeam{{ID: "team-a", Slug: "a", Namespace: "mcp-team-a", Role: teamRoleMember}}},
		{Role: roleUser, Subject: "owner-b", Namespace: "mcp-team-b", Teams: []principalTeam{{ID: "team-b", Slug: "b", Namespace: "mcp-team-b", Role: teamRoleOwner}}},
	} {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/delegation-recipients?namespace=mcp-team-a&team=known-b", nil)
		r = r.WithContext(withPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		s.HandleDelegationRecipients(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	}
	if reads != 0 {
		t.Fatalf("unauthorized recipient directory reads=%d", reads)
	}
}

func TestDelegationRecipientsExactTeamMinimalFieldsAndPagination(t *testing.T) {
	reads := []string{}
	directory := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads = append(reads, r.URL.Path)
		switch r.URL.Path {
		case "/internal/identity/teams/known-b":
			_, _ = w.Write([]byte(`{"id":"team-b","slug":"known-b","name":"Recipient B"}`))
		case "/internal/identity/teams/known-b/agents":
			if r.URL.Query().Get("status") != "active" || r.URL.Query().Get("limit") != "1" {
				t.Errorf("unbounded agent request: %s", r.URL)
			}
			_, _ = w.Write([]byte(`{"agents":[{"id":"agt_active","name":"Active","status":"active","team_id":"team-b","team_slug":"known-b","created_by_user_id":"private"},{"id":"agt_inactive","status":"inactive","team_id":"team-b"}],"next_cursor":"next-agent"}`))
		case "/internal/identity/teams/known-b/members":
			_, _ = w.Write([]byte(`{"members":[{"user_id":"user-z","team_id":"team-b","email":"private@example.invalid"},{"user_id":"user-a","team_id":"team-b","role":"owner"}]}`))
		default:
			t.Errorf("global or unexpected directory read: %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer directory.Close()
	s := &RuntimeServer{identity: &platformclient.Client{BaseURL: directory.URL, Token: "service", HTTP: directory.Client()}}
	p := principal{Role: roleUser, Subject: "owner-a", Namespace: "mcp-team-a", Teams: []principalTeam{{ID: "team-a", Slug: "a", Namespace: "mcp-team-a", Role: teamRoleOwner}}}
	get := func(query string) delegationRecipients {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/delegation-recipients?namespace=mcp-team-a&team=known-b&limit=1"+query, nil)
		r = r.WithContext(withPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		s.HandleDelegationRecipients(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "email") || strings.Contains(w.Body.String(), "role") || strings.Contains(w.Body.String(), "agt_inactive") {
			t.Fatalf("recipient metadata leaked: %s", w.Body.String())
		}
		var result delegationRecipients
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := get("")
	if first.Team.ID != "team-b" || len(first.Agents) != 1 || first.Agents[0].ID != "agt_active" || first.Members[0].UserID != "user-a" || first.MemberNextCursor == "" || first.AgentNextCursor != "next-agent" {
		t.Fatalf("first=%+v", first)
	}
	second := get("&member_cursor=" + first.MemberNextCursor)
	if len(second.Members) != 1 || second.Members[0].UserID != "user-z" || second.MemberNextCursor != "" {
		t.Fatalf("second=%+v", second)
	}
	if len(reads) != 6 {
		t.Fatalf("directory reads=%v", reads)
	}
}

func TestDelegationRecipientsValidationAndDirectoryFailure(t *testing.T) {
	for _, tc := range []struct {
		name, query string
		want        int
	}{
		{"exact team required", "team=*", http.StatusBadRequest}, {"limit bounded", "team=b&limit=101", http.StatusBadRequest}, {"member cursor validated", "team=b&member_cursor=!", http.StatusBadRequest}, {"directory outage", "team=b", http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/delegation-recipients?namespace=mcp-team-a&"+tc.query, nil)
			r = r.WithContext(withPrincipal(r.Context(), principal{Role: roleAdmin, Subject: "admin"}))
			w := httptest.NewRecorder()
			(&RuntimeServer{}).HandleDelegationRecipients(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}
