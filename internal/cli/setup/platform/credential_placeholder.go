package platform

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// knownPlaceholderCredential is the value shipped in k8s/02-secrets.yaml.example
// and its predecessors. Only this exact value (case-insensitive, trimmed) is
// treated as a placeholder; weak but unknown values are not guessed at.
const knownPlaceholderCredential = "changeme"

// placeholderCheckedKeys lists the secret-bearing credential keys that setup
// may import or preserve from existing objects. Keys holding URLs carry the
// secret as the userinfo password; CSV keys hold several secrets.
var placeholderCheckedKeys = map[string]string{
	"API_KEYS": "csv", "ADMIN_API_KEYS": "csv", "INGEST_API_KEYS": "csv",
	"UI_API_KEY": "", "GRAFANA_ADMIN_PASSWORD": "", "POSTGRES_PASSWORD": "",
	"JWT_SECRET": "", "INTERNAL_AUTH_TOKEN": "", "UI_SESSION_ENCRYPTION_KEY": "",
	"PLATFORM_ADMIN_PASSWORD": "",
	"POSTGRES_DSN":            "url", "UI_SESSION_DATABASE_URL": "url",
}

func isKnownPlaceholder(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), knownPlaceholderCredential)
}

func holdsKnownPlaceholder(key, value string) bool {
	kind, checked := placeholderCheckedKeys[key]
	if !checked {
		return false
	}
	switch kind {
	case "csv":
		for _, part := range strings.Split(value, ",") {
			if isKnownPlaceholder(part) {
				return true
			}
		}
		return false
	case "url":
		parsed, err := url.Parse(strings.TrimSpace(value))
		if err != nil || parsed.User == nil {
			return false
		}
		password, _ := parsed.User.Password()
		return isKnownPlaceholder(password)
	default:
		return isKnownPlaceholder(value)
	}
}

// guardPlaceholderCredentials refuses to import or preserve a known
// placeholder credential outside local test mode. It never rotates the value
// and never includes it in the error; the operator replaces it explicitly.
func guardPlaceholderCredentials(read analyticsSecretValueReader) analyticsSecretValueReader {
	if os.Getenv("MCP_RUNTIME_TEST_MODE") == "1" {
		return read
	}
	return func(namespace, name, key string) (string, error) {
		value, err := read(namespace, name, key)
		if err != nil {
			return "", err
		}
		if holdsKnownPlaceholder(key, value) {
			return "", fmt.Errorf("existing credential %s/%s key %s holds a known placeholder value (value not shown); set a unique secret in that Secret and rerun setup, or use test mode for a local cluster", namespace, name, key)
		}
		return value, nil
	}
}
