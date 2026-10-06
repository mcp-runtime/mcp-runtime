package platforminternal

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"mcp-platform-api/internal/platformstore"
	"mcp-runtime/pkg/apihttp"
	"mcp-runtime/pkg/internalapi"
	"mcp-runtime/pkg/platformauth"
)

type PlatformStore interface {
	PrincipalForUserID(ctx context.Context, userID string) (platformauth.Principal, error)
	AuthenticateUserAPIKey(ctx context.Context, rawKey string) (platformauth.Principal, bool, error)
	ResolveUserIDs(ctx context.Context, ids []string) (map[string]string, error)
	ResolveTeamIDs(ctx context.Context, ids []string) (map[string]string, error)
	WriteAudit(ctx context.Context, event platformstore.AuditEvent)
	CreateTeam(ctx context.Context, slug, name, createdByUserID string) (platformstore.Team, error)
	ListTeams(ctx context.Context) ([]platformstore.Team, error)
	GetTeamBySlug(ctx context.Context, slug string) (platformstore.Team, bool, error)
	CreateAgent(ctx context.Context, teamSlug, name, createdBy string) (platformstore.Agent, error)
	ListAgents(ctx context.Context, filter platformstore.AgentListFilter) (platformstore.AgentPage, error)
	GetAgent(ctx context.Context, id string) (platformstore.Agent, bool, error)
	RenameAgent(ctx context.Context, id, name string) (platformstore.Agent, error)
	SetAgentStatus(ctx context.Context, id, status, actorID string) (platformstore.Agent, error)
	DeleteTeamBySlug(ctx context.Context, slug string) error
	ListNamespaces(ctx context.Context) ([]map[string]any, error)
	GetNamespace(ctx context.Context, namespace string) (map[string]any, bool, error)
	ListTeamMemberships(ctx context.Context, teamSlug string) ([]platformstore.TeamMembership, error)
	UpsertTeamMembership(ctx context.Context, teamSlug, userID, role string) (platformstore.TeamMembership, error)
	DeleteTeamMembership(ctx context.Context, teamSlug, userID string) error
	CreateTeamUser(ctx context.Context, teamSlug, email, password, role string) (platformstore.User, platformstore.TeamMembership, error)
	CreatePasswordUser(ctx context.Context, email, password, role string) (platformstore.User, error)
	OperationsSnapshot(ctx context.Context, filter platformstore.OperationsFilter) (platformstore.OperationsSnapshot, error)
}

type Handler struct {
	Store               PlatformStore
	Token               string
	AuthenticateRequest func(*http.Request) (platformauth.Principal, bool, error)
}

func (h Handler) Register(mux *http.ServeMux) {
	mux.Handle("/internal/auth/resolve", h.authorize(http.HandlerFunc(h.resolveAuth)))
	mux.Handle("/internal/identity/principal", h.authorize(http.HandlerFunc(h.resolvePrincipal)))
	mux.Handle("/internal/identity/resolve-ids", h.authorize(http.HandlerFunc(h.resolveIDs)))
	mux.Handle("/internal/audit", h.authorize(http.HandlerFunc(h.audit)))
	mux.Handle("/internal/identity/teams", h.authorize(http.HandlerFunc(h.teams)))
	mux.Handle("/internal/identity/teams/", h.authorize(http.HandlerFunc(h.teamPath)))
	mux.Handle("/internal/identity/agents/", h.authorize(http.HandlerFunc(h.agentPath)))
	mux.Handle("/internal/identity/namespaces", h.authorize(http.HandlerFunc(h.namespaces)))
	mux.Handle("/internal/identity/namespaces/", h.authorize(http.HandlerFunc(h.namespaceItem)))
	mux.Handle("/internal/identity/users", h.authorize(http.HandlerFunc(h.createUser)))
	mux.Handle("/internal/operations/snapshot", h.authorize(http.HandlerFunc(h.operationsSnapshot)))
}

