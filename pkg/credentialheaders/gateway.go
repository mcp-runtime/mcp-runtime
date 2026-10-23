package credentialheaders

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// MaxValidationPath bounds the credential gateway validation path.
const MaxValidationPath = 128

var (
	validationPathRe = regexp.MustCompile(`^/[A-Za-z0-9._~/-]*$`)
)

// gatewayOwned lists headers the bundled gateway never injects, because it
// owns framing, routing and protocol semantics. ValidateName already rejects
// most of them; these are the extra ones, so a push fails here instead of the
// gateway refusing to start.
var gatewayOwned = map[string]bool{"via": true, "accept-encoding": true, "last-event-id": true}

// secretHeaderHints mark a header whose sign-in input is masked when the server
// does not list its secret headers explicitly.
var secretHeaderHints = []string{"token", "password", "secret", "key", "pat", "auth", "credential"}

// GatewayFieldName is the sign-in input name for the credential header at
// position index. Inputs are named by position, so any valid header name works,
// including names that are not valid identifiers; the header name is the label.
func GatewayFieldName(index int) string { return fmt.Sprintf("header%d", index+1) }

func sameHeader(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// IsSecretHeader reports whether the header's sign-in input is masked: listed
// in secretHeaders, or, when none are listed, named like a credential.
func IsSecretHeader(header string, secretHeaders []string) bool {
	if len(secretHeaders) > 0 {
		for _, secret := range secretHeaders {
			if sameHeader(secret, header) {
				return true
			}
		}
		return false
	}
	name := strings.ToLower(header)
	for _, hint := range secretHeaderHints {
		if strings.Contains(name, hint) {
			return true
		}
	}
	return false
}

// ValidateVaultURL accepts an empty value, an https URL, or a onepassword://
// app link, without credentials, whitespace or markup characters. The sign-in
// page links to it; nothing fetches it.
func ValidateVaultURL(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || u.User != nil || strings.ContainsAny(raw, "\r\n\t \"'<>") {
		return fmt.Errorf("credentialGateway.vaultURL must be a plain https URL or onepassword:// link")
	}
	if (u.Scheme == "https" && u.Host != "") || u.Scheme == "onepassword" {
		return nil
	}
	return fmt.Errorf("credentialGateway.vaultURL must be a plain https URL or onepassword:// link")
}

// ValidateGateway checks the settings that opt a header-mode server into the
// optional bundled credential gateway. validationPath is appended to the
// server's public MCP URL; it carries no query or traversal.
func ValidateGateway(validationPath, vaultURL string, secretHeaders, headers []string) error {
	if len(validationPath) > MaxValidationPath || !validationPathRe.MatchString(validationPath) ||
		strings.Contains(validationPath, "//") || strings.Contains(validationPath, "..") {
		return fmt.Errorf("credentialGateway.validationPath must be an absolute path of at most %d characters without query, '//' or '..'", MaxValidationPath)
	}
	if err := ValidateVaultURL(vaultURL); err != nil {
		return err
	}
	known := make(map[string]struct{}, len(headers))
	for _, header := range headers {
		lower := strings.ToLower(header)
		if gatewayOwned[lower] || strings.HasPrefix(lower, "sec-") {
			return fmt.Errorf("credential header %q cannot be supplied by the credential gateway", header)
		}
		known[lower] = struct{}{}
	}
	seen := make(map[string]struct{}, len(secretHeaders))
	for _, secret := range secretHeaders {
		lower := strings.ToLower(strings.TrimSpace(secret))
		if _, ok := known[lower]; !ok {
			return fmt.Errorf("credentialGateway.secretHeaders entry %q is not one of auth.headers", secret)
		}
		if _, dup := seen[lower]; dup {
			return fmt.Errorf("duplicate credentialGateway.secretHeaders entry %q", secret)
		}
		seen[lower] = struct{}{}
	}
	return nil
}
