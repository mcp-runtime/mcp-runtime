package platformapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type Agent struct {
	ID        string `json:"id"`
	TeamID    string `json:"team_id"`
	TeamSlug  string `json:"team_slug"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type AgentPage struct {
	Agents     []Agent `json:"agents"`
	NextCursor string  `json:"next_cursor,omitempty"`
}

func (c *PlatformClient) ListAgents(ctx context.Context, teamSlug, status, query, cursor string, limit int) (AgentPage, error) {
	values := url.Values{}
	if strings.TrimSpace(status) != "" {
		values.Set("status", strings.TrimSpace(status))
	}
	if strings.TrimSpace(query) != "" {
		values.Set("q", strings.TrimSpace(query))
	}
	if strings.TrimSpace(cursor) != "" {
		values.Set("cursor", cursor)
	}
	if limit > 0 {
		values.Set("limit", fmt.Sprint(limit))
	}
	rel := "/runtime/teams/" + url.PathEscape(strings.TrimSpace(teamSlug)) + "/agents"
	resp, err := c.do(ctx, http.MethodGet, rel, values.Encode(), nil)
	if err != nil {
		return AgentPage{}, err
	}
	defer resp.Body.Close()
	b, err := readBody(resp.Body)
	if err != nil {
		return AgentPage{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return AgentPage{}, httpAPIError(resp.StatusCode, b)
	}
	var page AgentPage
	if err := json.Unmarshal(b, &page); err != nil {
		return AgentPage{}, err
	}
	return page, nil
}

func (c *PlatformClient) GetAgent(ctx context.Context, id string) (Agent, error) {
	resp, err := c.do(ctx, http.MethodGet, "/runtime/agents/"+url.PathEscape(strings.TrimSpace(id)), "", nil)
	if err != nil {
		return Agent{}, err
	}
	defer resp.Body.Close()
	b, err := readBody(resp.Body)
	if err != nil {
		return Agent{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Agent{}, httpAPIError(resp.StatusCode, b)
	}
	var out struct {
		Agent Agent `json:"agent"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return Agent{}, err
	}
	return out.Agent, nil
}

func (c *PlatformClient) CreateAgent(ctx context.Context, teamSlug, name string) (Agent, error) {
	return c.writeAgent(ctx, http.MethodPost, "/runtime/teams/"+url.PathEscape(strings.TrimSpace(teamSlug))+"/agents", map[string]string{"name": strings.TrimSpace(name)})
}

func (c *PlatformClient) RenameAgent(ctx context.Context, id, name string) (Agent, error) {
	return c.writeAgent(ctx, http.MethodPatch, "/runtime/agents/"+url.PathEscape(strings.TrimSpace(id)), map[string]string{"name": strings.TrimSpace(name)})
}

func (c *PlatformClient) SetAgentActive(ctx context.Context, id string, active bool) (Agent, error) {
	action := "deactivate"
	if active {
		action = "reactivate"
	}
	return c.writeAgent(ctx, http.MethodPost, "/runtime/agents/"+url.PathEscape(strings.TrimSpace(id))+"/"+action, nil)
}

func (c *PlatformClient) writeAgent(ctx context.Context, method, path string, payload any) (Agent, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return Agent{}, err
		}
		body = bytes.NewReader(encoded)
	}
	resp, err := c.do(ctx, method, path, "", body)
	if err != nil {
		return Agent{}, err
	}
	defer resp.Body.Close()
	b, err := readBody(resp.Body)
	if err != nil {
		return Agent{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Agent{}, httpAPIError(resp.StatusCode, b)
	}
	var out struct {
		Agent Agent `json:"agent"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return Agent{}, err
	}
	return out.Agent, nil
}

func (c *PlatformClient) RevokeGrantSessions(ctx context.Context, namespace, name string) (int, error) {
	path := "/runtime/grants/" + url.PathEscape(strings.TrimSpace(namespace)) + "/" + url.PathEscape(strings.TrimSpace(name)) + "/revoke-sessions"
	resp, err := c.do(ctx, http.MethodPost, path, "", nil)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, err := readBody(resp.Body)
	if err != nil {
		return 0, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, httpAPIError(resp.StatusCode, b)
	}
	var out struct {
		RevokedSessions int `json:"revokedSessions"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return 0, err
	}
	return out.RevokedSessions, nil
}
