package platformclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"mcp-runtime/pkg/apihttp"
	"mcp-runtime/pkg/sessionstate"
)

func sessionError(status int, err error) error {
	if status == http.StatusNotFound {
		return sessionstate.ErrNotFound
	}
	if status == http.StatusConflict {
		return sessionstate.ErrConflict
	}
	if status == http.StatusForbidden {
		return sessionstate.ErrInactiveAgent
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("platform session request: status %d", status)
}
func sessionPath(namespace, name string) string {
	return sessionstate.AccessPath + "/" + url.PathEscape(namespace) + "/" + url.PathEscape(name)
}

func (c *Client) PutSession(ctx context.Context, item sessionstate.Session) (sessionstate.Session, error) {
	if !c.Configured() {
		return sessionstate.Session{}, ErrNotConfigured
	}
	var out sessionstate.Session
	status, err := c.requestJSON(ctx, http.MethodPost, sessionstate.AccessPath, item, &out, true)
	if err != nil || status != http.StatusCreated {
		return sessionstate.Session{}, sessionError(status, err)
	}
	return out, nil
}

func (c *Client) GetSession(ctx context.Context, namespace, name string) (sessionstate.Session, bool, error) {
	if !c.Configured() {
		return sessionstate.Session{}, false, ErrNotConfigured
	}
	var out sessionstate.Session
	status, err := c.requestJSON(ctx, http.MethodGet, sessionPath(namespace, name), nil, &out, true)
	if status == http.StatusNotFound {
		return sessionstate.Session{}, false, nil
	}
	if err != nil || status != http.StatusOK {
		return sessionstate.Session{}, false, sessionError(status, err)
	}
	return out, true, nil
}

// ListSessions drains bounded pages. Management callers retain the existing
// list contract; the gateway snapshot uses its separate authenticated audience.
func (c *Client) ListSessions(ctx context.Context, namespace string) ([]sessionstate.Session, error) {
	return c.ListSessionsFiltered(ctx, sessionstate.Filter{Namespace: namespace})
}
func (c *Client) ListSessionsFiltered(ctx context.Context, filter sessionstate.Filter) ([]sessionstate.Session, error) {
	if !c.Configured() {
		return nil, ErrNotConfigured
	}
	items := []sessionstate.Session{}
	for {
		q := url.Values{"namespace": {filter.Namespace}, "server": {filter.ServerName}, "server_uid": {filter.ServerUID}, "agent_id": {filter.AgentID}, "grant_namespace": {filter.GrantNamespace}, "grant_name": {filter.GrantName}, "after": {filter.After}, "limit": {"1000"}}
		var page sessionstate.Page
		status, err := c.requestJSON(ctx, http.MethodGet, sessionstate.AccessPath+"?"+q.Encode(), nil, &page, true)
		if err != nil || status != http.StatusOK {
			return nil, sessionError(status, err)
		}
		items = append(items, page.Sessions...)
		if page.NextCursor == "" {
			return items, nil
		}
		if page.NextCursor <= filter.After {
			return nil, apihttp.Internal("invalid session pagination cursor")
		}
		filter.After = page.NextCursor
	}
}
func (c *Client) SetSessionRevoked(ctx context.Context, namespace, name string, revoked bool, cause string) error {
	if !c.Configured() {
		return ErrNotConfigured
	}
	status, err := c.requestJSON(ctx, http.MethodPatch, sessionPath(namespace, name), sessionstate.RevokeRequest{Revoked: revoked, Cause: cause}, nil, true)
	if err != nil || status != http.StatusNoContent {
		return sessionError(status, err)
	}
	return nil
}
func (c *Client) RevokeSession(ctx context.Context, namespace, name, cause string) error {
	return c.SetSessionRevoked(ctx, namespace, name, true, cause)
}
func (c *Client) DeleteSession(ctx context.Context, namespace, name string) error {
	if !c.Configured() {
		return ErrNotConfigured
	}
	status, err := c.requestJSON(ctx, http.MethodDelete, sessionPath(namespace, name), nil, nil, true)
	if err != nil || status != http.StatusNoContent {
		return sessionError(status, err)
	}
	return nil
}
func (c *Client) RevokeSessions(ctx context.Context, filter sessionstate.Filter, cause string) (int64, error) {
	if !c.Configured() {
		return 0, ErrNotConfigured
	}
	var result sessionstate.BulkRevokeResponse
	status, err := c.requestJSON(ctx, http.MethodPost, sessionstate.AccessPath+"/revoke", sessionstate.BulkRevokeRequest{Filter: filter, Cause: cause}, &result, true)
	if err != nil || status != http.StatusOK {
		return 0, sessionError(status, err)
	}
	return result.Count, nil
}

// IsSessionNotFound accepts the shared sentinel and typed HTTP errors.
func IsSessionNotFound(err error) bool { return errors.Is(err, sessionstate.ErrNotFound) }
