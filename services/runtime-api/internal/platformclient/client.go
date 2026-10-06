package platformclient

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"mcp-runtime/pkg/apihttp"
	"mcp-runtime/pkg/internalapi"
	"mcp-runtime/pkg/platformauth"
)

// Client calls platform-api internal identity and audit endpoints.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

var (
	ErrAgentNotFound    = errors.New("agent not found")
	ErrTeamNotFound     = errors.New("team not found")
	ErrAgentNameTaken   = errors.New("agent name already exists in team")
	ErrInvalidAgentName = errors.New("invalid agent name")
)

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) authorizedJSON(ctx context.Context, method, path string, body any, out any) (int, error) {
	return c.requestJSON(ctx, method, path, body, out, false)
}

func (c *Client) requestJSON(ctx context.Context, method, path string, body any, out any, decodeError bool) (int, error) {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, reader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.Token))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if decodeError && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
		var envelope struct {
			Code    string `json:"error"`
			Message string `json:"message"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&envelope); err != nil || envelope.Message == "" || envelope.Code == "" {
			return resp.StatusCode, apihttp.Internal("platform identity request failed")
		}
		return resp.StatusCode, &apihttp.Error{Status: resp.StatusCode, Code: envelope.Code, Message: envelope.Message}
	}
	if out != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

func (c *Client) ListTeams(ctx context.Context) ([]Team, error) {
	var result internalapi.TeamsListResponse
	status, err := c.authorizedJSON(ctx, http.MethodGet, "/internal/identity/teams", nil, &result)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("list teams: status %d", status)
	}
	return result.Teams, nil
}

func (c *Client) GetTeamBySlug(ctx context.Context, slug string) (Team, bool, error) {
	var team Team
	status, err := c.authorizedJSON(ctx, http.MethodGet, "/internal/identity/teams/"+url.PathEscape(slug), nil, &team)
	if err != nil {
		return Team{}, false, err
	}
	if status == http.StatusNotFound {
		return Team{}, false, nil
	}
	if status != http.StatusOK {
		return Team{}, false, fmt.Errorf("get team: status %d", status)
	}
	return team, true, nil
}

func (c *Client) CreateTeam(ctx context.Context, slug, name, createdByUserID string) (Team, error) {
	var team Team
	status, err := c.authorizedJSON(ctx, http.MethodPost, "/internal/identity/teams", internalapi.TeamCreateRequest{
		Slug:            slug,
		Name:            name,
		CreatedByUserID: createdByUserID,
	}, &team)
	if err != nil {
		return Team{}, err
	}
	if status != http.StatusCreated {
		return Team{}, fmt.Errorf("create team: status %d", status)
	}
	return team, nil
}

func (c *Client) CreateAgent(ctx context.Context, teamSlug, name, createdBy string) (Agent, error) {
	var item Agent
	path := "/internal/identity/teams/" + url.PathEscape(teamSlug) + "/agents"
	status, err := c.authorizedJSON(ctx, http.MethodPost, path, internalapi.AgentCreateRequest{Name: name, CreatedBy: createdBy}, &item)
	if err != nil {
		return Agent{}, err
	}
	if status == http.StatusConflict {
		return Agent{}, ErrAgentNameTaken
	}
	if status == http.StatusBadRequest {
		return Agent{}, ErrInvalidAgentName
	}
	if status == http.StatusNotFound {
		return Agent{}, ErrTeamNotFound
	}
	if status != http.StatusCreated {
		return Agent{}, fmt.Errorf("create agent: status %d", status)
	}
	return item, nil
}

func (c *Client) ListAgents(ctx context.Context, teamSlug, statusFilter, q, cursor string, limit int) (AgentPage, error) {
	params := url.Values{}
	if statusFilter != "" {
		params.Set("status", statusFilter)
	}
	if q != "" {
		params.Set("q", q)
	}
	if cursor != "" {
		params.Set("cursor", cursor)
	}
	if limit > 0 {
		params.Set("limit", fmt.Sprintf("%d", limit))
	}
	path := "/internal/identity/teams/" + url.PathEscape(teamSlug) + "/agents"
	if encoded := params.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var page AgentPage
	status, err := c.authorizedJSON(ctx, http.MethodGet, path, nil, &page)
	if err != nil {
		return AgentPage{}, err
	}
	if status == http.StatusNotFound {
		return AgentPage{}, ErrTeamNotFound
	}
	if status != http.StatusOK {
		return AgentPage{}, fmt.Errorf("list agents: status %d", status)
	}
	return page, nil
}

func (c *Client) GetAgent(ctx context.Context, id string) (Agent, bool, error) {
	var item Agent
	status, err := c.authorizedJSON(ctx, http.MethodGet, "/internal/identity/agents/"+url.PathEscape(id), nil, &item)
	if err != nil {
		return Agent{}, false, err
	}
	if status == http.StatusNotFound {
		return Agent{}, false, nil
	}
	if status != http.StatusOK {
		return Agent{}, false, fmt.Errorf("get agent: status %d", status)
	}
	return item, true, nil
}

func (c *Client) RenameAgent(ctx context.Context, id, name string) (Agent, error) {
	var item Agent
	status, err := c.authorizedJSON(ctx, http.MethodPatch, "/internal/identity/agents/"+url.PathEscape(id), internalapi.AgentRenameRequest{Name: name}, &item)
	if err != nil {
		return Agent{}, err
	}
	if status == http.StatusNotFound {
		return Agent{}, ErrAgentNotFound
	}
	if status == http.StatusConflict {
		return Agent{}, ErrAgentNameTaken
	}
	if status == http.StatusBadRequest {
		return Agent{}, ErrInvalidAgentName
	}
	if status != http.StatusOK {
		return Agent{}, fmt.Errorf("rename agent: status %d", status)
	}
	return item, nil
}

func (c *Client) SetAgentStatus(ctx context.Context, id, statusName, actorID string) (Agent, error) {
	var item Agent
	var action string
	switch statusName {
	case "inactive":
		action = "deactivate"
	case "active":
		action = "reactivate"
	default:
		return Agent{}, fmt.Errorf("set agent status: unsupported status %q", statusName)
	}
	path := "/internal/identity/agents/" + url.PathEscape(id) + "/" + action
	status, err := c.authorizedJSON(ctx, http.MethodPost, path, map[string]string{"actor_id": actorID}, &item)
	if err != nil {
		return Agent{}, err
	}
	if status == http.StatusNotFound {
		return Agent{}, ErrAgentNotFound
	}
	if status != http.StatusOK {
		return Agent{}, fmt.Errorf("set agent status: status %d", status)
	}
	return item, nil
}

func (c *Client) DeleteTeamBySlug(ctx context.Context, slug string) error {
	status, err := c.authorizedJSON(ctx, http.MethodDelete, "/internal/identity/teams/"+url.PathEscape(slug), nil, nil)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		return sql.ErrNoRows
	}
	if status != http.StatusNoContent {
		return fmt.Errorf("delete team: status %d", status)
	}
	return nil
}

func (c *Client) PrincipalForUserID(ctx context.Context, userID string) (platformauth.Principal, error) {
	var result internalapi.PrincipalResolveResponse
	status, err := c.authorizedJSON(ctx, http.MethodPost, "/internal/identity/principal", internalapi.PrincipalResolveRequest{
		UserID: userID,
	}, &result)
	if err != nil {
		return platformauth.Principal{}, err
	}
	if status == http.StatusNotFound {
		return platformauth.Principal{}, sql.ErrNoRows
	}
	if status != http.StatusOK {
		return platformauth.Principal{}, fmt.Errorf("resolve principal: status %d", status)
	}
	return result.Principal, nil
}

func (c *Client) ListNamespaces(ctx context.Context) ([]map[string]any, error) {
	var result internalapi.NamespacesListResponse
	status, err := c.authorizedJSON(ctx, http.MethodGet, "/internal/identity/namespaces", nil, &result)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("list namespaces: status %d", status)
	}
	return result.Namespaces, nil
}

func (c *Client) GetNamespace(ctx context.Context, namespace string) (map[string]any, bool, error) {
	var item map[string]any
	status, err := c.authorizedJSON(ctx, http.MethodGet, "/internal/identity/namespaces/"+url.PathEscape(namespace), nil, &item)
	if err != nil {
		return nil, false, err
	}
	if status == http.StatusNotFound {
		return nil, false, nil
	}
	if status != http.StatusOK {
		return nil, false, fmt.Errorf("get namespace: status %d", status)
	}
	return item, true, nil
}

func (c *Client) ListTeamMemberships(ctx context.Context, teamSlug string) ([]TeamMembership, error) {
	var result internalapi.TeamMembersListResponse
	path := "/internal/identity/teams/" + url.PathEscape(teamSlug) + "/members"
	status, err := c.authorizedJSON(ctx, http.MethodGet, path, nil, &result)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("list team members: status %d", status)
	}
	return result.Members, nil
}

func (c *Client) UpsertTeamMembership(ctx context.Context, teamSlug, userID, role string) (TeamMembership, error) {
	var result internalapi.TeamMembershipResponse
	path := "/internal/identity/teams/" + url.PathEscape(teamSlug) + "/members/" + url.PathEscape(userID)
	status, err := c.requestJSON(ctx, http.MethodPut, path, internalapi.TeamMembershipPutRequest{
		Role: role,
	}, &result, true)
	if err != nil {
		return TeamMembership{}, err
	}
	if status == http.StatusNotFound {
		return TeamMembership{}, sql.ErrNoRows
	}
	if status != http.StatusOK {
		return TeamMembership{}, fmt.Errorf("upsert team membership: status %d", status)
	}
	return result.Membership, nil
}

func (c *Client) DeleteTeamMembership(ctx context.Context, teamSlug, userID string) error {
	path := "/internal/identity/teams/" + url.PathEscape(teamSlug) + "/members/" + url.PathEscape(userID)
	status, err := c.authorizedJSON(ctx, http.MethodDelete, path, nil, nil)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		return sql.ErrNoRows
	}
	if status != http.StatusNoContent && status != http.StatusOK {
		return fmt.Errorf("delete team membership: status %d", status)
	}
	return nil
}

func (c *Client) CreatePasswordUser(ctx context.Context, email, password, role string) (User, error) {
	var result internalapi.CreateUserResponse
	status, err := c.requestJSON(ctx, http.MethodPost, "/internal/identity/users", internalapi.CreateUserRequest{
		Email:    email,
		Password: password,
		Role:     role,
	}, &result, true)
	if err != nil {
		return User{}, err
	}
	if status != http.StatusCreated {
		return User{}, fmt.Errorf("create user: status %d", status)
	}
	return result.User, nil
}

func (c *Client) CreateTeamUser(ctx context.Context, teamSlug, email, password, role string) (User, TeamMembership, error) {
	var result internalapi.TeamUserCreateResponse
	path := "/internal/identity/teams/" + url.PathEscape(teamSlug) + "/users"
	status, err := c.requestJSON(ctx, http.MethodPost, path, internalapi.TeamUserCreateRequest{
		Email:    email,
		Password: password,
		Role:     role,
	}, &result, true)
	if err != nil {
		return User{}, TeamMembership{}, err
	}
	if status == http.StatusNotFound {
		return User{}, TeamMembership{}, sql.ErrNoRows
	}
	if status != http.StatusCreated {
		return User{}, TeamMembership{}, fmt.Errorf("create team user: status %d", status)
	}
	return result.User, result.Membership, nil
}

// WriteAudit posts an audit event to platform-api.
func (c *Client) WriteAudit(ctx context.Context, event AuditEvent) {
	status, err := c.authorizedJSON(ctx, http.MethodPost, "/internal/audit", event, nil)
	if err != nil || status != http.StatusAccepted {
		if err == nil {
			err = fmt.Errorf("audit status %d", status)
		}
		_ = err
	}
}

// OperationsSnapshot loads user, audit, and image activity for admin operations.
func (c *Client) OperationsSnapshot(ctx context.Context, filter OperationsFilter) (OperationsSnapshot, error) {
	q := url.Values{}
	if filter.User != "" {
		q.Set("user", filter.User)
	}
	if !filter.Since.IsZero() {
		q.Set("since", filter.Since.UTC().Format(time.RFC3339))
	}
	if !filter.Until.IsZero() {
		q.Set("until", filter.Until.UTC().Format(time.RFC3339))
	}
	if filter.Limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", filter.Limit))
	}
	path := "/internal/operations/snapshot"
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var snapshot OperationsSnapshot
	status, err := c.authorizedJSON(ctx, http.MethodGet, path, nil, &snapshot)
	if err != nil {
		return OperationsSnapshot{}, err
	}
	if status != http.StatusOK {
		return OperationsSnapshot{}, fmt.Errorf("operations snapshot: status %d", status)
	}
	return snapshot, nil
}

// Configured reports whether the client can reach platform-api.
func (c *Client) Configured() bool {
	return c != nil && strings.TrimSpace(c.BaseURL) != "" && strings.TrimSpace(c.Token) != ""
}

// ErrNotConfigured is returned when platform identity is unavailable.
var ErrNotConfigured = errors.New("platform identity client not configured")
