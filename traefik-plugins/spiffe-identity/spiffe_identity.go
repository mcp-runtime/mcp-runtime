// Package spiffe_identity is a Traefik local (Yaegi) middleware that turns a
// verified client certificate into a trusted identity header for the MCP
// gateway, and strips configured client-supplied headers before injection.
//
// It runs at the TLS-terminating ingress for optional adapter certificates.
// Because Traefik
// terminates the caller's mTLS, this middleware is the only component that sees
// the caller's certificate; it extracts the SPIFFE URI SAN and injects it as
// the verified-identity header, then re-encrypts onward to the gateway. The
// strip-before-inject ordering is the load-bearing anti-spoofing guarantee:
// a client cannot smuggle its own verified header past this middleware.
package spiffe_identity

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// Config holds the middleware configuration.
type Config struct {
	// VerifiedHeader is the header the gateway reads for the caller identity.
	VerifiedHeader string `json:"verifiedHeader,omitempty"`
	// TrustDomain, when set, restricts which SPIFFE host is accepted from the
	// client certificate. Empty accepts any spiffe:// URI SAN (the gateway
	// still validates the trust domain against policy).
	TrustDomain string `json:"trustDomain,omitempty"`
	// StripHeaders are client-supplied headers deleted on every request before
	// the verified header is injected. The VerifiedHeader is always stripped in
	// addition to this list.
	StripHeaders []string `json:"stripHeaders,omitempty"`
	// RejectInvalidCertificate rejects a presented client certificate without a
	// SPIFFE URI SAN matching TrustDomain. Requests without a certificate pass.
	RejectInvalidCertificate bool `json:"rejectInvalidCertificate,omitempty"`
}

// CreateConfig returns the default plugin configuration.
func CreateConfig() *Config {
	return &Config{
		VerifiedHeader: "X-MCP-Verified-SPIFFE-ID",
	}
}

// New creates the middleware.
func New(_ context.Context, next http.Handler, cfg *Config, _ string) (http.Handler, error) {
	if next == nil {
		return nil, errors.New("next handler is required")
	}
	if cfg == nil {
		cfg = CreateConfig()
	}
	verified := strings.TrimSpace(cfg.VerifiedHeader)
	if verified == "" {
		verified = "X-MCP-Verified-SPIFFE-ID"
	}
	// Always strip the verified header itself so a client cannot pre-set it.
	strip := append([]string{}, cfg.StripHeaders...)
	strip = append(strip, verified)
	return &Middleware{
		next:                     next,
		verified:                 verified,
		trustDomain:              strings.TrimSpace(cfg.TrustDomain),
		stripHeaders:             strip,
		rejectInvalidCertificate: cfg.RejectInvalidCertificate,
	}, nil
}

// Middleware implements http.Handler.
type Middleware struct {
	next                     http.Handler
	verified                 string
	trustDomain              string
	stripHeaders             []string
	rejectInvalidCertificate bool
}

func (m *Middleware) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	// Always remove the internal assertion header. For a verified adapter
	// certificate, also remove any configured headers before injecting the
	// session-bound SPIFFE identity.
	req.Header.Del(m.verified)
	if id := verifiedSPIFFEID(req, m.trustDomain); id != "" {
		for _, h := range m.stripHeaders {
			req.Header.Del(h)
		}
		req.Header.Set(m.verified, id)
	} else if m.rejectInvalidCertificate && req.TLS != nil && len(req.TLS.PeerCertificates) > 0 {
		http.Error(rw, "client certificate has no trusted SPIFFE identity", http.StatusUnauthorized)
		return
	}
	m.next.ServeHTTP(rw, req)
}

// verifiedSPIFFEID returns the spiffe:// URI SAN from the verified client
// certificate, or "" when the connection is not a verified mTLS handshake or
// the certificate carries no matching SPIFFE identity.
func verifiedSPIFFEID(req *http.Request, trustDomain string) string {
	if req == nil || req.TLS == nil || len(req.TLS.VerifiedChains) == 0 || len(req.TLS.PeerCertificates) == 0 {
		return ""
	}
	for _, uri := range req.TLS.PeerCertificates[0].URIs {
		if uri == nil || !strings.EqualFold(uri.Scheme, "spiffe") {
			continue
		}
		if trustDomain != "" && !strings.EqualFold(uri.Host, trustDomain) {
			continue
		}
		return uri.String()
	}
	return ""
}
