package certmanager

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"time"

	"mcp-runtime/internal/cli/core"
)

// MinCARemainingLifetime is the minimum remaining root lifetime accepted for
// the bundled workload CA in production. Below this, operators must plan a
// dual-trust rotation (docs/cli.md, "Bundled workload CA lifecycle") before
// setup will treat the CA as healthy.
const MinCARemainingLifetime = 180 * 24 * time.Hour

// CAHealth describes a validated CA keypair. It never carries key material.
type CAHealth struct {
	Subject   string
	NotAfter  time.Time
	Remaining time.Duration
	// NearExpiry is true when Remaining is below MinCARemainingLifetime.
	NearExpiry bool
}

// ValidateCAKeyPair checks that certPEM/keyPEM form a usable CA: parseable,
// matching, CA-constrained with certSign usage, within its validity window.
// Errors never include key material. Near-expiry is reported, not an error, so
// callers can choose policy (fail in production, warn in test mode).
func ValidateCAKeyPair(certPEM, keyPEM []byte, now time.Time) (CAHealth, error) {
	var health CAHealth
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		return health, core.NewWithSentinel(core.ErrCASecretInvalid, "CA secret is missing tls.crt or tls.key")
	}
	// X509KeyPair verifies the private key matches the certificate public key.
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		return health, core.NewWithSentinel(core.ErrCASecretInvalid, "CA certificate and private key are not a valid matching pair")
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return health, core.NewWithSentinel(core.ErrCASecretInvalid, "tls.crt is not PEM encoded")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return health, core.NewWithSentinel(core.ErrCASecretInvalid, "tls.crt is not a parseable certificate")
	}
	if !cert.IsCA || !cert.BasicConstraintsValid {
		return health, core.NewWithSentinel(core.ErrCASecretInvalid, "CA certificate lacks CA basic constraints (CA:TRUE)")
	}
	if cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		return health, core.NewWithSentinel(core.ErrCASecretInvalid, "CA certificate lacks the keyCertSign key usage")
	}
	if now.Before(cert.NotBefore) {
		return health, core.NewWithSentinel(core.ErrCASecretInvalid, fmt.Sprintf("CA certificate is not valid until %s", cert.NotBefore.UTC().Format(time.RFC3339)))
	}
	if !now.Before(cert.NotAfter) {
		return health, core.NewWithSentinel(core.ErrCAExpired, fmt.Sprintf("CA certificate expired at %s", cert.NotAfter.UTC().Format(time.RFC3339)))
	}
	health = CAHealth{
		Subject:   cert.Subject.String(),
		NotAfter:  cert.NotAfter,
		Remaining: cert.NotAfter.Sub(now),
	}
	health.NearExpiry = health.Remaining < MinCARemainingLifetime
	return health, nil
}
