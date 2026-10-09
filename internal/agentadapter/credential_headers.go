package agentadapter

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"

	"mcp-runtime/pkg/credentialheaders"
)

const (
	AuthModeCertificate = "certificate"
	AuthModeHeader      = "header"
)

// CredentialSource contains a local reference, never a credential value.
// Exactly one source is required. Sources are read for each outbound request.
type CredentialSource struct {
	Env  string `yaml:"env,omitempty" json:"env,omitempty"`
	File string `yaml:"file,omitempty" json:"file,omitempty"`
}

var credentialEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (s CredentialSource) Validate() error {
	if (s.Env == "") == (s.File == "") {
		return fmt.Errorf("credential source requires exactly one of env or file")
	}
	if s.Env != "" && !credentialEnvName.MatchString(s.Env) {
		return fmt.Errorf("invalid credential environment variable name")
	}
	if s.File != "" && strings.TrimSpace(s.File) != s.File {
		return fmt.Errorf("invalid credential file reference")
	}
	return nil
}

func (s CredentialSource) value() (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	value := ""
	if s.Env != "" {
		value = os.Getenv(s.Env)
	} else {
		f, err := os.Open(s.File)
		if err != nil {
			return "", fmt.Errorf("credential source cannot be read")
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, 8195))
		if err != nil || len(data) > 8194 {
			return "", fmt.Errorf("credential source cannot be read or exceeds size limit")
		}
		// A single trailing LF or CRLF is conventional in mounted secret files.
		value = string(data)
		switch {
		case strings.HasSuffix(value, "\r\n"):
			value = strings.TrimSuffix(value, "\r\n")
		case strings.HasSuffix(value, "\n"):
			value = strings.TrimSuffix(value, "\n")
		}
	}
	if err := credentialheaders.ValidateValue(value); err != nil {
		return "", err
	}
	return value, nil
}

// ValidateCredentialSources checks names and references without resolving
// secrets. Credential resolution also runs at startup and on each request.
func ValidateCredentialSources(sources map[string]CredentialSource) error {
	seen := make(map[string]bool, len(sources))
	for _, name := range sortedCredentialNames(sources) {
		if err := credentialheaders.ValidateName(name); err != nil {
			return err
		}
		key := strings.ToLower(name)
		if seen[key] {
			return fmt.Errorf("duplicate credential header name %q", name)
		}
		seen[key] = true
		if err := sources[name].Validate(); err != nil {
			return fmt.Errorf("credential header %q: %w", name, err)
		}
	}
	return nil
}

func sortedCredentialNames(sources map[string]CredentialSource) []string {
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// prepareCredentialHeaders clones before injection, rejects conflicting caller
// credentials, and pins every injection to the configured server route.
func (t *RuntimeTransport) prepareCredentialHeaders(req *http.Request) (*http.Request, error) {
	if t.CredentialTarget == nil && len(t.CredentialHeaders) == 0 {
		return req, nil
	}
	if !credentialTargetMatches(t.CredentialTarget, req) {
		return nil, fmt.Errorf("credential destination does not match configured runtime route")
	}
	if err := ValidateCredentialSources(t.CredentialHeaders); err != nil {
		return nil, err
	}
	cloned := req.Clone(req.Context())
	for _, name := range sortedCredentialNames(t.CredentialHeaders) {
		value, err := t.CredentialHeaders[name].value()
		if err != nil {
			return nil, fmt.Errorf("credential header %q: %w", name, err)
		}
		var existing []string
		for key, values := range cloned.Header {
			if strings.EqualFold(key, name) {
				existing = append(existing, values...)
			}
		}
		if len(existing) > 1 || len(existing) == 1 && existing[0] != value {
			return nil, fmt.Errorf("conflicting client credential for header %q", name)
		}
		for key := range cloned.Header {
			if strings.EqualFold(key, name) {
				delete(cloned.Header, key)
			}
		}
		cloned.Header.Set(name, value)
	}
	return cloned, nil
}

func credentialTargetMatches(target *url.URL, req *http.Request) bool {
	return target != nil && target.Scheme == "https" && req != nil && req.URL != nil &&
		req.URL.Scheme == target.Scheme && strings.EqualFold(req.URL.Host, target.Host) &&
		req.URL.EscapedPath() == target.EscapedPath() &&
		(req.Host == "" || strings.EqualFold(req.Host, target.Host))
}

// withCredentialHeaders copies configuration, not runtime synchronization state.
func (t *RuntimeTransport) withCredentialHeaders(target *url.URL, sources map[string]CredentialSource) *RuntimeTransport {
	out := &RuntimeTransport{
		Base: t.Base, Timeout: t.Timeout, AuthHeader: t.AuthHeader,
		Tracer: t.Tracer, Meter: t.Meter, CredentialTarget: cloneURL(target),
		CredentialHeaders: make(map[string]CredentialSource, len(sources)),
	}
	for name, source := range sources {
		out.CredentialHeaders[name] = source
	}
	return out
}
