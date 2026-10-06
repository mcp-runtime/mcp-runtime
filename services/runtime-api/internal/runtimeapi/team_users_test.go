package runtimeapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mcp-runtime-api/internal/platformclient"
)

func TestTeamUserCreatePreservesIdentityErrors(t *testing.T) {
	for _, tc := range []struct {
		status        int
		code, message string
	}{
		{400, "invalid_request_body", "valid email required"},
		{400, "invalid_request_body", "password must be at least 8 characters"},
		{400, "invalid_request_body", "membership role must be owner or member"},
		{409, "conflict", "a user with this email already exists; add them as a member instead"},
		{404, "not_found", "team not found"},
		{500, "internal_error", "failed to create user"},
	} {
		t.Run(tc.message, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/internal/identity/teams/acme/users" {
					t.Errorf("path=%s", r.URL.Path)
				}
				var body teamUserCreateRequest
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body.Password != " password123 " {
					t.Error("password whitespace was changed")
				}
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": tc.code, "message": tc.message})
			}))
			defer upstream.Close()
			server := &RuntimeServer{identity: &platformclient.Client{BaseURL: upstream.URL, Token: "test-token", HTTP: upstream.Client()}}
			audit := &fakeAuditWriter{}
			server.SetAuditWriter(audit)
			response := httptest.NewRecorder()
			request := httptest.NewRequest("POST", "/api/runtime/teams/acme/users", strings.NewReader(`{"email":"member@example.com","password":" password123 ","role":"member"}`))
			server.handleRuntimeTeamUserCreate(response, request, principal{Role: roleAdmin}, "acme")
			var envelope map[string]string
			_ = json.Unmarshal(response.Body.Bytes(), &envelope)
			if response.Code != tc.status || envelope["error"] != tc.code || envelope["message"] != tc.message {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if len(audit.events) != 1 || !strings.Contains(audit.events[0].Message, "code="+tc.code) || strings.Contains(audit.events[0].Message, "member@example.com") || strings.Contains(audit.events[0].Message, "password123") {
				t.Fatalf("unsafe or missing audit diagnostic: %+v", audit.events)
			}

		})
	}
}

func TestTeamUserCreateDeniesNonOwnerBeforeCallingIdentity(t *testing.T) {
	response := httptest.NewRecorder()
	server := &RuntimeServer{}
	server.handleRuntimeTeamUserCreate(response, httptest.NewRequest("POST", "/", strings.NewReader(`{}`)), principal{Role: roleUser}, "acme")
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d", response.Code)
	}
}
