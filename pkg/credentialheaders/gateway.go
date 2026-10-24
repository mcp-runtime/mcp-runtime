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

// ValidateGatewayHeader checks that a header name may be supplied by the
// credential gateway: a valid credential header name that is not one the gateway
// itself owns (Via, Accept-Encoding, Last-Event-Id, Sec-*).
func ValidateGatewayHeader(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	lower := strings.ToLower(name)
	if gatewayOwned[lower] || strings.HasPrefix(lower, "sec-") {
		return fmt.Errorf("credential header %q cannot be supplied by the credential gateway", name)
	}
	return nil
}

// ValidateInjectedValue is the strict check applied to a value the gateway is
// about to send upstream: non-empty, at most 8192 bytes, printable ASCII (tabs
// allowed), and no leading or trailing whitespace. It rejects CR, LF, NUL and
// every other control character, so a submitted value cannot add headers.
func ValidateInjectedValue(value string) error {
	if value == "" || len(value) > 8192 || value != strings.TrimSpace(value) {
		return fmt.Errorf("credential value must be non-empty, at most 8192 bytes, without surrounding whitespace")
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c < 0x20 && c != '\t') || c >= 0x7F {
			return fmt.Errorf("credential value contains a control or non-ASCII character")
		}
	}
	return nil
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
		if err := ValidateGatewayHeader(header); err != nil {
			return err
		}
		known[strings.ToLower(header)] = struct{}{}
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
