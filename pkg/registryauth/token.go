// Package registryauth signs short-lived Distribution registry access tokens.
package registryauth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v4"
)

const Service = "mcp-runtime-registry"
const Issuer = "mcp-runtime-registry-auth"
const Lifetime = 5 * time.Minute

type Access struct {
	Type    string   `json:"type"`
	Name    string   `json:"name"`
	Actions []string `json:"actions"`
}
type Signer struct {
	key  *rsa.PrivateKey
	cert *x509.Certificate
	kid  string
}

func NewSigner(keyPEM, certPEM []byte) (*Signer, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, errors.New("registry signing key PEM is missing")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("invalid registry PKCS8 signing key")
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok || key.N.BitLen() < 2048 {
		return nil, errors.New("registry signing key must be RSA with at least 2048 bits")
	}
	if err := key.Validate(); err != nil {
		return nil, errors.New("invalid registry signing key")
	}
	block, _ = pem.Decode(certPEM)
	if block == nil {
		return nil, errors.New("registry signing certificate PEM is missing")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, errors.New("invalid registry signing certificate")
	}
	pub, ok := cert.PublicKey.(*rsa.PublicKey)
	now := time.Now()
	if !ok || pub.N.Cmp(key.N) != 0 || pub.E != key.E || !cert.IsCA || !cert.BasicConstraintsValid || cert.KeyUsage&x509.KeyUsageCertSign == 0 || now.Before(cert.NotBefore) || !now.Add(24*time.Hour).Before(cert.NotAfter) {
		return nil, errors.New("registry signing certificate/key mismatch, invalid CA, or insufficient lifetime")
	}
	if err := cert.CheckSignatureFrom(cert); err != nil {
		return nil, errors.New("registry signing root must be self-signed")
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(der)
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(hash[:30])
	var groups []string
	for len(encoded) > 0 {
		groups = append(groups, encoded[:4])
		encoded = encoded[4:]
	}
	return &Signer{key: key, cert: cert, kid: strings.Join(groups, ":")}, nil
}

func GenerateMaterial() ([]byte, []byte, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: serial, NotBefore: now.Add(-time.Minute), NotAfter: now.AddDate(1, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	rawKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: rawKey}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

func (s *Signer) Sign(subject string, access []Access) (string, error) {
	now := time.Now()
	if s == nil || !now.Add(Lifetime).Before(s.cert.NotAfter) {
		return "", errors.New("registry signer is unavailable or expired")
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return "", err
	}
	if access == nil {
		access = []Access{}
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"iss": Issuer, "aud": Service, "sub": subject, "iat": now.Unix(), "nbf": now.Add(-30 * time.Second).Unix(), "exp": now.Add(Lifetime).Unix(), "jti": hex.EncodeToString(id), "access": access})
	token.Header["kid"] = s.kid
	token.Header["x5c"] = []string{base64.StdEncoding.EncodeToString(s.cert.Raw)}
	return token.SignedString(s.key)
}

// VerifyAccess accepts only this registry's RS256 tokens and exposes no raw
// parsing errors, which may otherwise include credential material.
func (s *Signer) VerifyAccess(raw string) ([]Access, bool) {
	if s == nil {
		return nil, false
	}
	claims := struct {
		Access []Access `json:"access"`
		jwt.RegisteredClaims
	}{}
	parser := jwt.NewParser(jwt.WithValidMethods([]string{"RS256"}))
	token, err := parser.ParseWithClaims(raw, &claims, func(*jwt.Token) (any, error) { return &s.key.PublicKey, nil })
	if err != nil || !token.Valid || claims.Issuer != Issuer || !claims.VerifyAudience(Service, true) || !claims.VerifyExpiresAt(time.Now(), true) {
		return nil, false
	}
	return claims.Access, true
}
