package identity

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mcp-platform-api/internal/platformstore"
	"mcp-runtime/pkg/platform"
	"mcp-runtime/pkg/platformauth"
)

type lifecycleStoreStub struct {
	err   error
	calls int
	event platformstore.AuditEvent
}

func (s *lifecycleStoreStub) DeactivateUser(_ context.Context, id, actor string, event platformstore.AuditEvent) (platform.UserDeactivationResult, error) {
	s.calls++
	s.event = event
	return platform.UserDeactivationResult{UserID: id, Status: "deactivated", RevokedSessions: 2}, s.err
}
func (s *lifecycleStoreStub) WriteAudit(_ context.Context, event platformstore.AuditEvent) {
	s.event = event
}

func TestAccountLifecycleAdminOnlyAndFailureDoesNotClaimSuccess(t *testing.T) {
	for _, tc := range []struct {
		role   string
		err    error
		status int
	}{{"", nil, 401}, {"user", nil, 403}, {"admin", nil, 200}, {"admin", platformstore.ErrLastTeamOwner, 409}, {"admin", platformstore.ErrLastAdministrator, 409}, {"admin", platformstore.ErrSelfDeactivation, 409}, {"admin", platformstore.ErrActorInactive, 403}, {"admin", errors.New("secret-internal-details"), 500}} {
		s := &lifecycleStoreStub{err: tc.err}
		r := httptest.NewRequest(http.MethodPost, "/api/v1/users/8d852676-a996-4cc8-9ad8-206f58f92028/deactivate", nil)
		if tc.role != "" {
			r = r.WithContext(platformauth.WithPrincipal(r.Context(), platformauth.Principal{Role: tc.role, Subject: "actor"}))
		}
		w := httptest.NewRecorder()
		HandleUserLifecycle(w, r, s)
		if w.Code != tc.status {
			t.Fatalf("role=%s err=%v status=%d body=%s", tc.role, tc.err, w.Code, w.Body)
		}
		if tc.status == 401 || tc.status == 403 && tc.err == nil {
			if s.calls != 0 {
				t.Fatal("unauthorized store call")
			}
		}
		if strings.Contains(w.Body.String(), "secret-internal-details") {
			t.Fatal("internal failure leaked")
		}
		if tc.status == 200 && !strings.Contains(w.Body.String(), `"revoked_sessions":2`) {
			t.Fatal("missing committed counts")
		}
	}
}
