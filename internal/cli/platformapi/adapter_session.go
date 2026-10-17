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
	"time"
)

// AdapterSessionRequest is the input contract for the platform API endpoint
// POST /api/v1/runtime/adapter/sessions. RequestedTTL/Trust are optional; empty
// values fall back to platform-side defaults.
type AdapterSessionRequest struct {
	ServerName     string `json:"serverName"`
	Namespace      string `json:"namespace,omitempty"`
	AgentID        string `json:"agentID"`
	RequestedTrust string `json:"requestedTrust,omitempty"`
	RequestedTTL   string `json:"requestedTTL,omitempty"`
}

// AdapterSession captures the identity the adapter must inject into runtime
// requests. ExpiresAt is absolute (server-side time); callers should refresh
// before it elapses.
type AdapterSession struct {
	Name           string    `json:"name"`
	Namespace      string    `json:"namespace"`
	HumanID        string    `json:"humanID"`
	AgentID        string    `json:"agentID"`
	TeamID         string    `json:"teamID,omitempty"`
	ServerName     string    `json:"serverName"`
	ConsentedTrust string    `json:"consentedTrust"`
	PolicyVersion  string    `json:"policyVersion"`
	ExpiresAt      time.Time `json:"expiresAt"`
	Reused         bool      `json:"reused"`
	TrustDomain    string    `json:"trustDomain,omitempty"`
}

type AdapterCertificateRequest struct {
	Namespace string `json:"namespace"`
	Session   string `json:"session"`
	CSR       string `json:"csr"`
}

type AdapterCertificate struct {
	Certificate string    `json:"certificate"`
	CABundle    string    `json:"caBundle"`
	SPIFFEID    string    `json:"spiffeID"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

// AdapterPreflightRequest asks whether the selected server is ready for
// session-bound client-certificate enrollment. It has no side effects.
type AdapterPreflightRequest struct {
	ServerName string `json:"serverName"`
	Namespace  string `json:"namespace,omitempty"`
	AgentID    string `json:"agentID"`
}

type AdapterPreflight struct {
	Ready               bool   `json:"ready"`
	AdapterCertificates bool   `json:"adapterCertificatesEnabled"`
	Issuer              string `json:"issuer,omitempty"`
	IssuerReady         bool   `json:"issuerReady"`
	TrustDomain         string `json:"trustDomain,omitempty"`
	RouteEligible       bool   `json:"routeEligible"`
	RouteReady          bool   `json:"routeReady"`
	Reason              string `json:"reason,omitempty"`
}

func (c *PlatformClient) PreflightAdapter(ctx context.Context, req AdapterPreflightRequest) (AdapterPreflight, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return AdapterPreflight{}, fmt.Errorf("marshal request: %w", err)
	}
	resp, err := c.do(ctx, http.MethodPost, "/runtime/adapter/preflight", "", bytes.NewReader(body))
	if err != nil {
		return AdapterPreflight{}, err
	}
	defer resp.Body.Close()
	respBody, err := readBody(io.LimitReader(resp.Body, maxAPIBodyRead))
	if err != nil {
		return AdapterPreflight{}, err
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return AdapterPreflight{}, httpAPIError(resp.StatusCode, respBody)
	}
	var result AdapterPreflight
	if err := json.Unmarshal(respBody, &result); err != nil {
		return AdapterPreflight{}, fmt.Errorf("decode adapter preflight: %w", err)
	}
	return result, nil
}

// CleanupAdapterSession deletes a newly-created, still-pending enrollment
// session after certificate issuance fails. The server verifies ownership and
// refuses to delete reused or completed sessions.
func (c *PlatformClient) CleanupAdapterSession(ctx context.Context, namespace, session string) error {
	path := "/runtime/adapter/sessions/" + url.PathEscape(strings.TrimSpace(namespace)) + "/" + url.PathEscape(strings.TrimSpace(session))
	resp, err := c.do(ctx, http.MethodDelete, path, "", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := readBody(io.LimitReader(resp.Body, maxAPIBodyRead))
	if err != nil {
		return err
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return httpAPIError(resp.StatusCode, respBody)
	}
	return nil
}

// CreateAdapterSession asks the platform to issue (or reuse) an MCPAgentSession
// for the calling principal. The returned session.Name doubles as the
// SessionID the adapter forwards on every runtime request.
func (c *PlatformClient) CreateAdapterSession(ctx context.Context, req AdapterSessionRequest) (AdapterSession, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return AdapterSession{}, fmt.Errorf("marshal request: %w", err)
	}
	resp, err := c.do(ctx, http.MethodPost, "/runtime/adapter/sessions", "", bytes.NewReader(body))
	if err != nil {
		return AdapterSession{}, err
	}
	defer resp.Body.Close()
	respBody, err := readBody(io.LimitReader(resp.Body, maxAPIBodyRead))
	if err != nil {
		return AdapterSession{}, err
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return AdapterSession{}, httpAPIError(resp.StatusCode, respBody)
	}
	var session AdapterSession
	if err := json.Unmarshal(respBody, &session); err != nil {
		return AdapterSession{}, fmt.Errorf("decode response: %w", err)
	}
	return session, nil
}

func (c *PlatformClient) IssueAdapterCertificate(ctx context.Context, req AdapterCertificateRequest) (AdapterCertificate, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return AdapterCertificate{}, fmt.Errorf("marshal request: %w", err)
	}
	resp, err := c.do(ctx, http.MethodPost, "/runtime/adapter/certificates", "", bytes.NewReader(body))
	if err != nil {
		return AdapterCertificate{}, err
	}
	defer resp.Body.Close()
	respBody, err := readBody(io.LimitReader(resp.Body, maxAPIBodyRead))
	if err != nil {
		return AdapterCertificate{}, err
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return AdapterCertificate{}, httpAPIError(resp.StatusCode, respBody)
	}
	var certificate AdapterCertificate
	if err := json.Unmarshal(respBody, &certificate); err != nil {
		return AdapterCertificate{}, fmt.Errorf("decode response: %w", err)
	}
	return certificate, nil
}
