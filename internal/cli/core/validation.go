package core

import (
	"fmt"
	"regexp"
	"strings"
)

// ValidK8sName matches Kubernetes resource name requirements (RFC 1123 subdomain).
var ValidK8sName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// ValidateManifestField rejects control characters, requires non-empty after
// trimming, and returns the trimmed value.
func ValidateManifestField(field, value string) (string, error) {
	if strings.ContainsAny(value, "\r\n\t") {
		return "", NewWithBase(ErrControlCharsNotAllowed, fmt.Sprintf("%s must not contain control characters", field))
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", NewWithBase(ErrFieldRequired, fmt.Sprintf("%s is required", field))
	}
	return value, nil
}

// ValidateK8sNameAndNamespace validates a name+namespace pair against RFC-1123
// subdomain rules plus ValidateManifestField. nameLabel customizes the
// invalid-name error message ("server name", "resource name"); nameBase
// (may be nil) selects the base error category.
func ValidateK8sNameAndNamespace(nameLabel string, nameBase error, name, namespace string) (string, string, error) {
	if !ValidK8sName.MatchString(name) {
		return "", "", NewWithBase(nameBase, fmt.Sprintf("invalid %s %q: must be lowercase alphanumeric with optional hyphens", nameLabel, name))
	}
	var err error
	if name, err = ValidateManifestField("name", name); err != nil {
		return "", "", err
	}
	if namespace, err = ValidateManifestField("namespace", namespace); err != nil {
		return "", "", err
	}
	return name, namespace, nil
}
