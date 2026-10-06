package registryauth

import (
	"github.com/golang-jwt/jwt/v4"
	"testing"
	"time"
)

func TestRegistrySignerRejectsWrongAudienceAlgorithmExpiryAndRoot(t *testing.T) {
	key, cert, err := GenerateMaterial()
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewSigner(key, cert)
	if err != nil {
		t.Fatal(err)
	}
	token, err := signer.Sign("node", []Access{{Type: "repository", Name: "acme/app", Actions: []string{"pull"}}})
	if err != nil {
		t.Fatal(err)
	}
	if grants, valid := signer.VerifyAccess(token); !valid || len(grants) != 1 {
		t.Fatal("valid signed access rejected")
	}
	for _, tc := range []struct {
		name   string
		method jwt.SigningMethod
		aud    string
		expiry time.Time
	}{{"audience", jwt.SigningMethodRS256, "other", time.Now().Add(time.Minute)}, {"expired", jwt.SigningMethodRS256, Service, time.Now().Add(-time.Minute)}, {"algorithm", jwt.SigningMethodHS256, Service, time.Now().Add(time.Minute)}} {
		t.Run(tc.name, func(t *testing.T) {
			value := jwt.NewWithClaims(tc.method, jwt.RegisteredClaims{Issuer: Issuer, Audience: jwt.ClaimStrings{tc.aud}, ExpiresAt: jwt.NewNumericDate(tc.expiry)})
			var material any = signer.key
			if tc.method == jwt.SigningMethodHS256 {
				material = []byte("test-key")
			}
			raw, err := value.SignedString(material)
			if err != nil {
				t.Fatal(err)
			}
			if _, valid := signer.VerifyAccess(raw); valid {
				t.Fatal("invalid token accepted")
			}
		})
	}
	_, otherCert, err := GenerateMaterial()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewSigner(key, otherCert); err == nil {
		t.Fatal("mismatched signing root accepted")
	}
}
