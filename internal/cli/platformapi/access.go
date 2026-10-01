// Access grant and agent session calls against the platform API.

package platformapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	sentinelaccess "mcp-runtime/pkg/access"
)

type grantsListResponse struct {
	Grants []sentinelaccess.GrantSummary `json:"grants"`
}

type sessionsListResponse struct {
	Sessions []sentinelaccess.SessionSummary `json:"sessions"`
}

type grantGetResponse struct {
	Grant sentinelaccess.GrantSummary `json:"grant"`
}

type sessionGetResponse struct {
	Session sentinelaccess.SessionSummary `json:"session"`
}

type grantAPIBody struct {
	Name               string                          `json:"name"`
	Namespace          string                          `json:"namespace"`
	ServerRef          sentinelaccess.ServerReference  `json:"serverRef"`
	Subject            sentinelaccess.SubjectRef       `json:"subject"`
	MaxTrust           sentinelaccess.TrustLevel       `json:"maxTrust"`
	ExpiresAt          *metav1.Time                    `json:"expiresAt,omitempty"`
	AllowedSideEffects []sentinelaccess.ToolSideEffect `json:"allowedSideEffects,omitempty"`
	PolicyVersion      string                          `json:"policyVersion,omitempty"`
	Disabled           *bool                           `json:"disabled,omitempty"`
	ToolRules          []sentinelaccess.ToolRule       `json:"toolRules"`
}

type sessionAPIBody struct {
	Name           string                         `json:"name"`
	Namespace      string                         `json:"namespace"`
	ServerRef      sentinelaccess.ServerReference `json:"serverRef"`
	Subject        sentinelaccess.SubjectRef      `json:"subject"`
	ConsentedTrust sentinelaccess.TrustLevel      `json:"consentedTrust"`
	ExpiresAt      *metav1.Time                   `json:"expiresAt,omitempty"`
	Revoked        *bool                          `json:"revoked,omitempty"`
	PolicyVersion  string                         `json:"policyVersion"`
}

func (c *PlatformClient) ListGrants(ctx context.Context, namespace string) ([]sentinelaccess.GrantSummary, error) {
	resp, err := c.do(ctx, http.MethodGet, "/runtime/grants", listQuery(namespace), nil)
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
	var out grantsListResponse
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out.Grants, nil
}

func (c *PlatformClient) ListSessions(ctx context.Context, namespace string) ([]sentinelaccess.SessionSummary, error) {
	resp, err := c.do(ctx, http.MethodGet, "/runtime/sessions", listQuery(namespace), nil)
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
	var out sessionsListResponse
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out.Sessions, nil
}

func (c *PlatformClient) GetGrant(ctx context.Context, namespace, name string) (sentinelaccess.GrantSummary, error) {
	p := fmt.Sprintf("/runtime/grants/%s/%s", url.PathEscape(namespace), url.PathEscape(name))
	resp, err := c.do(ctx, http.MethodGet, p, "", nil)
	if err != nil {
		return sentinelaccess.GrantSummary{}, err
	}
	defer resp.Body.Close()
	b, err := readBody(resp.Body)
	if err != nil {
		return sentinelaccess.GrantSummary{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return sentinelaccess.GrantSummary{}, httpAPIError(resp.StatusCode, b)
	}
	var out grantGetResponse
	if err := json.Unmarshal(b, &out); err != nil {
		return sentinelaccess.GrantSummary{}, err
	}
	return out.Grant, nil
}

func (c *PlatformClient) GetSession(ctx context.Context, namespace, name string) (sentinelaccess.SessionSummary, error) {
	p := fmt.Sprintf("/runtime/sessions/%s/%s", url.PathEscape(namespace), url.PathEscape(name))
	resp, err := c.do(ctx, http.MethodGet, p, "", nil)
	if err != nil {
		return sentinelaccess.SessionSummary{}, err
	}
	defer resp.Body.Close()
	b, err := readBody(resp.Body)
	if err != nil {
		return sentinelaccess.SessionSummary{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return sentinelaccess.SessionSummary{}, httpAPIError(resp.StatusCode, b)
	}
	var out sessionGetResponse
	if err := json.Unmarshal(b, &out); err != nil {
		return sentinelaccess.SessionSummary{}, err
	}
	return out.Session, nil
}

func (c *PlatformClient) postGrant(ctx context.Context, body grantAPIBody) error {
	js, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, http.MethodPost, "/runtime/grants", "", bytes.NewReader(js))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := readBody(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return httpAPIError(resp.StatusCode, b)
	}
	return nil
}

func (c *PlatformClient) postSession(ctx context.Context, body sessionAPIBody) error {
	js, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, http.MethodPost, "/runtime/sessions", "", bytes.NewReader(js))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := readBody(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return httpAPIError(resp.StatusCode, b)
	}
	return nil
}

