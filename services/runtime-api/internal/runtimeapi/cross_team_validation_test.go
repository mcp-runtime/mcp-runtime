package runtimeapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"mcp-runtime-api/internal/platformclient"
	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	mcpaccess "mcp-runtime/pkg/access"
)

func TestCrossTeamGrantEndpointMembershipExpiryAndDirectoryStatus(t *testing.T) {
	for _, tc := range []struct {
		name, outage                                   string
		wrongHuman, wrongAgent, missingExpiry, overTTL bool
		want                                           int
	}{
		{name: "owner grants foreign subject", want: 200},
		{name: "foreign human is not a member", wrongHuman: true, want: 422},
		{name: "foreign agent belongs to another team", wrongAgent: true, want: 422},
		{name: "expiry required", missingExpiry: true, want: 422},
		{name: "expiry over configured cap", overTTL: true, want: 422},
		{name: "team directory outage", outage: "teams", want: 503},
		{name: "membership directory outage", outage: "members", want: 503},
		{name: "agent directory outage", outage: "agents", want: 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MCP_CROSS_TEAM_GRANT_MAX_TTL", "1h")
			directory := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/internal/identity/teams":
					if tc.outage == "teams" {
						http.Error(w, "unavailable", 503)
						return
					}
					_, _ = w.Write([]byte(`{"teams":[{"id":"team-b","slug":"b"}]}`))
				case "/internal/identity/teams/b/members":
					if tc.outage == "members" {
						http.Error(w, "unavailable", 503)
						return
					}
					user := "user-b"
					if tc.wrongHuman {
						user = "other"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"members": []map[string]string{{"user_id": user, "team_id": "team-b"}}})
				case "/internal/identity/agents/agt_01arz3ndektsv4rrffq69g5fav":
					if tc.outage == "agents" {
						http.Error(w, "unavailable", 503)
						return
					}
					team := "team-b"
					if tc.wrongAgent {
						team = "team-c"
					}
					_ = json.NewEncoder(w).Encode(map[string]string{"id": "agt_01arz3ndektsv4rrffq69g5fav", "team_id": team, "status": "active"})
				default:
					http.NotFound(w, r)
				}
			}))
			defer directory.Close()
			scheme := runtime.NewScheme()
			_ = mcpv1alpha1.AddToScheme(scheme)
			manager := mcpaccess.NewManager(dynamicfake.NewSimpleDynamicClient(scheme, &mcpv1alpha1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "server", Namespace: "team-a"}, Spec: mcpv1alpha1.MCPServerSpec{TeamID: "team-a"}}), nil)
			server := &RuntimeServer{accessMgr: manager, identity: &platformclient.Client{BaseURL: directory.URL, Token: "service", HTTP: directory.Client()}}
			body := map[string]any{"name": "delegation", "namespace": "team-a", "serverRef": map[string]string{"name": "server"}, "subject": map[string]string{"humanID": "user-b", "agentID": "agt_01arz3ndektsv4rrffq69g5fav", "teamID": "team-b"}, "maxTrust": "low", "allowedSideEffects": []string{"read"}}
			if !tc.missingExpiry {
				ttl := 30 * time.Minute
				if tc.overTTL {
					ttl = 2 * time.Hour
				}
				body["expiresAt"] = time.Now().Add(ttl).UTC().Format(time.RFC3339)
			}
			raw, _ := json.Marshal(body)
			r := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/grants", bytes.NewReader(raw))
			r = r.WithContext(withPrincipal(r.Context(), principal{Role: roleUser, Subject: "owner-a", Namespace: "team-a", Teams: []principalTeam{{ID: "team-a", Slug: "a", Namespace: "team-a", Role: teamRoleOwner}}}))
			w := httptest.NewRecorder()
			server.Access().handleRuntimeGrantApply(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			grants, err := manager.ListGrants(t.Context(), "team-a")
			if err != nil {
				t.Fatal(err)
			}
			if tc.want != 200 && len(grants.Items) != 0 {
				t.Fatal("invalid or unavailable directory created a grant")
			}
		})
	}
}