func (h Handler) agentPath(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/internal/identity/agents/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		apihttp.WriteEnvelope(w, http.StatusNotFound, apihttp.CodeNotFound, "agent not found")
		return
	}
	id := parts[0]
	if len(parts) == 1 && r.Method == http.MethodGet {
		item, ok, err := h.Store.GetAgent(r.Context(), id)
		if err != nil {
			apihttp.WriteEnvelope(w, http.StatusInternalServerError, apihttp.CodeQueryFailed, "failed to get agent")
			return
		}
		if !ok {
			apihttp.WriteEnvelope(w, http.StatusNotFound, apihttp.CodeNotFound, "agent not found")
			return
		}
		writeJSON(w, http.StatusOK, agentToInternal(item))
		return
	}
	if len(parts) == 1 && r.Method == http.MethodPatch {
		var request internalapi.AgentRenameRequest
		if err := decodeJSON(r, &request); err != nil {
			apihttp.WriteEnvelope(w, http.StatusBadRequest, apihttp.CodeInvalidRequestBody, "invalid request body")
			return
		}
		item, err := h.Store.RenameAgent(r.Context(), id, request.Name)
		if err != nil {
			writeAgentStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, agentToInternal(item))
		return
	}
	if len(parts) == 2 && r.Method == http.MethodPost && (parts[1] == "deactivate" || parts[1] == "reactivate") {
		var request struct {
			ActorID string `json:"actor_id"`
		}
		if err := decodeJSON(r, &request); err != nil {
			apihttp.WriteEnvelope(w, http.StatusBadRequest, apihttp.CodeInvalidRequestBody, "invalid request body")
			return
		}
		status := "inactive"
		if parts[1] == "reactivate" {
			status = "active"
		}
		item, err := h.Store.SetAgentStatus(r.Context(), id, status, request.ActorID)
		if err != nil {
			writeAgentStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, agentToInternal(item))
		return
	}
	methodNotAllowed(w)
}

func writeAgentStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		apihttp.WriteEnvelope(w, http.StatusNotFound, apihttp.CodeNotFound, "team not found")
	case errors.Is(err, platformstore.ErrAgentNotFound):
		apihttp.WriteEnvelope(w, http.StatusNotFound, apihttp.CodeNotFound, "agent not found")
	case errors.Is(err, platformstore.ErrAgentNameTaken):
		apihttp.WriteEnvelope(w, http.StatusConflict, apihttp.CodeConflict, "an agent with this name already exists in the team")
	case errors.Is(err, platformstore.ErrInvalidAgentName):
		apihttp.WriteEnvelope(w, http.StatusBadRequest, apihttp.CodeInvalidRequestBody, err.Error())
	default:
		apihttp.WriteEnvelope(w, http.StatusInternalServerError, apihttp.CodeQueryFailed, "failed to update agent")
	}
}

func agentToInternal(item platformstore.Agent) internalapi.Agent {
	return internalapi.Agent{ID: item.ID, TeamID: item.TeamID, TeamSlug: item.TeamSlug, Name: item.Name, Status: item.Status,
		CreatedBy: item.CreatedBy, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt, DeactivatedAt: item.DeactivatedAt, DeactivatedBy: item.DeactivatedBy}
}

func (h Handler) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expected := strings.TrimSpace(h.Token)
		provided := strings.TrimPrefix(strings.TrimSpace(r.Header.Get("Authorization")), "Bearer ")
		if expected == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
			apihttp.WriteEnvelope(w, http.StatusUnauthorized, apihttp.CodeUnauthorized, "internal authentication required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h Handler) resolveAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var request internalapi.AuthResolveRequest
	if err := decodeJSON(r, &request); err != nil {
		apihttp.WriteEnvelope(w, http.StatusBadRequest, apihttp.CodeInvalidRequestBody, "invalid request body")
		return
	}
	principal, ok, err := h.resolvePrincipalForAPIKey(r, request.APIKey)
	if err != nil {
		apihttp.WriteEnvelope(w, http.StatusInternalServerError, apihttp.CodeAuthFailed, "failed to resolve API key")
		return
	}
	writeJSON(w, http.StatusOK, internalapi.AuthResolveResponse{OK: ok, Principal: principal})
}

