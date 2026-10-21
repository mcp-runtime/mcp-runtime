package adapterd

import (
	"fmt"
	"strings"
	"time"
)

const (
	// ProtocolVersion is the management API version shared by the daemon and the Dock app.
	ProtocolVersion = 1
	// LocalTokenHeader is the per-profile loopback credential. It is never forwarded upstream.
	LocalTokenHeader = "X-MCP-Runtime-Local-Token" // #nosec G101 -- HTTP header name; no credential value is embedded.
	// DefaultListenAddr is the single loopback listener shared by every profile path.
	DefaultListenAddr = "127.0.0.1:8099"
)

// Profile is one saved connection to one MCP server.
type Profile struct {
	ID             string    `json:"id"`
	Slug           string    `json:"slug"`
	Server         string    `json:"server"`
	Namespace      string    `json:"namespace,omitempty"`
	Team           string    `json:"team,omitempty"`
	Agent          string    `json:"agent,omitempty"`
	PlatformURL    string    `json:"platformUrl,omitempty"`
	RuntimeURL     string    `json:"runtimeUrl"`
	Human          string    `json:"human,omitempty"`
	Session        string    `json:"session,omitempty"`
	SessionExpires time.Time `json:"sessionExpires,omitempty"`
	TrustDomain    string    `json:"trustDomain,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
	CertExpires    time.Time `json:"certExpires,omitempty"`
	AuthState      string    `json:"authState"`
	LastError      string    `json:"lastError,omitempty"`
	RenewalError   string    `json:"renewalError,omitempty"`
}

const (
	AuthEnrolled       = "enrolled"
	AuthSignInRequired = "sign_in_required"
	AuthRenewalFailed  = "renewal_failed"
	AuthNotEnrolled    = "not_enrolled"
)

// LocalURL is the MCP client URL for this profile on listenAddr.
func (p Profile) LocalURL(listenAddr string) string {
	host := listenAddr
	if host == "" {
		host = DefaultListenAddr
	}
	return "http://" + host + "/" + p.Slug + "/mcp"
}

// SlugFromServer builds a URL path segment from a server name.
func SlugFromServer(server string) (string, error) {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(server)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		case r == '_' || r == '.' || r == ' ':
			b.WriteByte('-')
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" || slug == "mcp" {
		return "", fmt.Errorf("server name %q cannot be used as a local path", server)
	}
	return slug, nil
}

// UniqueSlug returns slug, or slug-2, slug-3, when taken.
func UniqueSlug(base string, taken func(string) bool) string {
	if !taken(base) {
		return base
	}
	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s-%d", base, n)
		if !taken(candidate) {
			return candidate
		}
	}
}
