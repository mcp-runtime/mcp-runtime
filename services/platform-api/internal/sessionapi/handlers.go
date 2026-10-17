// Package sessionapi owns runtime-to-platform session management and the
// separate, server-scoped gateway read audience.
package sessionapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"mcp-runtime/pkg/apihttp"
	"mcp-runtime/pkg/sessionstate"
)

type Store interface {
	PutSession(context.Context, sessionstate.Session) (sessionstate.Session, error)
	GetSession(context.Context, string, string) (sessionstate.Session, bool, error)
	ListSessions(context.Context, sessionstate.Filter) (sessionstate.Page, error)
	SetSessionRevoked(context.Context, string, string, bool, string) error
	DeleteSession(context.Context, string, string) error
	DeleteSessionVersioned(context.Context, string, string, string) error
	RevokeSessions(context.Context, sessionstate.Filter, string) (int64, error)
	SessionSnapshot(context.Context, sessionstate.Server) (sessionstate.Snapshot, error)
	SessionChanges(context.Context, sessionstate.Server, string, int64) (sessionstate.Changes, error)
}

type Handler struct {
	Store        Store
	Token        string
	Authenticate func(context.Context, string) (sessionstate.Server, error)
}

func write(w http.ResponseWriter, status int, item any) { apihttp.WriteJSON(w, status, item) }
func fail(w http.ResponseWriter, status int, message string) {
	codes := map[int]string{400: apihttp.CodeInvalidRequestBody, 401: apihttp.CodeUnauthorized, 403: apihttp.CodeForbidden, 404: apihttp.CodeNotFound, 409: apihttp.CodeConflict, 410: "sync_cursor_expired", 503: apihttp.CodeServiceUnavailable}
	code := codes[status]
	if code == "" {
		code = apihttp.CodeInternalError
	}
	apihttp.WriteEnvelope(w, status, code, message)
}
func bearer(r *http.Request) string {
	v := r.Header.Get("Authorization")
	if !strings.HasPrefix(v, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(v, "Bearer "))
}
func decode(w http.ResponseWriter, r *http.Request, out any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		fail(w, http.StatusBadRequest, "invalid session request")
		return false
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		fail(w, http.StatusBadRequest, "request must contain one JSON object")
		return false
	}
	return true
}
func storeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sessionstate.ErrInvalidSession):
		fail(w, 400, "invalid session identity, subject or expiry")
	case errors.Is(err, sessionstate.ErrNotFound):
		fail(w, 404, "session not found")
	case errors.Is(err, sessionstate.ErrConflict):
		fail(w, 409, "session identity conflicts, was deleted, or belongs to a retired server; create a new session name")
	case errors.Is(err, sessionstate.ErrInactiveAgent):
		fail(w, 403, "session agent is not active in the subject team")
	case errors.Is(err, sessionstate.ErrInactiveHuman):
		fail(w, 403, "session human account is inactive")
	default:
		fail(w, 503, "session store unavailable")
	}
}

// RegisterAccess mounts only on the control-plane listener. Tenant tokens are
// never accepted here, and the internal shared token is never accepted by Gateway.
func (h Handler) RegisterAccess(mux *http.ServeMux) {
	wrap := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			token := bearer(r)
			if h.Token == "" || token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(h.Token)) != 1 {
				fail(w, 401, "internal authentication required")
				return
			}
			if h.Store == nil {
				fail(w, 503, "session store unavailable")
				return
			}
			next(w, r)
		}
	}
	mux.HandleFunc(sessionstate.AccessPath, wrap(h.collection))
	mux.HandleFunc(sessionstate.AccessPath+"/", wrap(h.item))
}

func (h Handler) collection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var item sessionstate.Session
		if !decode(w, r, &item) {
			return
		}
		if item.Namespace == "" || item.Binding.Name == "" || item.ServerNamespace == "" || item.ServerName == "" || item.ServerUID == "" || (item.Binding.AgentID == "" && item.Binding.HumanID == "" && item.Binding.TeamID == "") {
			fail(w, 400, "session identity, server UID and subject are required")
			return
		}
		result, err := h.Store.PutSession(r.Context(), item)
		if err != nil {
			storeError(w, err)
			return
		}
		write(w, 201, result)
	case http.MethodGet:
		q := r.URL.Query()
		limit, _ := strconv.Atoi(q.Get("limit"))
		page, err := h.Store.ListSessions(r.Context(), sessionstate.Filter{Namespace: q.Get("namespace"), ServerName: q.Get("server"), ServerUID: q.Get("server_uid"), AgentID: q.Get("agent_id"), GrantNamespace: q.Get("grant_namespace"), GrantName: q.Get("grant_name"), After: q.Get("after"), Limit: limit})
		if err != nil {
			storeError(w, err)
			return
		}
		write(w, 200, page)
	default:
		w.WriteHeader(405)
	}
}

