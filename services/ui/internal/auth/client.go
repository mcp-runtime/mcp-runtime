// Package auth owns the console's platform-authentication client contract.
package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Principal struct {
	Role     string `json:"role,omitempty"`
	Subject  string `json:"subject,omitempty"`
	Email    string `json:"email,omitempty"`
	AuthType string `json:"auth_type,omitempty"`
}

// Client uses the transport selected by the console bootstrap. All calls retain
// their bounded contexts and platform authentication/expiry semantics.
type Client struct{ HTTP *http.Client }

func (c Client) Session(ctx context.Context, apiUpstream, idToken string) (Principal, string, time.Time, error) {
	p, token, expiresAt, err := c.OIDC(ctx, apiUpstream, idToken)
	if err == nil {
		return p, token, expiresAt, nil
	}
	var statusErr *oidcLoginStatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusServiceUnavailable {
		return Principal{}, "", time.Time{}, err
	}
	p, verifyErr := c.VerifyOIDC(ctx, apiUpstream, idToken)
	if verifyErr != nil {
		return Principal{}, "", time.Time{}, verifyErr
	}
	return p, idToken, idTokenExpiry(idToken), nil
}

func (c Client) Password(ctx context.Context, apiUpstream, email, password string) (Principal, string, time.Time, error) {
	loginURL, err := apiUpstreamURL(apiUpstream, "api", "v1", "auth", "login")
	if err != nil {
		return Principal{}, "", time.Time{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	body, err := json.Marshal(map[string]string{
		"email":    email,
		"password": password,
	})
	if err != nil {
		return Principal{}, "", time.Time{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, loginURL, strings.NewReader(string(body)))
	if err != nil {
		return Principal{}, "", time.Time{}, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-mcp-source", "ui")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Principal{}, "", time.Time{}, err
	}
	defer drainAndClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return Principal{}, "", time.Time{}, fmt.Errorf("password auth failed: status %d", resp.StatusCode)
	}
	var payload struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		User        struct {
			ID        string `json:"id"`
			Email     string `json:"email"`
			Role      string `json:"role"`
			Namespace string `json:"namespace"`
		} `json:"user"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return Principal{}, "", time.Time{}, err
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		return Principal{}, "", time.Time{}, errors.New("missing access token")
	}
	if payload.ExpiresIn <= 0 {
		return Principal{}, "", time.Time{}, errors.New("missing access token expiry")
	}
	role := strings.TrimSpace(payload.User.Role)
	if role == "" {
		role = "user"
	}
	expiresAt := time.Now().UTC().Add(time.Duration(payload.ExpiresIn) * time.Second)
	return Principal{
		Role:     role,
		Subject:  strings.TrimSpace(payload.User.ID),
		Email:    strings.TrimSpace(payload.User.Email),
		AuthType: "platform_jwt",
	}, payload.AccessToken, expiresAt, nil
}

func idTokenExpiry(idToken string) time.Time {
	parts := strings.Split(strings.TrimSpace(idToken), ".")
	if len(parts) < 2 {
		return time.Time{}
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return time.Time{}
	}
	if claims.Exp <= 0 {
		return time.Time{}
	}
	return time.Unix(claims.Exp, 0).UTC()
}

type oidcLoginStatusError struct {
	StatusCode int
}

func (e *oidcLoginStatusError) Error() string {
	return fmt.Sprintf("oidc login failed: status %d", e.StatusCode)
}

func (c Client) OIDC(ctx context.Context, apiUpstream, idToken string) (Principal, string, time.Time, error) {
	oidcURL, err := apiUpstreamURL(apiUpstream, "api", "v1", "auth", "oidc")
	if err != nil {
		return Principal{}, "", time.Time{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	body, err := json.Marshal(map[string]string{"id_token": idToken})
	if err != nil {
		return Principal{}, "", time.Time{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, oidcURL, strings.NewReader(string(body)))
	if err != nil {
		return Principal{}, "", time.Time{}, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-mcp-source", "ui")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Principal{}, "", time.Time{}, err
	}
	defer drainAndClose(resp.Body)

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return Principal{}, "", time.Time{}, &oidcLoginStatusError{StatusCode: resp.StatusCode}
	}

	var payload struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		User        struct {
			ID    string `json:"id"`
			Email string `json:"email"`
			Role  string `json:"role"`
		} `json:"user"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return Principal{}, "", time.Time{}, err
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		return Principal{}, "", time.Time{}, errors.New("missing access token")
	}
	role := strings.TrimSpace(payload.User.Role)
	if role == "" {
		role = "user"
	}
	var expiresAt time.Time
	if payload.ExpiresIn > 0 {
		expiresAt = time.Now().UTC().Add(time.Duration(payload.ExpiresIn) * time.Second)
	}
	return Principal{
		Role:     role,
		Subject:  strings.TrimSpace(payload.User.ID),
		Email:    strings.TrimSpace(payload.User.Email),
		AuthType: "platform_jwt",
	}, strings.TrimSpace(payload.AccessToken), expiresAt, nil
}

func (c Client) VerifyOIDC(ctx context.Context, apiUpstream, idToken string) (Principal, error) {
	meURL, err := apiUpstreamURL(apiUpstream, "api", "v1", "auth", "me")
	if err != nil {
		return Principal{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, meURL, nil)
	if err != nil {
		return Principal{}, err
	}
	req.Header.Set("authorization", "Bearer "+idToken)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Principal{}, err
	}
	defer drainAndClose(resp.Body)

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return Principal{}, fmt.Errorf("auth check failed: status %d", resp.StatusCode)
	}

	var payload struct {
		Authenticated bool      `json:"authenticated"`
		Principal     Principal `json:"principal"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return Principal{}, err
	}
	if !payload.Authenticated {
		return Principal{}, errors.New("not authenticated")
	}
	if strings.TrimSpace(payload.Principal.Role) == "" {
		payload.Principal.Role = "user"
	}
	if payload.Principal.AuthType == "" {
		payload.Principal.AuthType = "oidc_jwt"
	}
	return payload.Principal, nil
}

func apiUpstreamURL(apiUpstream string, parts ...string) (string, error) {
	base := strings.TrimSpace(apiUpstream)
	if base == "" {
		return "", errors.New("api upstream is empty")
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", errors.New("api upstream must include scheme and host")
	}
	return url.JoinPath(base, parts...)
}

func drainAndClose(body io.ReadCloser) {
	if body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, body)
	_ = body.Close()
}