// resolvePrincipalForAPIKey uses the platform's complete public authentication
// policy for internal service-to-service key resolution. Falling back to the
// store keeps the handler useful in isolated tests and older embedders, while
// the production route can now resolve both user API keys and configured
// service/admin API keys consistently with /api/v1/auth/me.
func (h Handler) resolvePrincipalForAPIKey(r *http.Request, rawKey string) (platformauth.Principal, bool, error) {
	if h.AuthenticateRequest != nil {
		clone := r.Clone(r.Context())
		clone.Header.Set("x-api-key", strings.TrimSpace(rawKey))
		clone.Header.Del("authorization")
		return h.AuthenticateRequest(clone)
	}
	return h.Store.AuthenticateUserAPIKey(r.Context(), rawKey)
}

func (h Handler) resolvePrincipal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var request internalapi.PrincipalResolveRequest
	if err := decodeJSON(r, &request); err != nil {
		apihttp.WriteEnvelope(w, http.StatusBadRequest, apihttp.CodeInvalidRequestBody, "invalid request body")
		return
	}
	principal, err := h.Store.PrincipalForUserID(r.Context(), strings.TrimSpace(request.UserID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			apihttp.WriteEnvelope(w, http.StatusNotFound, apihttp.CodeNotFound, "user not found")
			return
		}
		apihttp.WriteEnvelope(w, http.StatusInternalServerError, apihttp.CodeQueryFailed, "failed to resolve principal")
		return
	}
	writeJSON(w, http.StatusOK, internalapi.PrincipalResolveResponse{Principal: principal})
}

func (h Handler) resolveIDs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var request internalapi.ResolveIDsRequest
	if err := decodeJSON(r, &request); err != nil {
		apihttp.WriteEnvelope(w, http.StatusBadRequest, apihttp.CodeInvalidRequestBody, "invalid request body")
		return
	}
	users, err := h.Store.ResolveUserIDs(r.Context(), request.UserIDs)
	if err != nil {
		apihttp.WriteEnvelope(w, http.StatusInternalServerError, apihttp.CodeQueryFailed, "failed to resolve user ids")
		return
	}
	teams, err := h.Store.ResolveTeamIDs(r.Context(), request.TeamIDs)
	if err != nil {
		apihttp.WriteEnvelope(w, http.StatusInternalServerError, apihttp.CodeQueryFailed, "failed to resolve team ids")
		return
	}
	writeJSON(w, http.StatusOK, internalapi.ResolveIDsResponse{Users: users, Teams: teams})
}

func (h Handler) audit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var event internalapi.AuditEvent
	if err := decodeJSON(r, &event); err != nil {
		apihttp.WriteEnvelope(w, http.StatusBadRequest, apihttp.CodeInvalidRequestBody, "invalid request body")
		return
	}
	h.Store.WriteAudit(r.Context(), platformstore.AuditEvent(event))
	w.WriteHeader(http.StatusAccepted)
}

func (h Handler) teams(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		teams, err := h.Store.ListTeams(r.Context())
		if err != nil {
			apihttp.WriteEnvelope(w, http.StatusInternalServerError, apihttp.CodeQueryFailed, "failed to list teams")
			return
		}
		writeJSON(w, http.StatusOK, internalapi.TeamsListResponse{Teams: teamsToInternal(teams)})
	case http.MethodPost:
		var request internalapi.TeamCreateRequest
		if err := decodeJSON(r, &request); err != nil {
			apihttp.WriteEnvelope(w, http.StatusBadRequest, apihttp.CodeInvalidRequestBody, "invalid request body")
			return
		}
		team, err := h.Store.CreateTeam(r.Context(), request.Slug, request.Name, request.CreatedByUserID)
		if err != nil {
			apihttp.WriteEnvelope(w, http.StatusBadRequest, apihttp.CodeInvalidRequestBody, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, teamToInternal(team))
	default:
		methodNotAllowed(w)
	}
}

func (h Handler) teamPath(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/internal/identity/teams/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		apihttp.WriteEnvelope(w, http.StatusNotFound, apihttp.CodeNotFound, "team not found")
		return
	}
	slug := parts[0]
	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		h.teamItemGet(w, r, slug)
	case len(parts) == 1 && r.Method == http.MethodDelete:
		h.teamItemDelete(w, r, slug)
	case len(parts) == 2 && parts[1] == "members" && r.Method == http.MethodGet:
		h.teamMembersList(w, r, slug)
	case len(parts) == 2 && parts[1] == "members" && r.Method == http.MethodPost:
		h.teamMembersUpsertBody(w, r, slug)
	case len(parts) == 3 && parts[1] == "members" && r.Method == http.MethodPut:
		h.teamMemberUpsert(w, r, slug, parts[2])
	case len(parts) == 3 && parts[1] == "members" && r.Method == http.MethodDelete:
		h.teamMemberDelete(w, r, slug, parts[2])
	case len(parts) == 2 && parts[1] == "users" && r.Method == http.MethodPost:
		h.teamUserCreate(w, r, slug)
	case len(parts) == 2 && parts[1] == "agents" && (r.Method == http.MethodGet || r.Method == http.MethodPost):
		h.teamAgents(w, r, slug)
	default:
		methodNotAllowed(w)
	}
}

