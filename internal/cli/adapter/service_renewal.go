package adapter

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"time"

	"mcp-runtime/internal/adapterservice"
	"mcp-runtime/internal/cli/platformapi"
	"mcp-runtime/pkg/certauth"
)

// Renewal signs a CSR for the same session. It deliberately does not call the
// session-creation endpoint, so a background retry cannot extend user consent.
func renewProfileCertificate(ctx context.Context, p adapterservice.Profile) (adapterservice.Credential, error) {
	if !time.Now().Before(p.SessionExpires) {
		return adapterservice.Credential{}, errors.New("session expired")
	}
	client, err := platformapi.NewCredentialClient(p.PlatformURL, p.PlatformToken)
	if err != nil {
		return adapterservice.Credential{}, err
	}
	key, csr, _, err := certauth.BuildSessionCSR(p.TrustDomain, p.Namespace, p.Session)
	if err != nil {
		return adapterservice.Credential{}, err
	}
	issued, err := client.IssueAdapterCertificate(ctx, platformapi.AdapterCertificateRequest{Namespace: p.Namespace, Session: p.Session, CSR: string(csr)})
	if err != nil {
		return adapterservice.Credential{}, err
	}
	cert, err := tls.X509KeyPair([]byte(issued.Certificate), key)
	if err != nil {
		return adapterservice.Credential{}, err
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return adapterservice.Credential{}, err
	}
	expected := "spiffe://" + p.TrustDomain + "/ns/" + p.Namespace + "/session/" + p.Session
	now := time.Now()
	if issued.SPIFFEID != expected || !issued.ExpiresAt.After(now) || issued.ExpiresAt.After(p.SessionExpires) || leaf.NotAfter.After(issued.ExpiresAt) || leaf.NotAfter.After(p.SessionExpires) || !leaf.NotAfter.After(now) || leaf.NotBefore.After(now) {
		return adapterservice.Credential{}, errors.New("certificate identity or lifetime mismatch")
	}
	block, _ := pem.Decode(csr)
	if block == nil {
		return adapterservice.Credential{}, errors.New("invalid generated certificate request")
	}
	if err := certauth.ValidateIssuedCertificatePEM(issued.Certificate, block.Bytes, expected, p.SessionExpires.Sub(now), now); err != nil {
		return adapterservice.Credential{}, errors.New("issued certificate does not match the authorized session")
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return adapterservice.Credential{}, err
	}
	if !roots.AppendCertsFromPEM([]byte(issued.CABundle)) {
		return adapterservice.Credential{}, errors.New("invalid issued CA bundle")
	}
	return adapterservice.Credential{Certificate: cert, Roots: roots, Expires: leaf.NotAfter}, nil
}
