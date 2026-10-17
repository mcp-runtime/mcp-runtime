package identity

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"mcp-platform-api/internal/httperrors"
	"mcp-platform-api/internal/platformstore"
	"mcp-runtime/pkg/apihttp"
	"mcp-runtime/pkg/platform"
	"mcp-runtime/pkg/platformauth"
)

type UserLifecycleStore interface {
	DeactivateUser(context.Context, string, string, platformstore.AuditEvent) (platform.UserDeactivationResult, error)
	WriteAudit(context.Context, platformstore.AuditEvent)
}

func HandleUserLifecycle(w http.ResponseWriter, r *http.Request, store UserLifecycleStore) {
	p, ok := platformauth.FromContext(r.Context())
	if !ok {
		httperrors.Unauthorized(w)
		return
	}
	if p.Role != platformauth.RoleAdmin {
		httperrors.Forbidden(w, "administrator role required")
		return
	}
	if r.Method != http.MethodPost {
		httperrors.MethodNotAllowed(w, http.MethodPost)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/users/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] != "deactivate" {
		httperrors.NotFound(w, "unknown account action")
		return
	}
	if store == nil {
		httperrors.PlatformUnavailable(w)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	event := platformstore.AuditEvent{UserID: p.UserID(), Action: "user_deactivate", Resource: parts[0], ActorIP: platformauth.RequestIP(r), Source: platformauth.AuditSource(r, p), AuthIdentity: platformauth.AuditIdentityLabel(p)}
	out, err := store.DeactivateUser(ctx, parts[0], p.UserID(), event)
	if err != nil {
		event.Status = "error"
		event.Message = "account cleanup was not confirmed"
		store.WriteAudit(ctx, event)
		var input platformstore.UserInputError
		switch {
		case errors.As(err, &input):
			httperrors.BadRequest(w, string(input))
		case errors.Is(err, sql.ErrNoRows):
			httperrors.NotFound(w, "user not found")
		case errors.Is(err, platformstore.ErrActorInactive):
			httperrors.Forbidden(w, "administrator account is no longer active")
		case errors.Is(err, platformstore.ErrSelfDeactivation), errors.Is(err, platformstore.ErrLastAdministrator), errors.Is(err, platformstore.ErrLastTeamOwner):
			apihttp.WriteEnvelope(w, http.StatusConflict, apihttp.CodeConflict, err.Error())
		default:
			httperrors.Internal(w, "account cleanup was not confirmed; retry the idempotent deactivation command")
		}
		return
	}
	apihttp.WriteJSON(w, http.StatusOK, out)
}