func (h Handler) teamAgents(w http.ResponseWriter, r *http.Request, slug string) {
	if r.Method == http.MethodPost {
		var request internalapi.AgentCreateRequest
		if err := decodeJSON(r, &request); err != nil {
			apihttp.WriteEnvelope(w, http.StatusBadRequest, apihttp.CodeInvalidRequestBody, "invalid request body")
			return
		}
		item, err := h.Store.CreateAgent(r.Context(), slug, request.Name, request.CreatedBy)
		if err != nil {
			writeAgentStoreError(w, err)
			return
		}
		if team, ok, err := h.Store.GetTeamBySlug(r.Context(), slug); err == nil && ok {
			item.TeamSlug = team.Slug
		}
		writeJSON(w, http.StatusCreated, agentToInternal(item))
		return
	}
	query := r.URL.Query()
	limit := 0
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 200 {
			apihttp.WriteEnvelope(w, http.StatusBadRequest, apihttp.CodeInvalidRequestBody, "limit must be between 1 and 200")
			return
		}
		limit = parsed
	}
	status := strings.TrimSpace(query.Get("status"))
	if status != "" && status != "active" && status != "inactive" {
		apihttp.WriteEnvelope(w, http.StatusBadRequest, apihttp.CodeInvalidRequestBody, "status must be active or inactive")
		return
	}
	page, err := h.Store.ListAgents(r.Context(), platformstore.AgentListFilter{TeamSlug: slug, Status: status,
		Query: query.Get("q"), Cursor: query.Get("cursor"), Limit: limit})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			apihttp.WriteEnvelope(w, http.StatusNotFound, apihttp.CodeNotFound, "team not found")
		} else if strings.Contains(err.Error(), "invalid cursor") {
			apihttp.WriteEnvelope(w, http.StatusBadRequest, apihttp.CodeInvalidRequestBody, "invalid cursor")
		} else {
			apihttp.WriteEnvelope(w, http.StatusInternalServerError, apihttp.CodeQueryFailed, "failed to list agents")
		}
		return
	}
	out := internalapi.AgentPage{Agents: make([]internalapi.Agent, 0, len(page.Agents)), NextCursor: page.NextCursor}
	for _, item := range page.Agents {
		out.Agents = append(out.Agents, agentToInternal(item))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h Handler) teamItemGet(w http.ResponseWriter, r *http.Request, slug string) {
	team, ok, err := h.Store.GetTeamBySlug(r.Context(), slug)
	if err != nil {
		apihttp.WriteEnvelope(w, http.StatusInternalServerError, apihttp.CodeQueryFailed, "failed to get team")
		return
	}
	if !ok {
		apihttp.WriteEnvelope(w, http.StatusNotFound, apihttp.CodeNotFound, "team not found")
		return
	}
	writeJSON(w, http.StatusOK, teamToInternal(team))
}

func (h Handler) teamItemDelete(w http.ResponseWriter, r *http.Request, slug string) {
	if err := h.Store.DeleteTeamBySlug(r.Context(), slug); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			apihttp.WriteEnvelope(w, http.StatusNotFound, apihttp.CodeNotFound, "team not found")
			return
		}
		apihttp.WriteEnvelope(w, http.StatusInternalServerError, apihttp.CodeQueryFailed, "failed to delete team")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h Handler) teamMembersList(w http.ResponseWriter, r *http.Request, slug string) {
	members, err := h.Store.ListTeamMemberships(r.Context(), slug)
	if err != nil {
		apihttp.WriteEnvelope(w, http.StatusInternalServerError, apihttp.CodeQueryFailed, "failed to list team members")
		return
	}
	writeJSON(w, http.StatusOK, internalapi.TeamMembersListResponse{Members: members})
}

