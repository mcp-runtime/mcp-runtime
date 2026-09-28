package adapter

import (
	"context"
	"fmt"
	"strings"
	"time"

	"mcp-runtime/internal/cli/platformapi"
	"mcp-runtime/pkg/certauth"
)

// issuedCredential is a platform-signed, session-bound mTLS credential. The
// certificate's sole SPIFFE URI SAN encodes the issuing MCPAgentSession
// (spiffe://<trustDomain>/ns/<namespace>/session/<name>), so the gateway maps
// the verified cert straight to the session binding — no governance headers
// are involved. It is produced both by `adapter enroll` (written to disk) and
// by automatic certificate enrollment (kept in memory and rotated before expiry).
type issuedCredential struct {
	CertPEM   []byte
	KeyPEM    []byte
	CABundle  []byte
	SPIFFEID  string
	Session   platformapi.AdapterSession
	ExpiresAt time.Time
}

// issueAdapterCredential issues (or reuses) an adapter session for the target
// MCPServer, then signs a session-bound client certificate for it. Each call
// produces a fresh keypair, so rotating callers never reuse private keys.
func issueAdapterCredential(ctx context.Context, client *platformapi.PlatformClient, flags platformSessionFlags, trustDomain string) (issuedCredential, error) {
	trustDomain = strings.TrimSpace(trustDomain)
	session, err := client.CreateAdapterSession(ctx, platformapi.AdapterSessionRequest{
		ServerName: strings.TrimSpace(flags.server),
		Namespace:  strings.TrimSpace(flags.namespace),
		AgentID:    strings.TrimSpace(flags.agent),
	})
	if err != nil {
		return issuedCredential{}, fmt.Errorf("create adapter session: %w", err)
	}
	if trustDomain == "" {
		trustDomain = strings.TrimSpace(session.TrustDomain)
	}
	if trustDomain == "" {
		return issuedCredential{}, fmt.Errorf("platform SPIFFE trust domain is not configured; set MCP_TRUST_DOMAIN")
	}
	if session.TrustDomain != "" && trustDomain != strings.TrimSpace(session.TrustDomain) {
		return issuedCredential{}, fmt.Errorf("configured trust domain %q does not match platform trust domain %q", trustDomain, session.TrustDomain)
	}

	keyPEM, csrPEM, _, err := certauth.BuildSessionCSR(trustDomain, session.Namespace, session.Name)
	if err != nil {
		return issuedCredential{}, err
	}
	issued, err := client.IssueAdapterCertificate(ctx, platformapi.AdapterCertificateRequest{
		Namespace: session.Namespace,
		Session:   session.Name,
		CSR:       string(csrPEM),
	})
	if err != nil {
		return issuedCredential{}, fmt.Errorf("issue adapter certificate: %w", err)
	}
	return issuedCredential{
		CertPEM:   []byte(issued.Certificate),
		KeyPEM:    keyPEM,
		CABundle:  []byte(issued.CABundle),
		SPIFFEID:  issued.SPIFFEID,
		Session:   session,
		ExpiresAt: issued.ExpiresAt,
	}, nil
}
