package platformauth

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v4"
)

const Issuer = "mcp-runtime"

func Sign(secret []byte, p Principal, ttl time.Duration, audiences []string) (string, error) {
	if len(secret) == 0 {
		return "", errors.New("JWT secret is required")
	}
	if ttl <= 0 {
		return "", errors.New("JWT TTL must be positive")
	}
	now := time.Now().UTC()
	return signUntil(secret, p, now, now.Add(ttl), audiences)
}

func signUntil(secret []byte, p Principal, now, expires time.Time, audiences []string) (string, error) {
	claims := ClaimsFromPrincipal(p)
	claims.RegisteredClaims.Issuer = Issuer
	claims.RegisteredClaims.Audience = jwt.ClaimStrings(append([]string(nil), audiences...))
	claims.RegisteredClaims.IssuedAt = jwt.NewNumericDate(now)
	claims.RegisteredClaims.ExpiresAt = jwt.NewNumericDate(expires)
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
}