func (h Handler) teamMembersUpsertBody(w http.ResponseWriter, r *http.Request, slug string) {
	var request internalapi.TeamMembershipUpsertRequest
	if err := decodeJSON(r, &request); err != nil {
		apihttp.WriteEnvelope(w, http.StatusBadRequest, apihttp.CodeInvalidRequestBody, "invalid request body")
		return
	}
	role := strings.TrimSpace(request.Role)
	if role == "" {
		role = teamRoleMember
	}
	membership, err := h.Store.UpsertTeamMembership(r.Context(), slug, strings.TrimSpace(request.UserID), role)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			apihttp.WriteEnvelope(w, http.StatusNotFound, apihttp.CodeNotFound, "team or user not found")
			return
		}
		writeMembershipError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, internalapi.TeamMembershipResponse{Membership: membership})
}

func (h Handler) teamMemberUpsert(w http.ResponseWriter, r *http.Request, slug, userID string) {
	var request internalapi.TeamMembershipPutRequest
	if r.Method == http.MethodPut {
		if err := decodeJSON(r, &request); err != nil {
			apihttp.WriteEnvelope(w, http.StatusBadRequest, apihttp.CodeInvalidRequestBody, "invalid request body")
			return
		}
	}
	role := strings.TrimSpace(request.Role)
	if role == "" {
		role = teamRoleMember
	}
	membership, err := h.Store.UpsertTeamMembership(r.Context(), slug, strings.TrimSpace(userID), role)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			apihttp.WriteEnvelope(w, http.StatusNotFound, apihttp.CodeNotFound, "team or user not found")
			return
		}
		writeMembershipError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, internalapi.TeamMembershipResponse{Membership: membership})
}

func (h Handler) teamMemberDelete(w http.ResponseWriter, r *http.Request, slug, userID string) {
	if err := h.Store.DeleteTeamMembership(r.Context(), slug, userID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			apihttp.WriteEnvelope(w, http.StatusNotFound, apihttp.CodeNotFound, "membership not found")
			return
		}
		apihttp.WriteEnvelope(w, http.StatusInternalServerError, apihttp.CodeQueryFailed, "failed to delete membership")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h Handler) teamUserCreate(w http.ResponseWriter, r *http.Request, teamSlug string) {
	var request internalapi.TeamUserCreateRequest
	if err := decodeJSON(r, &request); err != nil {
		apihttp.WriteEnvelope(w, http.StatusBadRequest, apihttp.CodeInvalidRequestBody, "invalid request body")
		return
	}
	role := strings.TrimSpace(request.Role)
	if role == "" {
		role = teamRoleMember
	}
	user, membership, err := h.Store.CreateTeamUser(r.Context(), teamSlug, strings.TrimSpace(request.Email), request.Password, role)
	if err != nil {
		writeUserCreateError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, internalapi.TeamUserCreateResponse{User: userToInternal(user), Membership: membership})
}

func (h Handler) createUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var request internalapi.CreateUserRequest
	if err := decodeJSON(r, &request); err != nil {
		apihttp.WriteEnvelope(w, http.StatusBadRequest, apihttp.CodeInvalidRequestBody, "invalid request body")
		return
	}
	role := strings.TrimSpace(request.Role)
	if role == "" {
		role = roleUser
	}
	user, err := h.Store.CreatePasswordUser(r.Context(), strings.TrimSpace(request.Email), request.Password, role)
	if err != nil {
		writeUserCreateError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, internalapi.CreateUserResponse{User: userToInternal(user)})
}

