package platforminternal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"mcp-platform-api/internal/platformstore"
)

type failingTeamUserStore struct {
	fakeStore
	err error
}

func (s *failingTeamUserStore) CreateTeamUser(context.Context, string, string, string, string) (platformstore.User, platformstore.TeamMembership, error) {
	return platformstore.User{}, platformstore.TeamMembership{}, s.err
}

func TestTeamUserCreateSafeErrors(t *testing.T) {
	for _, tc := range []struct {
		err     error
		status  int
		message string
	}{
		{platformstore.UserInputError("valid email required"), 400, "valid email required"},
		{platformstore.ErrEmailAlreadyRegistered, 409, platformstore.ErrEmailAlreadyRegistered.Error()},
		{sql.ErrNoRows, 404, "team not found"},
		{errors.New("SQL password and email details"), 500, "failed to create user"},
	} {
		response := httptest.NewRecorder()
		handler := Handler{Store: &failingTeamUserStore{err: tc.err}}
		handler.teamUserCreate(response, httptest.NewRequest("POST", "/", strings.NewReader(`{"email":"user@example.com","password":"password123"}`)), "acme")
		var envelope map[string]string
		_ = json.Unmarshal(response.Body.Bytes(), &envelope)
		if response.Code != tc.status || envelope["message"] != tc.message {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	}
}
