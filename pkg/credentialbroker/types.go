// Package credentialbroker defines metadata-only credential management and the
// private gateway resolution contract. Secret values are never management data.
package credentialbroker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	Audience       = "mcp-runtime-credential-broker"
	ResolvePath    = "/api/v1/credential-resolutions"
	ManagementPath = "/api/v1/credentials"
	HeaderPrefix   = "X-Mcp-Credential-"
	MaxValueBytes  = 64 << 10
)

var (
	ErrDenied      = errors.New("credential access denied")
	ErrUnavailable = errors.New("credential provider unavailable")
	ErrConflict    = errors.New("credential revision conflict")
	ErrNotFound    = errors.New("credential metadata not found")
	nameRE         = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	fieldRE        = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
	uuidRE         = regexp.MustCompile(`^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$`)
)

// Source is an administrator-approved exact backing reference. No provider
// token, password, credential payload or arbitrary endpoint belongs here.
type Source struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Provider  string `json:"provider"`
	Resource  string `json:"resource"`
	Key       string `json:"key,omitempty"`
	Version   string `json:"version,omitempty"`
	UserID    string `json:"user_id,omitempty"`
	TeamID    string `json:"team_id,omitempty"`
	Disabled  bool   `json:"disabled,omitempty"`
	Revision  int64  `json:"revision"`
}

// Binding narrows a source to one immutable workload and explicit tools and
// subjects. HumanID and AgentID are mandatory; TeamID binds directory authority.
type Binding struct {
	Namespace string   `json:"namespace"`
	Name      string   `json:"name"`
	Source    string   `json:"source"`
	Server    string   `json:"server"`
	ServerUID string   `json:"server_uid"`
	Tools     []string `json:"tools"`
	HumanID   string   `json:"human_id"`
	AgentID   string   `json:"agent_id"`
	TeamID    string   `json:"team_id"`
	SessionID string   `json:"session_id,omitempty"`
	Header    string   `json:"header"`
	Disabled  bool     `json:"disabled,omitempty"`
	Revision  int64    `json:"revision"`
}

type ResolutionRequest struct {
	Session  string   `json:"session"` // canonical namespace/name from verified adapter identity
	Tool     string   `json:"tool"`
	Bindings []string `json:"bindings"`
}

// Delivery is PRIVATE: only the server-authenticated resolution endpoint may
// serialize it. Values have no String method and must never be logged.
type Delivery struct {
	Headers map[string]string `json:"headers"`
}

type Value struct {
	Bytes   []byte
	Version string
}
type Provider interface {
	Read(context.Context, Source) (Value, error)
}

func NameValid(s string) bool { return nameRE.MatchString(s) }

// LocalUpstream prevents credential-enabled gateways from delivering a secret
// to an arbitrary configured network destination. Plain HTTP is confined to
// the server process in the same Pod's loopback network namespace.
func LocalUpstream(raw string, expectedPort int) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "::1") {
		return false
	}
	port, err := strconv.Atoi(u.Port())
	return err == nil && port > 0 && port <= 65535 && (expectedPort == 0 || port == expectedPort)
}

func (s Source) Validate() error {
	if (s.UserID != "" && !uuidRE.MatchString(s.UserID)) || (s.TeamID != "" && !uuidRE.MatchString(s.TeamID)) {
		return errors.New("source authority requires a stable user or team UUID")
	}
	if !NameValid(s.Namespace) || !NameValid(s.Name) || (s.UserID == "" && s.TeamID == "") || (s.UserID != "" && s.TeamID != "") {
		return errors.New("source requires namespace, name and exactly one authorized user_id or team_id")
	}
	if strings.ContainsAny(s.Resource+s.Key+s.Version, "\r\n\x00") || len(s.Resource) > 1024 || len(s.Version) > 128 {
		return errors.New("invalid provider reference")
	}
	switch s.Provider {
	case "kubernetes":
		if !NameValid(s.Resource) || !fieldRE.MatchString(s.Key) || s.Version != "" {
			return errors.New("a Kubernetes source requires a same-namespace Secret name and key; version must be omitted")
		}
	case "gcp":
		if !regexp.MustCompile(`^projects/[a-zA-Z0-9_-]+/secrets/[a-zA-Z0-9_-]+$`).MatchString(s.Resource) || s.Key != "" || (s.Version != "" && !fieldRE.MatchString(s.Version)) {
			return errors.New("invalid GCP secret reference")
		}
	case "aws":
		if !regexp.MustCompile(`^arn:aws[a-z-]*:secretsmanager:[a-z0-9-]+:[0-9]{12}:secret:[A-Za-z0-9/_+=.@-]+$`).MatchString(s.Resource) || s.Key != "" || (s.Version != "" && !fieldRE.MatchString(s.Version)) {
			return errors.New("AWS source requires an exact Secrets Manager ARN")
		}
	case "azure":
		u, err := url.Parse(s.Resource)
		if err != nil || u.Scheme != "https" || !strings.HasSuffix(u.Hostname(), ".vault.azure.net") || u.Host != u.Hostname() || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !regexp.MustCompile(`^/secrets/[A-Za-z0-9-]+$`).MatchString(u.Path) || s.Key != "" || (s.Version != "" && !fieldRE.MatchString(s.Version)) {
			return errors.New("an Azure source requires an exact HTTPS Key Vault secret URL")
		}
	case "vault":
		if strings.HasPrefix(s.Resource, "/") || strings.Contains(s.Resource, "..") || !regexp.MustCompile(`^[A-Za-z0-9_-]+/[A-Za-z0-9_/-]+$`).MatchString(s.Resource) || !fieldRE.MatchString(s.Key) || (s.Version != "" && !regexp.MustCompile(`^[1-9][0-9]*$`).MatchString(s.Version)) {
			return errors.New("a Vault source requires mount/path, field key and optional numeric KV v2 version")
		}
	default:
		return errors.New("unsupported credential provider")
	}
	return nil
}

