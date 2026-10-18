package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	cb "mcp-runtime/pkg/credentialbroker"
)

type credentialBroker struct {
	endpoint string
	token    *tokenFileSource
	client   *http.Client
}

func newCredentialBroker(baseURL, tokenFile, caFile string) (*credentialBroker, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || tokenFile == "" {
		return nil, errors.New("credential broker requires an HTTPS origin and projected token file")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile != "" {
		pem, err := os.ReadFile(caFile) // #nosec G304 G703 -- administrator-configured trust bundle, never request data.
		if err != nil {
			return nil, errors.New("credential broker CA unavailable")
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("credential broker CA invalid")
		}
		transport.TLSClientConfig.RootCAs = roots
	}
	return &credentialBroker{endpoint: strings.TrimSuffix(baseURL, "/") + cb.ResolvePath, token: newTokenFileSource(tokenFile), client: &http.Client{Timeout: 5 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (b *credentialBroker) resolve(ctx context.Context, request cb.ResolutionRequest) (cb.Delivery, error) {
	if b == nil || b.token == nil {
		return cb.Delivery{}, cb.ErrUnavailable
	}
	token, err := b.token.Token()
	if err != nil {
		return cb.Delivery{}, cb.ErrUnavailable
	}
	body, err := json.Marshal(request)
	if err != nil {
		return cb.Delivery{}, cb.ErrUnavailable
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint, bytes.NewReader(body))
	if err != nil {
		return cb.Delivery{}, cb.ErrUnavailable
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(r)
	if err != nil {
		return cb.Delivery{}, cb.ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return cb.Delivery{}, cb.ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (16*cb.MaxValueBytes)+1))
	if err != nil || len(data) > 16*cb.MaxValueBytes {
		return cb.Delivery{}, cb.ErrUnavailable
	}
	var result cb.Delivery
	if json.Unmarshal(data, &result) != nil || len(result.Headers) > 16 {
		return cb.Delivery{}, cb.ErrUnavailable
	}
	for name, value := range result.Headers {
		if !strings.HasPrefix(http.CanonicalHeaderKey(name), cb.HeaderPrefix) || !cb.ValidValue([]byte(value)) {
			return cb.Delivery{}, cb.ErrUnavailable
		}
	}
	return result, nil
}

func (s *gatewayServer) injectCredentials(ex *Exchange) bool {
	cb.StripHeaders(ex.R.Header)
	cb.StripHeaders(ex.R.Trailer)
	if ex.Policy == nil || len(ex.Policy.CredentialBindings) == 0 || !ex.Inspection.ToolCall {
		return true
	}
	if ex.Identity.Session == nil || !ex.Decision.Allowed {
		return false
	}
	if s.upstreamURL == nil || !cb.LocalUpstream(s.upstreamURL.String(), 0) {
		return false
	}
	b := ex.Identity.Session
	result, err := s.credentialBroker.resolve(ex.R.Context(), cb.ResolutionRequest{Session: string(b.Namespace) + "/" + string(b.Name), Tool: ex.Inspection.ToolName, Bindings: ex.Policy.CredentialBindings})
	if err != nil {
		return false
	}
	for name, value := range result.Headers {
		ex.R.Header.Set(name, value)
	}
	return true
}
