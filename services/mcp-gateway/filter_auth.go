package main

import (
	"net/http"
	"strings"
	"time"

	"mcp-runtime/pkg/identity"
	policypkg "mcp-runtime/pkg/policy"
)

// authFilter is stage 3 of the gateway pipeline. It extracts the caller
// identity and, when the policy uses OAuth, validates the bearer JWT.
//
// For legacy header-mode policies (local/dev), identity is read from the
// request; no further validation is performed here — authzFilter (stage 4)
// enforces grant and session policy.
//
// For OAuth policies, the JWT is verified against the issuer's JWKS. On any
// authentication failure, authFilter writes a denial response and returns
// Reject; it never falls through to stage 4 with an unauthenticated identity.
//
// authFilter always runs after policyFilter (stage 2) has set Exchange.Policy
// and always completes before authzFilter (stage 4) reads Exchange.Identity.
func (s *gatewayServer) authFilter(ex *Exchange) Result {
	// A presented adapter certificate is a complete adapter authentication
	// method. Its session-bound identity proceeds to authorization; requests
	// without a certificate use the ordinary OAuth flow below.
	if policypkg.PolicyUsesOAuth(ex.Policy) && ex.Policy.Auth != nil && strings.TrimSpace(ex.Policy.Auth.TrustDomain) != "" &&
		strings.TrimSpace(ex.R.Header.Get(s.verifiedSPIFFEHeaderName())) != "" {
		verifiedIdentity, reason := s.authenticateAdapterCertificate(ex.R, ex.Policy)
		if reason != "" {
			ex.Decision = policypkg.Deny(
				http.StatusUnauthorized,
				reason,
				policypkg.ChoosePolicyVersion(policypkg.PolicyVersion(ex.Policy), s.defaultPolicyVersion),
			)
			s.writeDeniedResponse(ex)
			return Reject
		}
		ex.Identity = verifiedIdentity
		return Continue
	}

	if !policypkg.PolicyUsesOAuth(ex.Policy) {
		// Legacy header mode (local/dev only).
		ex.Identity = s.extractIdentity(ex.R, ex.Policy)
		return Continue
	}

	oauthResult := s.authenticateOAuth(ex.R, ex.Policy)
	ex.Identity = oauthResult.Identity
	ex.OAuthToken = oauthResult.Token

	if !oauthResult.Allowed {
		ex.Decision = policypkg.Deny(
			oauthResult.Status,
			oauthResult.Reason,
			policypkg.ChoosePolicyVersion(policypkg.PolicyVersion(ex.Policy), s.defaultPolicyVersion),
		)
		s.writeDeniedResponse(ex)
		return Reject
	}
	return Continue
}

// authenticateAdapterCertificate validates the verified ingress assertion for
// a session-bound adapter certificate. Requests with a verified adapter
// certificate use its session identity; requests without one follow OAuth.
//
// TLS is terminated at the ingress (Traefik), which verifies the caller's
// client certificate against the identity CA and injects the caller's SPIFFE
// identity as a trusted header. The ingress→gateway hop is itself re-encrypted
// with mTLS, so the gateway can prove the request actually came through the
// ingress rather than from a peer that bypassed it. The gateway therefore:
//
//  1. Requires the request to arrive over a verified mTLS hop. A plaintext or
//     unverified connection — e.g. a pod connecting directly to the gateway and
//     forging the identity header — is rejected before the header is ever read.
//     This is the cryptographic half of the trusted-header guarantee; the
//     NetworkPolicy that restricts the gateway to ingress-only traffic is the
//     defense-in-depth half.
//  2. Optionally pins the ingress identity: when trustedProxySPIFFE is set, the
//     verified peer certificate must present exactly that SPIFFE URI SAN. This
//     matters when the identity CA also signs non-ingress certificates (e.g.
//     adapter certs), so a verified chain alone is not proof of "came from the
//     ingress."
//  3. Reads the caller identity from the verified SPIFFE header, validates its
//     trust domain, and maps it to a rendered session binding.
//
// Client-supplied governance headers (human/agent/team/session) are never
// consulted in this mode; identity comes only from the verified SPIFFE header.
func (s *gatewayServer) authenticateAdapterCertificate(r *http.Request, policy *policypkg.Document) (identityContext, string) {
	// (1) The connection must be an ingress-authenticated mTLS hop.
	if r == nil || r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
		return identityContext{}, "missing_client_certificate"
	}
	// (2) Optionally pin the trusted ingress identity.
	if expected := strings.TrimSpace(s.trustedProxySPIFFE); expected != "" {
		if !identity.CertificateHasURI(r.TLS.PeerCertificates[0], expected) {
			return identityContext{}, "untrusted_proxy"
		}
	}

	// (3) Read the caller identity from the ingress-injected verified header.
	rawID := strings.TrimSpace(r.Header.Get(s.verifiedSPIFFEHeaderName()))
	if rawID == "" {
		return identityContext{}, "missing_verified_identity"
	}
	namespace, sessionID, ok := identity.ParseSessionSPIFFE(rawID, policy.Auth.TrustDomain)
	if !ok {
		return identityContext{}, "invalid_spiffe_identity"
	}

	for _, binding := range policy.Sessions {
		if string(binding.Namespace) != namespace || string(binding.Name) != sessionID {
			continue
		}
		if binding.Revoked {
			return identityContext{}, "session_revoked"
		}
		if mtlsBindingExpired(binding.ExpiresAt) {
			return identityContext{}, "session_expired"
		}
		return identityContext{
			HumanID:   string(binding.HumanID),
			AgentID:   string(binding.AgentID),
			TeamID:    string(binding.TeamID),
			SessionID: string(binding.Name),
		}, ""
	}
	return identityContext{}, "session_not_found"
}

// verifiedSPIFFEHeaderName returns the configured trusted-identity header,
// falling back to the default when unset.
func (s *gatewayServer) verifiedSPIFFEHeaderName() string {
	if h := strings.TrimSpace(s.verifiedSPIFFEHeader); h != "" {
		return h
	}
	return defaultVerifiedSPIFFEHeader
}

func mtlsBindingExpired(expiresAt string) bool {
	trimmed := strings.TrimSpace(expiresAt)
	if trimmed == "" {
		return false
	}
	parsed, err := time.Parse(time.RFC3339, trimmed)
	return err == nil && !parsed.After(time.Now())
}