func (b Binding) Validate() error {
	if !uuidRE.MatchString(b.HumanID) || !uuidRE.MatchString(b.TeamID) || !regexp.MustCompile(`^agt_[0-9a-hjkmnp-tv-z]{26}$`).MatchString(b.AgentID) || len(b.ServerUID) > 128 {
		return errors.New("binding requires canonical human, team and managed agent identifiers")
	}
	if b.SessionID != "" {
		parts := strings.Split(b.SessionID, "/")
		if len(parts) != 2 || !NameValid(parts[0]) || !NameValid(parts[1]) {
			return errors.New("session_id must be namespace/name")
		}
	}
	if !NameValid(b.Namespace) || !NameValid(b.Name) || !NameValid(b.Source) || !NameValid(b.Server) || b.ServerUID == "" || b.HumanID == "" || b.AgentID == "" || b.TeamID == "" {
		return errors.New("binding requires namespace, name, source, immutable server UID, human, managed agent and team")
	}
	if len(b.Tools) == 0 || len(b.Tools) > 64 {
		return errors.New("binding requires explicit tools")
	}
	seen := map[string]bool{}
	for _, tool := range b.Tools {
		if tool == "" || strings.ContainsAny(tool, "*\r\n\x00") || len(tool) > 256 || seen[tool] {
			return errors.New("binding tools must be unique explicit names")
		}
		seen[tool] = true
	}
	if !strings.HasPrefix(http.CanonicalHeaderKey(b.Header), HeaderPrefix) || len(b.Header) <= len(HeaderPrefix) || !fieldRE.MatchString(strings.TrimPrefix(http.CanonicalHeaderKey(b.Header), HeaderPrefix)) || strings.ContainsAny(b.Header, "_.") {
		return fmt.Errorf("delivery header must use %s followed by a header-safe name", HeaderPrefix)
	}
	return nil
}

func (s Source) Allows(human, team string) bool {
	return !s.Disabled && ((s.UserID != "" && s.UserID == human) || (s.TeamID != "" && s.TeamID == team))
}
func (b Binding) Matches(human, agent, team, session, tool string) bool {
	return !b.Disabled && b.HumanID == human && b.AgentID == agent && b.TeamID == team && (b.SessionID == "" || b.SessionID == session) && slices.Contains(b.Tools, tool)
}
func ValidValue(value []byte) bool {
	if len(value) == 0 || len(value) > MaxValueBytes || !utf8.Valid(value) || value[0] == ' ' || value[len(value)-1] == ' ' {
		return false
	}
	for _, b := range value {
		if b < 0x20 || b == 0x7f {
			return false
		}
	}
	return true
}

func StripHeaders(h http.Header) {
	for name := range h {
		if strings.HasPrefix(strings.ToLower(strings.TrimPrefix(name, http.TrailerPrefix)), strings.ToLower(HeaderPrefix)) {
			delete(h, name)
		}
	}
	// Preserve unrelated declared trailers while removing the reserved names.
	declarations := h.Values("Trailer")
	h.Del("Trailer")
	for _, declaration := range declarations {
		for _, name := range strings.Split(declaration, ",") {
			name = strings.TrimSpace(name)
			if name != "" && !strings.HasPrefix(strings.ToLower(name), strings.ToLower(HeaderPrefix)) {
				h.Add("Trailer", name)
			}
		}
	}
}
