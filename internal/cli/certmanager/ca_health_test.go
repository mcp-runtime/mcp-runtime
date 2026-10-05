package certmanager

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"

	"mcp-runtime/internal/cli/core"
)

func testCA(t *testing.T, isCA bool, usage x509.KeyUsage, notBefore, notAfter time.Time) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test"},
		NotBefore: notBefore, NotAfter: notAfter, KeyUsage: usage,
		BasicConstraintsValid: true, IsCA: isCA,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

func TestValidateCAKeyPair(t *testing.T) {
	now := time.Now()
	good := x509.KeyUsageCertSign
	okCert, okKey := testCA(t, true, good, now.Add(-time.Hour), now.AddDate(5, 0, 0))
	nearCert, nearKey := testCA(t, true, good, now.Add(-time.Hour), now.AddDate(0, 0, 30))
	expCert, expKey := testCA(t, true, good, now.AddDate(-2, 0, 0), now.AddDate(-1, 0, 0))
	leafCert, leafKey := testCA(t, false, good, now.Add(-time.Hour), now.AddDate(5, 0, 0))
	noUsageCert, noUsageKey := testCA(t, true, x509.KeyUsageDigitalSignature, now.Add(-time.Hour), now.AddDate(5, 0, 0))
	_, otherKey := testCA(t, true, good, now.Add(-time.Hour), now.AddDate(5, 0, 0))

	cases := []struct {
		name string
		cert []byte
		key  []byte
		base error
		near bool
	}{
		{"healthy", okCert, okKey, nil, false},
		{"near expiry reported", nearCert, nearKey, nil, true},
		{"expired", expCert, expKey, core.ErrCAExpired, false},
		{"not a CA", leafCert, leafKey, core.ErrCASecretInvalid, false},
		{"no certSign", noUsageCert, noUsageKey, core.ErrCASecretInvalid, false},
		{"key mismatch", okCert, otherKey, core.ErrCASecretInvalid, false},
		{"missing key", okCert, nil, core.ErrCASecretInvalid, false},
		{"garbage", []byte("x"), []byte("y"), core.ErrCASecretInvalid, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, err := ValidateCAKeyPair(tc.cert, tc.key, now)
			if tc.base != nil {
				if !errors.Is(err, tc.base) {
					t.Fatalf("want %v, got %v", tc.base, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if h.NearExpiry != tc.near {
				t.Fatalf("NearExpiry=%v want %v", h.NearExpiry, tc.near)
			}
		})
	}
}

func TestGeneratedCAPassesValidation(t *testing.T) {
	now := time.Now()
	certPEM, keyPEM, err := generateInternalCAPEM(now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateCAKeyPair(certPEM, keyPEM, now); err != nil {
		t.Fatal(err)
	}
}
