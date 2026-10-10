package platformapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"mcp-runtime/pkg/registryauth"
)

type RegistryPullCredential struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	Password  string    `json:"password"`
	ExpiresAt time.Time `json:"expires_at"`
}

// NewPlatformClientWithAPIKey returns a client for setup-time administration
// that authenticates with a cluster-held service key instead of a CLI login.
// The base URL must be an HTTPS origin without credentials, query, or fragment.
func NewPlatformClientWithAPIKey(baseURL, apiKey string) (*PlatformClient, error) {
	base := NormalizeBaseURL(strings.TrimSpace(baseURL))
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("platform API base URL must be an HTTPS origin")
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("platform API key is required")
	}
	return &PlatformClient{
		baseURL:   base,
		token:     strings.TrimSpace(apiKey),
		http:      &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		apiPrefix: "/api/v1",
	}, nil
}

func (c *PlatformClient) CheckRegistryAdmin(ctx context.Context) error {
	resp, err := c.do(ctx, http.MethodGet, "/registry/pull-credentials", "", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		return fmt.Errorf("registry administration preflight returned HTTP %d; install the registry-auth API and log in as administrator", resp.StatusCode)
	}
	return nil
}

func (c *PlatformClient) CreateRegistryPullCredential(ctx context.Context, scope registryauth.PullScope) (RegistryPullCredential, error) {
	body, err := json.Marshal(scope)
	if err != nil {
		return RegistryPullCredential{}, err
	}
	resp, err := c.do(ctx, http.MethodPost, "/registry/pull-credentials", "", bytes.NewReader(body))
	if err != nil {
		return RegistryPullCredential{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return RegistryPullCredential{}, fmt.Errorf("create scoped registry pull credential: HTTP %d", resp.StatusCode)
	}
	var record RegistryPullCredential
	if err := json.NewDecoder(resp.Body).Decode(&record); err != nil {
		return record, fmt.Errorf("invalid registry credential response")
	}
	if record.ID == "" || record.Username == "" || record.Password == "" {
		return record, fmt.Errorf("incomplete registry credential response")
	}
	return record, nil
}
