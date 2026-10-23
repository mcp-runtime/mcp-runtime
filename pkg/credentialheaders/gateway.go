package credentialheaders

import (
	"fmt"
	"regexp"
	"strings"
)

// MaxValidationPath bounds the credential gateway validation path.
const MaxValidationPath = 128

var (
	validationPathRe = regexp.MustCompile(`^/[A-Za-z0-9._~/-]*$`)
	gatewayFieldRe   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
)

// secretHeaderHints mark a header whose sign-in input is masked when the server
// does not list its secret headers explicitly.
var secretHeaderHints = []string{"token", "password", "secret", "key", "pat", "auth", "credential"}

// GatewayFieldName is the sign-in input name for a credential header.
func GatewayFieldName(header string) string { return strings.ToLower(strings.TrimSpace(header)) }

// IsSecretHeader reports whether the header's sign-in input is masked: listed
// in secretHeaders, or, when none are listed, named like a credential.
func IsSecretHeader(header string, secretHeaders []string) bool {
	name := GatewayFieldName(header)
	if len(secretHeaders) > 0 {
		for _, secret := range secretHeaders {
			if GatewayFieldName(secret) == name {
				return true
			}
		}
		return false
	}
	for _, hint := range secretHeaderHints {
		if strings.Contains(name, hint) {
			return true
		}
	}
	return false
}

// ValidateGateway checks the settings that opt a header-mode server into the
// optional bundled credential gateway. validationPath is appended to the
// server's public MCP URL; it carries no query or traversal.
func ValidateGateway(validationPath string, secretHeaders, headers []string) error {
	if len(validationPath) > MaxValidationPath || !validationPathRe.MatchString(validationPath) ||
		strings.Contains(validationPath, "//") || strings.Contains(validationPath, "..") {
		return fmt.Errorf("credentialGateway.validationPath must be an absolute path of at most %d characters without query, '//' or '..'", MaxValidationPath)
	}
	known := make(map[string]struct{}, len(headers))
	for _, header := range headers {
		field := GatewayFieldName(header)
		if !gatewayFieldRe.MatchString(field) {
			return fmt.Errorf("credential header %q cannot be a sign-in input: it must start with a letter and use letters, digits and '-'", header)
		}
		known[field] = struct{}{}
	}
	seen := make(map[string]struct{}, len(secretHeaders))
	for _, secret := range secretHeaders {
		field := GatewayFieldName(secret)
		if _, ok := known[field]; !ok {
			return fmt.Errorf("credentialGateway.secretHeaders entry %q is not one of auth.headers", secret)
		}
		if _, dup := seen[field]; dup {
			return fmt.Errorf("duplicate credentialGateway.secretHeaders entry %q", secret)
		}
		seen[field] = struct{}{}
	}
	return nil
}