func (h Handler) operationsSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	filter, err := operationsFilterFromRequest(r)
	if err != nil {
		apihttp.WriteEnvelope(w, http.StatusBadRequest, apihttp.CodeInvalidQueryParam, err.Error())
		return
	}
	snapshot, err := h.Store.OperationsSnapshot(r.Context(), filter)
	if err != nil {
		apihttp.WriteEnvelope(w, http.StatusInternalServerError, apihttp.CodeQueryFailed, "failed to load operations snapshot")
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

const (
	teamRoleMember = platformstore.TeamRoleMember
	roleUser       = platformstore.RoleUser
)

func operationsFilterFromRequest(r *http.Request) (platformstore.OperationsFilter, error) {
	user := strings.TrimSpace(r.URL.Query().Get("user"))
	filter := platformstore.OperationsFilter{
		User:       user,
		UserSearch: strings.ToLower(user),
		Limit:      queryInt(r, "limit", 50),
	}
	if filter.Limit < 1 {
		filter.Limit = 1
	}
	if filter.Limit > 200 {
		filter.Limit = 200
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("since")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			if parsed, err = time.Parse("2006-01-02", raw); err != nil {
				return platformstore.OperationsFilter{}, err
			}
		}
		filter.Since = parsed
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("until")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			if parsed, err = time.Parse("2006-01-02", raw); err != nil {
				return platformstore.OperationsFilter{}, err
			}
			parsed = parsed.Add(24*time.Hour - time.Nanosecond)
		}
		filter.Until = parsed
	}
	if !filter.Since.IsZero() && !filter.Until.IsZero() && filter.Since.After(filter.Until) {
		return platformstore.OperationsFilter{}, errors.New("since must be before until")
	}
	return filter, nil
}

func queryInt(r *http.Request, key string, fallback int) int {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}

func (h Handler) namespaces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	namespaces, err := h.Store.ListNamespaces(r.Context())
	if err != nil {
		apihttp.WriteEnvelope(w, http.StatusInternalServerError, apihttp.CodeQueryFailed, "failed to list namespaces")
		return
	}
	writeJSON(w, http.StatusOK, internalapi.NamespacesListResponse{Namespaces: namespaces})
}

func (h Handler) namespaceItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	namespace := strings.TrimPrefix(r.URL.Path, "/internal/identity/namespaces/")
	item, ok, err := h.Store.GetNamespace(r.Context(), namespace)
	if err != nil {
		apihttp.WriteEnvelope(w, http.StatusInternalServerError, apihttp.CodeQueryFailed, "failed to get namespace")
		return
	}
	if !ok {
		apihttp.WriteEnvelope(w, http.StatusNotFound, apihttp.CodeNotFound, "namespace not found")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func decodeJSON(r *http.Request, target any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func methodNotAllowed(w http.ResponseWriter) {
	apihttp.WriteEnvelope(w, http.StatusMethodNotAllowed, apihttp.CodeMethodNotAllowed, "method not allowed")
}

func teamToInternal(team platformstore.Team) internalapi.Team {
	return internalapi.Team{
		ID:        team.ID,
		Slug:      team.Slug,
		Name:      team.Name,
		Namespace: team.Namespace,
		CreatedAt: team.CreatedAt,
	}
}

func teamsToInternal(teams []platformstore.Team) []internalapi.Team {
	out := make([]internalapi.Team, len(teams))
	for i, team := range teams {
		out[i] = teamToInternal(team)
	}
	return out
}

func userToInternal(user platformstore.User) internalapi.User {
	return internalapi.User{
		ID:    user.ID,
		Email: user.Email,
		Role:  user.Role,
	}
}

func writeUserCreateError(w http.ResponseWriter, err error) {
	var input platformstore.UserInputError
	switch {
	case errors.Is(err, platformstore.ErrEmailAlreadyRegistered):
		apihttp.WriteEnvelope(w, http.StatusConflict, apihttp.CodeConflict, platformstore.ErrEmailAlreadyRegistered.Error())
	case errors.Is(err, sql.ErrNoRows):
		apihttp.WriteEnvelope(w, http.StatusNotFound, apihttp.CodeNotFound, "team not found")
	case errors.As(err, &input):
		apihttp.WriteEnvelope(w, http.StatusBadRequest, apihttp.CodeInvalidRequestBody, input.Error())
	default:
		apihttp.WriteEnvelope(w, http.StatusInternalServerError, apihttp.CodeInternalError, "failed to create user")
	}
}

func writeMembershipError(w http.ResponseWriter, err error) {
	var input platformstore.UserInputError
	if errors.As(err, &input) {
		apihttp.WriteEnvelope(w, http.StatusBadRequest, apihttp.CodeInvalidRequestBody, input.Error())
		return
	}
	apihttp.WriteEnvelope(w, http.StatusInternalServerError, apihttp.CodeInternalError, "failed to update membership")
}