func (h Handler) item(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, sessionstate.AccessPath+"/")
	if path == "revoke" {
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		var request sessionstate.BulkRevokeRequest
		if !decode(w, r, &request) {
			return
		}
		if request.Filter.ServerUID == "" && request.Filter.AgentID == "" && request.Filter.GrantName == "" {
			fail(w, 400, "bulk revoke requires server UID, agent or grant")
			return
		}
		count, err := h.Store.RevokeSessions(r.Context(), request.Filter, request.Cause)
		if err != nil {
			storeError(w, err)
			return
		}
		write(w, 200, sessionstate.BulkRevokeResponse{Count: count})
		return
	}
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		w.WriteHeader(404)
		return
	}
	namespace, name := parts[0], parts[1]
	switch r.Method {
	case http.MethodGet:
		item, ok, err := h.Store.GetSession(r.Context(), namespace, name)
		if err != nil {
			storeError(w, err)
			return
		}
		if !ok {
			fail(w, 404, "session not found")
			return
		}
		write(w, 200, item)
	case http.MethodPatch:
		var request sessionstate.RevokeRequest
		if !decode(w, r, &request) {
			return
		}
		if err := h.Store.SetSessionRevoked(r.Context(), namespace, name, request.Revoked, request.Cause); err != nil {
			storeError(w, err)
			return
		}
		w.WriteHeader(204)
	case http.MethodDelete:
		if err := h.Store.DeleteSessionVersioned(r.Context(), namespace, name, r.URL.Query().Get("uid")); err != nil {
			storeError(w, err)
			return
		}
		w.WriteHeader(204)
	default:
		w.WriteHeader(405)
	}
}

// Gateway returns a mux used exclusively on the dedicated gateway port.
func (h Handler) Gateway() http.Handler {
	mux := http.NewServeMux()
	serve := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(405)
			return
		}
		if h.Authenticate == nil || bearer(r) == "" {
			fail(w, 401, "gateway identity required")
			return
		}
		server, err := h.Authenticate(r.Context(), bearer(r))
		if err != nil {
			fail(w, 403, "invalid gateway identity")
			return
		}
		if server.UID == "" || server.Namespace == "" {
			fail(w, 403, "gateway server identity required")
			return
		}
		if h.Store == nil {
			fail(w, 503, "session store unavailable")
			return
		}
		switch r.URL.Path {
		case sessionstate.GatewayPath:
			// Snapshot scope is solely authenticated identity; query parameters
			// cannot select a namespace or another server.
			out, err := h.Store.SessionSnapshot(r.Context(), server)
			if err != nil {
				storeError(w, err)
				return
			}
			write(w, 200, out)
		case sessionstate.GatewayPath + "/changes":
			after, err := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
			if err != nil || after < 0 || r.URL.Query().Get("epoch") == "" {
				fail(w, 400, "epoch and nonnegative after cursor required")
				return
			}
			out, err := h.Store.SessionChanges(r.Context(), server, r.URL.Query().Get("epoch"), after)
			if errors.Is(err, sessionstate.ErrReset) {
				fail(w, 410, "session cursor expired; relist required")
				return
			}
			if err != nil {
				storeError(w, err)
				return
			}
			write(w, 200, out)
		default:
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, sessionstate.GatewayPath+"/"), "/")
			if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
				w.WriteHeader(404)
				return
			}
			item, ok, err := h.Store.GetSession(r.Context(), parts[0], parts[1])
			if err != nil {
				storeError(w, err)
				return
			}
			if !ok || item.ServerUID != server.UID || item.ServerNamespace != server.Namespace || item.ServerName != server.Name {
				fail(w, 404, "session not found")
				return
			}
			write(w, 200, item)
		}
	}
	mux.HandleFunc(sessionstate.GatewayPath, serve)
	mux.HandleFunc(sessionstate.GatewayPath+"/", serve)
	return mux
}
