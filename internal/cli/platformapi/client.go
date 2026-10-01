// HTTP client for the Sentinel platform API using auth from authfile.
// User-facing (non-kubeconfig) path for access, server list, and policy.

package platformapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"mcp-runtime/pkg/authfile"
)

const maxAPIBodyRead = 4 << 20

// errPlatformNoBaseURL is returned when a token exists but the API base URL is missing.
var errPlatformNoBaseURL = errors.New("set MCP_PLATFORM_API_URL or run mcp-runtime auth login with --api-url to use the platform API")

// PlatformClient calls the mcp-sentinel API with an API key.
type PlatformClient struct {
	baseURL   string
	token     string
	http      *http.Client
	apiPrefix string
}

// NewPlatformClient returns a client when platform credentials and API base URL are configured.
// If the user is not logged in, returns [authfile.ErrNotFound].
func NewPlatformClient() (*PlatformClient, error) {
	tok, base, _, err := authfile.ResolveToken()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(base) == "" {
		if strings.TrimSpace(tok) != "" {
			return nil, errPlatformNoBaseURL
		}
		return nil, authfile.ErrNotFound
	}
	return &PlatformClient{
		baseURL:   NormalizeBaseURL(base),
		token:     tok,
		http:      &http.Client{Timeout: 2 * time.Minute},
		apiPrefix: "/api/v1",
	}, nil
}

func HasPlatformClient() bool {
	_, err := NewPlatformClient()
	return err == nil
}

func (c *PlatformClient) do(ctx context.Context, method, relPath, query string, body io.Reader) (*http.Response, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, err
	}
	rel, err := url.Parse(c.apiPrefix + relPath)
	if err != nil {
		return nil, err
	}
	joined := u.ResolveReference(rel)
	if query != "" {
		joined.RawQuery = query
	}
	req, err := http.NewRequestWithContext(ctx, method, joined.String(), body)
	if err != nil {
		return nil, err
	}
	c.setAuthHeaders(req)
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	return c.http.Do(req)
}

func (c *PlatformClient) setAuthHeaders(req *http.Request) {
	if req == nil {
		return
	}
	req.Header.Set("x-api-key", c.token)
	req.Header.Set("authorization", "Bearer "+c.token)
	req.Header.Set("x-mcp-source", "cli")
}

func listQuery(namespace string) string {
	v := url.Values{}
	if strings.TrimSpace(namespace) != "" {
		v.Set("namespace", namespace)
	}
	return v.Encode()
}

func (c *PlatformClient) ValidateCredentials(ctx context.Context) error {
	resp, err := c.do(ctx, http.MethodGet, "/auth/me", "", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := readBody(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return httpAPIError(resp.StatusCode, b)
	}
	return nil
}

func readBody(r io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, maxAPIBodyRead))
}

func httpAPIError(status int, body []byte) error {
	var m map[string]string
	if err := json.Unmarshal(body, &m); err == nil {
		if message := m["message"]; message != "" {
			return fmt.Errorf("API %d: %s", status, message)
		}
		if e := m["error"]; e != "" {
			return fmt.Errorf("API %d: %s", status, e)
		}
	}
	s := strings.TrimSpace(string(body))
	if s == "" {
		return fmt.Errorf("API returned HTTP %d", status)
	}
	return fmt.Errorf("API %d: %s", status, s)
}

// --- runtime / servers (GET) ------------------------------------------------

type Principal struct {
	Role              string   `json:"role"`
	Subject           string   `json:"subject,omitempty"`
	Email             string   `json:"email,omitempty"`
	Namespace         string   `json:"namespace,omitempty"`
	AllowedNamespaces []string `json:"allowedNamespaces,omitempty"`
	Teams             []Team   `json:"teams,omitempty"`
}

type authMeResponse struct {
	Authenticated bool      `json:"authenticated"`
	Principal     Principal `json:"principal"`
}

func (c *PlatformClient) CurrentPrincipal(ctx context.Context) (Principal, error) {
	resp, err := c.do(ctx, http.MethodGet, "/auth/me", "", nil)
	if err != nil {
		return Principal{}, err
	}
	defer resp.Body.Close()
	b, err := readBody(resp.Body)
	if err != nil {
		return Principal{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Principal{}, httpAPIError(resp.StatusCode, b)
	}
	var out authMeResponse
	if err := json.Unmarshal(b, &out); err != nil {
		return Principal{}, err
	}
	return out.Principal, nil
}

// PlatformAuthRequiredMessage tells users how to use the platform-backed CLI path.
const PlatformAuthRequiredMessage = "platform API credentials are required; run `mcp-runtime auth login --api-url <platform-url>` for normal platform access. `--use-kube` is direct Kubernetes mode for admin/dev/test environments with admin/operator Kubernetes access only"

// AuthRequiredError wraps platform credential errors with user-facing mode guidance.
func AuthRequiredError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", PlatformAuthRequiredMessage, err)
}

// ResolvePlatformOrKube returns direct Kubernetes mode only when useKube is explicit.
// Otherwise it requires platform API credentials and does not fall back to kubeconfig.
func ResolvePlatformOrKube(useKube bool) (*PlatformClient, bool, error) {
	if useKube {
		return nil, true, nil
	}
	cl, e := NewPlatformClient()
	if e == nil {
		return cl, false, nil
	}
	return nil, false, AuthRequiredError(e)
}
