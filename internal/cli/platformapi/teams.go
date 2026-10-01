// Team, user, and namespace calls against the platform API.

package platformapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"mcp-runtime/pkg/platform"
)

type Team struct {
	ID        string    `json:"id"`
	Slug      string    `json:"slug"`
	Name      string    `json:"name"`
	Namespace string    `json:"namespace"`
	CreatedAt time.Time `json:"created_at"`
}

type PlatformUser struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	Namespace string `json:"namespace,omitempty"`
}

type TeamMembership = platform.TeamMembership

type teamsResponse struct {
	Teams []Team `json:"teams"`
}

type teamResponse struct {
	Team Team `json:"team"`
}

type teamMembersResponse struct {
	Members []TeamMembership `json:"members"`
}

type teamMembershipResponse struct {
	Membership TeamMembership `json:"membership"`
}

type userResponse struct {
	User PlatformUser `json:"user"`
}

type namespaceListItem struct {
	Namespace string `json:"namespace"`
	Scope     string `json:"scope,omitempty"`
	TeamID    string `json:"team_id,omitempty"`
	TeamSlug  string `json:"team_slug,omitempty"`
	TeamName  string `json:"team_name,omitempty"`
	TeamRole  string `json:"team_role,omitempty"`
	IsShared  bool   `json:"is_shared,omitempty"`
}

type namespacesResponse struct {
	Namespaces []namespaceListItem `json:"namespaces"`
}

func (c *PlatformClient) ListTeams(ctx context.Context) ([]Team, error) {
	resp, err := c.do(ctx, http.MethodGet, "/runtime/teams", "", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := readBody(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, httpAPIError(resp.StatusCode, b)
	}
	var out teamsResponse
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out.Teams, nil
}

func (c *PlatformClient) GetTeam(ctx context.Context, slug string) (Team, error) {
	rel := "/runtime/teams/" + url.PathEscape(strings.TrimSpace(slug))
	resp, err := c.do(ctx, http.MethodGet, rel, "", nil)
	if err != nil {
		return Team{}, err
	}
	defer resp.Body.Close()
	b, err := readBody(resp.Body)
	if err != nil {
		return Team{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Team{}, httpAPIError(resp.StatusCode, b)
	}
	var out teamResponse
	if err := json.Unmarshal(b, &out); err != nil {
		return Team{}, err
	}
	return out.Team, nil
}

func (c *PlatformClient) CreateTeam(ctx context.Context, slug, name string) (Team, error) {
	payload := map[string]string{
		"slug": strings.TrimSpace(slug),
		"name": strings.TrimSpace(name),
	}
	js, err := json.Marshal(payload)
	if err != nil {
		return Team{}, err
	}
	resp, err := c.do(ctx, http.MethodPost, "/runtime/teams", "", bytes.NewReader(js))
	if err != nil {
		return Team{}, err
	}
	defer resp.Body.Close()
	b, err := readBody(resp.Body)
	if err != nil {
		return Team{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Team{}, httpAPIError(resp.StatusCode, b)
	}
	var out teamResponse
	if err := json.Unmarshal(b, &out); err != nil {
		return Team{}, err
	}
	return out.Team, nil
}

func (c *PlatformClient) CreateUser(ctx context.Context, email, password, role string) (PlatformUser, error) {
	payload := map[string]string{
		"email":    strings.TrimSpace(email),
		"password": password,
		"role":     strings.TrimSpace(role),
	}
	js, err := json.Marshal(payload)
	if err != nil {
		return PlatformUser{}, err
	}
	resp, err := c.do(ctx, http.MethodPost, "/users", "", bytes.NewReader(js))
	if err != nil {
		return PlatformUser{}, err
	}
	defer resp.Body.Close()
	b, err := readBody(resp.Body)
	if err != nil {
		return PlatformUser{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return PlatformUser{}, httpAPIError(resp.StatusCode, b)
	}
	var out userResponse
	if err := json.Unmarshal(b, &out); err != nil {
		return PlatformUser{}, err
	}
	return out.User, nil
}

func (c *PlatformClient) ListTeamMembers(ctx context.Context, slug string) ([]TeamMembership, error) {
	rel := "/runtime/teams/" + url.PathEscape(strings.TrimSpace(slug)) + "/members"
	resp, err := c.do(ctx, http.MethodGet, rel, "", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := readBody(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, httpAPIError(resp.StatusCode, b)
	}
	var out teamMembersResponse
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out.Members, nil
}

func (c *PlatformClient) CreateTeamUser(ctx context.Context, slug, email, password, role string) (TeamMembership, error) {
	user, err := c.CreateUser(ctx, email, password, "")
	if err != nil {
		return TeamMembership{}, err
	}
	membership, err := c.UpsertTeamMember(ctx, slug, user.ID, role)
	if err != nil {
		return TeamMembership{}, err
	}
	membership.Email = user.Email
	return membership, nil
}

func (c *PlatformClient) UpsertTeamMember(ctx context.Context, slug, userID, role string) (TeamMembership, error) {
	payload := map[string]string{
		"role": strings.TrimSpace(role),
	}
	js, err := json.Marshal(payload)
	if err != nil {
		return TeamMembership{}, err
	}
	rel := "/runtime/teams/" + url.PathEscape(strings.TrimSpace(slug)) + "/members/" + url.PathEscape(strings.TrimSpace(userID))
	resp, err := c.do(ctx, http.MethodPut, rel, "", bytes.NewReader(js))
	if err != nil {
		return TeamMembership{}, err
	}
	defer resp.Body.Close()
	b, err := readBody(resp.Body)
	if err != nil {
		return TeamMembership{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return TeamMembership{}, httpAPIError(resp.StatusCode, b)
	}
	var out teamMembershipResponse
	if err := json.Unmarshal(b, &out); err != nil {
		return TeamMembership{}, err
	}
	return out.Membership, nil
}

func (c *PlatformClient) ListNamespaces(ctx context.Context) ([]namespaceListItem, error) {
	resp, err := c.do(ctx, http.MethodGet, "/runtime/namespaces", "", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := readBody(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, httpAPIError(resp.StatusCode, b)
	}
	var out namespacesResponse
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out.Namespaces, nil
}
