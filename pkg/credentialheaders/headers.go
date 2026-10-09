// Package credentialheaders validates application credential headers without
// letting them replace transport or trusted gateway identity fields.
package credentialheaders

import (
	"fmt"
	"net/http"
	"strings"
)

const MaxNames = 16

// ValidateName accepts HTTP token names, including Authorization, and rejects
// hop-by-hop, framing, routing, and trusted identity headers.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("credential header name is empty")
	}
	for _, c := range name {
		if c > 127 || !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c)) {
			return fmt.Errorf("invalid credential header name")
		}
	}
	lower := strings.ToLower(name)
	if strings.HasPrefix(lower, "x-forwarded-") || strings.HasPrefix(lower, "x-mcp-") || strings.HasPrefix(lower, "mcp-") {
		return fmt.Errorf("reserved credential header name %q", name)
	}
	switch lower {
	case "host", "connection", "keep-alive", "proxy-connection", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade", "content-length", "content-type", "content-encoding", "accept", "cookie", "set-cookie", "forwarded", "x-forwarded-client-cert", "origin", "referer":
		return fmt.Errorf("reserved credential header name %q", name)
	}
	return nil
}

// ValidateValue accepts any configured credential value. The value is opaque.
// It never includes the credential in its error.
func ValidateValue(value string) error {
	if strings.TrimSpace(value) == "" || len(value) > 8192 {
		return fmt.Errorf("credential must be nonempty and at most 8192 bytes")
	}
	for _, c := range value {
		if c < 32 || c == 127 {
			return fmt.Errorf("credential contains a control character")
		}
	}
	return nil
}

// NormalizeNames checks a configured list of credential header names.
func NormalizeNames(names []string) error {
	if len(names) == 0 || len(names) > MaxNames {
		return fmt.Errorf("credential headers require between 1 and %d names", MaxNames)
	}
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if err := ValidateName(name); err != nil {
			return err
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate credential header name %q", name)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// NormalizePresence accepts any, all, or empty (any).
func NormalizePresence(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "any":
		return "any", nil
	case "all":
		return "all", nil
	default:
		return "", fmt.Errorf("credentialPresence must be any or all")
	}
}

// CheckRequest reports whether h satisfies the configured header contract.
// The result is a stable reason code. It is empty when the request may be
// forwarded. Credential values are never included.
func CheckRequest(h http.Header, names []string, presence string) string {
	if err := NormalizeNames(names); err != nil {
		return "invalid_credential_config"
	}
	mode, err := NormalizePresence(presence)
	if err != nil {
		return "invalid_credential_config"
	}
	if h == nil {
		h = http.Header{}
	}
	present := 0
	for _, name := range names {
		var values []string
		for key, vals := range h {
			if strings.EqualFold(key, name) {
				values = append(values, vals...)
			}
		}
		nonempty := make([]string, 0, len(values))
		for _, value := range values {
			if strings.TrimSpace(value) == "" {
				return "empty_credential"
			}
			nonempty = append(nonempty, value)
		}
		if len(nonempty) > 1 {
			return "duplicate_credential"
		}
		if len(nonempty) == 1 {
			present++
		}
	}
	if mode == "all" && present != len(names) || mode == "any" && present == 0 {
		return "missing_credential"
	}
	return ""
}
