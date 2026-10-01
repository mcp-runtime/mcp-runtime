// Package certauth provides shared certificate signing request helpers for
// session-bound mTLS credentials issued by the platform API and adapter CLI.
package certauth

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"mcp-runtime/pkg/identity"
)

// BuildSessionCSR generates a fresh P-256 key and a CSR whose only SAN is the
// session SPIFFE URI. It returns PKCS#8 key PEM, CSR PEM, and the SPIFFE ID string.
func BuildSessionCSR(trustDomain, namespace, sessionName string) (keyPEM, csrPEM []byte, spiffeID string, err error) {
	trustDomain = strings.TrimSpace(trustDomain)
	if trustDomain == "" {
		return nil, nil, "", fmt.Errorf("trust domain must not be empty")
	}
	spiffeID = identity.SessionSPIFFEID(trustDomain, namespace, sessionName)
	spiffe, err := url.Parse(spiffeID)
	if err != nil {
		return nil, nil, "", fmt.Errorf("parse session SPIFFE URI: %w", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, "", fmt.Errorf("generate client key: %w", err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		URIs: []*url.URL{spiffe},
	}, key)
	if err != nil {
		return nil, nil, "", fmt.Errorf("create CSR: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, "", fmt.Errorf("marshal client key: %w", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	csrPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
	return keyPEM, csrPEM, spiffeID, nil
}

// ValidateCSRPEM parses and validates a PEM CSR. The CSR must be signed, carry
// exactly one URI SAN equal to expectedSPIFFEID, and must not include DNS,
// email, or IP subject alternative names. It returns the CSR DER on success.
func ValidateCSRPEM(raw, expectedSPIFFEID string) ([]byte, error) {
	block, _ := pem.Decode([]byte(raw))
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, fmt.Errorf("csr must be a PEM CERTIFICATE REQUEST")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse csr: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("csr signature verification failed: %w", err)
	}
	if len(csr.URIs) != 1 || csr.URIs[0].String() != expectedSPIFFEID {
		return nil, fmt.Errorf("csr must contain exactly the SPIFFE URI %q", expectedSPIFFEID)
	}
	if len(csr.DNSNames) != 0 || len(csr.EmailAddresses) != 0 || len(csr.IPAddresses) != 0 {
		return nil, fmt.Errorf("csr may not contain DNS, email, or IP subject alternative names")
	}
	return block.Bytes, nil
}

// ValidateIssuedCertificatePEM verifies a certificate returned by the workload
// issuer against the CSR the platform submitted. It is a defense-in-depth check
// that does not depend on the issuer's approval policy: the leaf must carry
// exactly the expected SPIFFE URI and no other SAN, be a non-CA client-auth-only
// certificate bound to the CSR's public key, and not outlive maxTTL (plus a
// small skew allowance for issuers that backdate NotBefore).
func ValidateIssuedCertificatePEM(certPEM string, csrDER []byte, expectedSPIFFEID string, maxTTL time.Duration, now time.Time) error {
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		return fmt.Errorf("parse csr: %w", err)
	}
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil || block.Type != "CERTIFICATE" {
		return fmt.Errorf("issued certificate is not a PEM CERTIFICATE")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("parse issued certificate: %w", err)
	}
	if len(cert.URIs) != 1 || cert.URIs[0].String() != expectedSPIFFEID {
		return fmt.Errorf("issued certificate does not carry exactly the SPIFFE URI %q", expectedSPIFFEID)
	}
	if len(cert.DNSNames) != 0 || len(cert.EmailAddresses) != 0 || len(cert.IPAddresses) != 0 {
		return fmt.Errorf("issued certificate carries unexpected subject alternative names")
	}
	if cert.IsCA {
		return fmt.Errorf("issued certificate is a CA certificate")
	}
	if len(cert.ExtKeyUsage) != 1 || cert.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
		return fmt.Errorf("issued certificate must be restricted to client authentication")
	}
	if !publicKeysEqual(csr.PublicKey, cert.PublicKey) {
		return fmt.Errorf("issued certificate does not match the submitted key")
	}
	if cert.NotAfter.After(now.Add(maxTTL + certExpirySkew)) {
		return fmt.Errorf("issued certificate outlives the requested duration")
	}
	return nil
}

const certExpirySkew = 5 * time.Minute

func publicKeysEqual(a, b any) bool {
	type equaler interface{ Equal(crypto.PublicKey) bool }
	ea, ok := a.(equaler)
	return ok && ea.Equal(b)
}

// WritePrivateFile writes data to dir/name with mode, rejecting path traversal.
func WritePrivateFile(dir, name string, data []byte, mode os.FileMode) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
