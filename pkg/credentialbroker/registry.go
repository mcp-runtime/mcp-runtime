package credentialbroker

import (
	"errors"
	"fmt"
)

// ErrProviderNotSupported is returned for provider kinds that are recognised
// but not enabled. Only the Kubernetes Secret provider is supported today.
var ErrProviderNotSupported = errors.New("credential provider is not yet supported; only the kubernetes provider is available")

// ProviderSupported reports whether a provider kind is enabled. It is the one
// gate for source validation and for the reader Registry. To enable another
// provider later: add its kind here, keep its reference validation in
// validateReference, and register its Provider (reader) in the platform API.
func ProviderSupported(kind string) bool { return kind == "kubernetes" }

// knownUnsupportedProvider marks kinds that are planned but not implemented.
func knownUnsupportedProvider(kind string) bool {
	switch kind {
	case "gcp", "aws", "azure", "vault":
		return true
	}
	return false
}

// Registry maps a provider kind to the Provider (reader) the broker resolves
// source references through. Only supported kinds can be registered, and
// Reader never returns a reader for an unsupported kind.
type Registry map[string]Provider

// Register adds a reader for an enabled provider kind.
func (r Registry) Register(kind string, reader Provider) error {
	if reader == nil {
		return errors.New("credential provider reader is required")
	}
	if !ProviderSupported(kind) {
		return fmt.Errorf("register %q: %w", kind, ErrProviderNotSupported)
	}
	r[kind] = reader
	return nil
}

// Reader returns the reader for kind, or nil when none is enabled.
func (r Registry) Reader(kind string) Provider {
	if !ProviderSupported(kind) {
		return nil
	}
	return r[kind]
}