func (c *PlatformClient) DeleteGrant(ctx context.Context, namespace, name string) error {
	p := fmt.Sprintf("/runtime/grants/%s/%s", url.PathEscape(namespace), url.PathEscape(name))
	resp, err := c.do(ctx, http.MethodDelete, p, "", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := readBody(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return httpAPIError(resp.StatusCode, b)
	}
	return nil
}

func (c *PlatformClient) DeleteSession(ctx context.Context, namespace, name string) error {
	p := fmt.Sprintf("/runtime/sessions/%s/%s", url.PathEscape(namespace), url.PathEscape(name))
	resp, err := c.do(ctx, http.MethodDelete, p, "", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := readBody(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return httpAPIError(resp.StatusCode, b)
	}
	return nil
}

func (c *PlatformClient) PatchGrant(ctx context.Context, namespace, name string, disabled bool) error {
	body := map[string]any{"disabled": disabled}
	js, err := json.Marshal(body)
	if err != nil {
		return err
	}
	p := fmt.Sprintf("/runtime/grants/%s/%s", url.PathEscape(strings.TrimSpace(namespace)), url.PathEscape(strings.TrimSpace(name)))
	resp, err := c.do(ctx, http.MethodPatch, p, "", bytes.NewReader(js))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := readBody(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return httpAPIError(resp.StatusCode, b)
	}
	return nil
}

func (c *PlatformClient) PatchSession(ctx context.Context, namespace, name string, revoked bool) error {
	body := map[string]any{"revoked": revoked}
	js, err := json.Marshal(body)
	if err != nil {
		return err
	}
	p := fmt.Sprintf("/runtime/sessions/%s/%s", url.PathEscape(strings.TrimSpace(namespace)), url.PathEscape(strings.TrimSpace(name)))
	resp, err := c.do(ctx, http.MethodPatch, p, "", bytes.NewReader(js))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := readBody(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return httpAPIError(resp.StatusCode, b)
	}
	return nil
}

func (c *PlatformClient) ApplyAccessFromYAMLFile(ctx context.Context, path string) error {
	b, err := readFileAtPath(path)
	if err != nil {
		return err
	}
	decoder := k8syaml.NewYAMLOrJSONDecoder(bytes.NewReader(b), 4096)
	docIndex := 0
	for {
		var rawDoc map[string]any
		if err := decoder.Decode(&rawDoc); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return fmt.Errorf("decode %s document %d: %w", path, docIndex+1, err)
		}
		if len(rawDoc) == 0 {
			continue
		}
		docIndex++
		metaBytes, err := json.Marshal(rawDoc)
		if err != nil {
			return fmt.Errorf("encode %s document %d: %w", path, docIndex, err)
		}
		var meta struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(metaBytes, &meta); err != nil {
			return fmt.Errorf("parse %s document %d metadata: %w", path, docIndex, err)
		}
		switch strings.TrimSpace(meta.Kind) {
		case "MCPAccessGrant":
			var g mcpv1alpha1.MCPAccessGrant
			if err := json.Unmarshal(metaBytes, &g); err != nil {
				return fmt.Errorf("parse %s document %d grant: %w", path, docIndex, err)
			}
			if err := c.postGrant(ctx, grantFromV1(&g)); err != nil {
				return fmt.Errorf("apply %s document %d grant: %w", path, docIndex, err)
			}
		case "MCPAgentSession":
			var s mcpv1alpha1.MCPAgentSession
			if err := json.Unmarshal(metaBytes, &s); err != nil {
				return fmt.Errorf("parse %s document %d session: %w", path, docIndex, err)
			}
			if err := c.postSession(ctx, sessionFromV1(&s)); err != nil {
				return fmt.Errorf("apply %s document %d session: %w", path, docIndex, err)
			}
		default:
			return fmt.Errorf("manifest document %d kind %q is not supported for platform apply (use MCPAccessGrant or MCPAgentSession)", docIndex, meta.Kind)
		}
	}
	if docIndex == 0 {
		return errors.New("manifest does not contain MCPAccessGrant or MCPAgentSession")
	}
	return nil
}

func grantFromV1(g *mcpv1alpha1.MCPAccessGrant) grantAPIBody {
	ns := g.Namespace
	if ns == "" {
		ns = sentinelaccess.DefaultMCPResourceNamespace
	}
	trust := sentinelaccess.TrustLevel(g.Spec.MaxTrust)
	allowedSideEffects := make([]sentinelaccess.ToolSideEffect, 0, len(g.Spec.AllowedSideEffects))
	for _, sideEffect := range g.Spec.AllowedSideEffects {
		allowedSideEffects = append(allowedSideEffects, sentinelaccess.ToolSideEffect(sideEffect))
	}
	rules := make([]sentinelaccess.ToolRule, 0, len(g.Spec.ToolRules))
	for _, tr := range g.Spec.ToolRules {
		rules = append(rules, sentinelaccess.ToolRule{
			Name:          tr.Name,
			Decision:      sentinelaccess.PolicyDecision(tr.Decision),
			RequiredTrust: sentinelaccess.TrustLevel(tr.RequiredTrust),
		})
	}
	dis := g.Spec.Disabled
	return grantAPIBody{
		Name:               g.Name,
		Namespace:          ns,
		ServerRef:          sentinelaccess.ServerReference{Name: sentinelaccess.ServerName(g.Spec.ServerRef.Name), Namespace: sentinelaccess.Namespace(g.Spec.ServerRef.Namespace)},
		Subject:            sentinelaccess.SubjectRef{HumanID: sentinelaccess.HumanID(g.Spec.Subject.HumanID), AgentID: sentinelaccess.AgentID(g.Spec.Subject.AgentID), TeamID: sentinelaccess.TeamID(g.Spec.Subject.TeamID)},
		MaxTrust:           trust,
		ExpiresAt:          g.Spec.ExpiresAt,
		AllowedSideEffects: allowedSideEffects,
		PolicyVersion:      g.Spec.PolicyVersion,
		Disabled:           &dis,
		ToolRules:          rules,
	}
}

func sessionFromV1(s *mcpv1alpha1.MCPAgentSession) sessionAPIBody {
	ns := s.Namespace
	if ns == "" {
		ns = sentinelaccess.DefaultMCPResourceNamespace
	}
	rev := s.Spec.Revoked
	return sessionAPIBody{
		Name:           s.Name,
		Namespace:      ns,
		ServerRef:      sentinelaccess.ServerReference{Name: sentinelaccess.ServerName(s.Spec.ServerRef.Name), Namespace: sentinelaccess.Namespace(s.Spec.ServerRef.Namespace)},
		Subject:        sentinelaccess.SubjectRef{HumanID: sentinelaccess.HumanID(s.Spec.Subject.HumanID), AgentID: sentinelaccess.AgentID(s.Spec.Subject.AgentID), TeamID: sentinelaccess.TeamID(s.Spec.Subject.TeamID)},
		ConsentedTrust: sentinelaccess.TrustLevel(s.Spec.ConsentedTrust),
		PolicyVersion:  s.Spec.PolicyVersion,
		Revoked:        &rev,
		ExpiresAt:      s.Spec.ExpiresAt,
	}
}

func readFileAtPath(path string) ([]byte, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve file path: %w", err)
	}

	root, err := os.OpenRoot(filepath.Dir(absPath))
	if err != nil {
		return nil, err
	}
	defer root.Close()

	base := filepath.Base(absPath)
	info, err := root.Stat(base)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("read file %q: not a regular file", path)
	}

	file, err := root.Open(base)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	return io.ReadAll(file)
}
