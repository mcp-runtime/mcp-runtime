package certauth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestBuildSessionCSR(t *testing.T) {
	t.Parallel()
	keyPEM, csrPEM, spiffeID, err := BuildSessionCSR("mcpruntime.org", "mcp-team-acme", "adapter-xyz")
	if err != nil {
		t.Fatalf("BuildSessionCSR: %v", err)
	}
	want := "spiffe://mcpruntime.org/ns/mcp-team-acme/session/adapter-xyz"
	if spiffeID != want {
		t.Fatalf("spiffeID = %q, want %q", spiffeID, want)
	}
	if _, err := ValidateCSRPEM(string(csrPEM), want); err != nil {
		t.Fatalf("ValidateCSRPEM() error = %v", err)
	}
	if block, _ := pem.Decode(keyPEM); block == nil || block.Type != "PRIVATE KEY" {
		t.Fatal("key PEM is not a PRIVATE KEY block")
	}
}

func TestValidateCSRPEM(t *testing.T) {
	t.Parallel()
	const expected = "spiffe://example.org/ns/team-a/session/session-1"
	_, csrPEM, _, err := BuildSessionCSR("example.org", "team-a", "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateCSRPEM(string(csrPEM), expected); err != nil {
		t.Fatalf("ValidateCSRPEM() error = %v", err)
	}
	if _, err := ValidateCSRPEM(string(csrPEM), strings.Replace(expected, "session-1", "session-2", 1)); err == nil {
		t.Fatal("ValidateCSRPEM() accepted a CSR for another session")
	}
}

func TestValidateCSRPEMRejectsAdditionalSANs(t *testing.T) {
	t.Parallel()
	const expected = "spiffe://example.org/ns/team-a/session/session-1"
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	uri, err := url.Parse(expected)
	if err != nil {
		t.Fatal(err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		URIs:     []*url.URL{uri},
		DNSNames: []string{"attacker.example"},
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
	if _, err := ValidateCSRPEM(string(csrPEM), expected); err == nil {
		t.Fatal("ValidateCSRPEM() accepted an additional DNS SAN")
	}
}

func TestWritePrivateFileRejectsTraversal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := WritePrivateFile(dir, "client.key", []byte("secret"), 0o600); err != nil {
		t.Fatalf("WritePrivateFile() error = %v", err)
	}
	if err := WritePrivateFile(dir, "../escape", []byte("nope"), 0o600); err == nil {
		t.Fatal("WritePrivateFile() allowed path traversal")
	}
}

func issueTestCert(t *testing.T, csrDER []byte, mutate func(*x509.Certificate)) string {
	t.Helper()
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		t.Fatal(err)
	}
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ca"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(2), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		URIs: csr.URIs, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	if mutate != nil {
		mutate(leaf)
	}
	der, err := x509.CreateCertificate(rand.Reader, leaf, caTmpl, csr.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestValidateIssuedCertificatePEM(t *testing.T) {
	_, csrPEM, spiffe, err := BuildSessionCSR("example.org", "team-a", "s1")
	if err != nil {
		t.Fatal(err)
	}
	csrDER, err := ValidateCSRPEM(string(csrPEM), spiffe)
	if err != nil {
		t.Fatal(err)
	}
	forged, _ := url.Parse("spiffe://example.org/ns/traefik/sa/traefik")
	cases := map[string]struct {
		mutate  func(*x509.Certificate)
		wantErr bool
	}{
		"exact":          {},
		"forged uri":     {func(c *x509.Certificate) { c.URIs = []*url.URL{forged} }, true},
		"extra uri":      {func(c *x509.Certificate) { c.URIs = append(c.URIs, forged) }, true},
		"dns san":        {func(c *x509.Certificate) { c.DNSNames = []string{"x.example.org"} }, true},
		"server auth":    {func(c *x509.Certificate) { c.ExtKeyUsage = append(c.ExtKeyUsage, x509.ExtKeyUsageServerAuth) }, true},
		"ca":             {func(c *x509.Certificate) { c.IsCA = true; c.BasicConstraintsValid = true }, true},
		"too long lived": {func(c *x509.Certificate) { c.NotAfter = time.Now().Add(48 * time.Hour) }, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			certPEM := issueTestCert(t, csrDER, tc.mutate)
			err := ValidateIssuedCertificatePEM(certPEM, csrDER, spiffe, time.Hour, time.Now())
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
	t.Run("different key", func(t *testing.T) {
		_, otherCSR, _, _ := BuildSessionCSR("example.org", "team-a", "s1")
		otherDER, _ := ValidateCSRPEM(string(otherCSR), spiffe)
		certPEM := issueTestCert(t, otherDER, nil)
		if err := ValidateIssuedCertificatePEM(certPEM, csrDER, spiffe, time.Hour, time.Now()); err == nil {
			t.Fatal("accepted certificate for a different key")
		}
	})
}
