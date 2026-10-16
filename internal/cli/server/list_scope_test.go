package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
	"mcp-runtime/internal/cli/core"
	"mcp-runtime/pkg/authfile"
)

func TestListServersTeamFilter(t *testing.T) {
	for _, tc := range []struct {
		name       string
		teamStatus int
		teamBody   string
		listStatus int
		wantErr    string
		wantList   bool
	}{
		{"authorized team", http.StatusOK, `{"team":{"slug":"acme","namespace":"mcp-team-acme"}}`, http.StatusOK, "", true},
		{"team lookup forbidden", http.StatusForbidden, `{"error":"forbidden"}`, http.StatusOK, "403", false},
		{"namespace access forbidden", http.StatusOK, `{"team":{"slug":"acme","namespace":"mcp-team-acme"}}`, http.StatusForbidden, "403", true},
		{"missing namespace fails closed", http.StatusOK, `{"team":{"slug":"acme"}}`, http.StatusOK, "has no namespace", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			teamCalls, listCalls := 0, 0
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("content-type", "application/json")
				switch r.URL.Path {
				case "/api/v1/runtime/teams/acme":
					teamCalls++
					w.WriteHeader(tc.teamStatus)
					_, _ = w.Write([]byte(tc.teamBody))
				case "/api/v1/runtime/servers":
					listCalls++
					if got := r.URL.Query().Get("namespace"); got != "mcp-team-acme" {
						t.Errorf("namespace = %q", got)
					}
					w.WriteHeader(tc.listStatus)
					_, _ = w.Write([]byte(`{"servers":[{"name":"team-server","namespace":"mcp-team-acme"}]}`))
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer api.Close()
			t.Setenv(authfile.EnvAPIToken, "token-1")
			t.Setenv(authfile.EnvAPIURL, api.URL)
			t.Setenv("MCP_RUNTIME_CONFIG_DIR", t.TempDir())
			mock := &core.MockExecutor{}
			mgr := NewServerManager(core.NewTestKubectlClient(mock), zap.NewNop())
			err := mgr.ListServers("", " acme ")
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
			if teamCalls != 1 || (listCalls == 1) != tc.wantList {
				t.Fatalf("team calls=%d list calls=%d", teamCalls, listCalls)
			}
			if len(mock.Commands) != 0 {
				t.Fatal("platform list fell back to kubectl")
			}
		})
	}
}

func TestListServersNamespaceFilter(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/runtime/servers" || r.URL.Query().Get("namespace") != "mcp-servers-org" {
			t.Errorf("unexpected request %s", r.URL)
		}
		_, _ = w.Write([]byte(`{"servers":[]}`))
	}))
	defer api.Close()
	t.Setenv(authfile.EnvAPIToken, "token-1")
	t.Setenv(authfile.EnvAPIURL, api.URL)
	t.Setenv("MCP_RUNTIME_CONFIG_DIR", t.TempDir())
	mock := &core.MockExecutor{}
	mgr := NewServerManager(core.NewTestKubectlClient(mock), zap.NewNop())
	if err := mgr.ListServers("mcp-servers-org", ""); err != nil {
		t.Fatal(err)
	}
	if err := mgr.ListServers("mcp-team-acme", "acme"); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("expected mutually exclusive selectors, got %v", err)
	}
	if len(mock.Commands) != 0 {
		t.Fatal("platform list fell back to kubectl")
	}
}
