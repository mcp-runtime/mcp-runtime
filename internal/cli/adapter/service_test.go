//go:build darwin || linux

package adapter

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"mcp-runtime/internal/adapterservice"
	"mcp-runtime/internal/cli/platformapi"
	"mcp-runtime/pkg/certauth"
)

type serverMetadataStub struct {
	mode        string
	err         error
	wrongServer bool
	called      bool
}

func (s *serverMetadataStub) GetRuntimeServer(_ context.Context, namespace, name string) (platformapi.ServerListItem, error) {
	s.called = true
	if s.wrongServer {
		name = "other"
	}
	return platformapi.ServerListItem{Name: name, Namespace: namespace, AuthMode: s.mode}, s.err
}

func TestProfileOAuthRequirement(t *testing.T) {
	for _, tc := range []struct {
		name, mode, namespace string
		err                   error
		wrongServer           bool
		want                  *bool
	}{
		{name: "OAuth", mode: "oauth", namespace: "team-a", want: ptrBool(true)},
		{name: "certificate only", namespace: "team-a", want: ptrBool(false)},
		{name: "header auth", mode: "header", namespace: "team-a", want: ptrBool(false)},
		{name: "future mode is unknown", mode: "future", namespace: "team-a"},
		{name: "denied lookup is unknown", namespace: "team-a", err: errors.New("forbidden")},
		{name: "unresolved namespace is unknown"},
		{name: "different server is unknown", mode: "oauth", namespace: "team-a", wrongServer: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &serverMetadataStub{mode: tc.mode, err: tc.err, wrongServer: tc.wrongServer}
			got := profileOAuthRequirement(context.Background(), client, tc.namespace, "demo")
			if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
				t.Fatalf("OAuth requirement got %v, want %v", got, tc.want)
			}
			if tc.namespace == "" && client.called {
				t.Fatal("queried an unresolved namespace")
			}
		})
	}
}

func ptrBool(v bool) *bool { return &v }

func TestServiceRenewalCertificateLifetime(t *testing.T) {
	for _, tc := range []struct {
		name                             string
		offset, notBefore, reportedExtra time.Duration
		usage                            x509.ExtKeyUsage
		wantError                        bool
	}{
		{name: "valid shorter leaf", offset: -30 * time.Second, notBefore: -time.Minute, usage: x509.ExtKeyUsageClientAuth},
		{name: "exceeds consent", offset: 30 * time.Second, notBefore: -time.Minute, usage: x509.ExtKeyUsageClientAuth, wantError: true},
		{name: "expired leaf", offset: -2 * time.Hour, notBefore: -3 * time.Hour, usage: x509.ExtKeyUsageClientAuth, wantError: true},
		{name: "future leaf", offset: -30 * time.Second, notBefore: 2 * time.Minute, usage: x509.ExtKeyUsageClientAuth, wantError: true},
		{name: "server auth leaf", offset: -30 * time.Second, notBefore: -time.Minute, usage: x509.ExtKeyUsageServerAuth, wantError: true},
		{name: "response extends consent", offset: -30 * time.Second, notBefore: -time.Minute, reportedExtra: time.Minute, usage: x509.ExtKeyUsageClientAuth, wantError: true},
		{name: "leaf exceeds updated consent", offset: -30 * time.Second, notBefore: -time.Minute, reportedExtra: -2 * time.Minute, usage: x509.ExtKeyUsageClientAuth, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expiry := time.Now().Add(time.Hour).Truncate(time.Second)
			const expected = "spiffe://example.org/ns/team-a/session/session-1"
			var server *httptest.Server
			server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request platformapi.AdapterCertificateRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Errorf("decode certificate request: %v", err)
					http.Error(w, "invalid request", 400)
					return
				}
				block, _ := pem.Decode([]byte(request.CSR))
				if block == nil {
					t.Error("request contains no CSR")
					http.Error(w, "invalid CSR", 400)
					return
				}
				csr, err := x509.ParseCertificateRequest(block.Bytes)
				if err != nil {
					t.Errorf("parse submitted CSR: %v", err)
					http.Error(w, "invalid CSR", 400)
					return
				}
				parent := server.Certificate()
				leaf := &x509.Certificate{
					SerialNumber: big.NewInt(2), NotBefore: time.Now().Add(tc.notBefore),
					NotAfter: expiry.Add(tc.offset), URIs: csr.URIs,
					KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{tc.usage},
				}
				der, err := x509.CreateCertificate(rand.Reader, leaf, parent, csr.PublicKey, server.TLS.Certificates[0].PrivateKey)
				if err != nil {
					t.Errorf("sign fixture: %v", err)
					http.Error(w, "sign failed", 500)
					return
				}
				certificate := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
				if !tc.wantError {
					if err := certauth.ValidateIssuedCertificatePEM(certificate, block.Bytes, expected, time.Until(expiry), time.Now()); err != nil {
						t.Errorf("backend rejects valid fixture: %v", err)
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(platformapi.AdapterCertificate{
					Certificate: certificate, CABundle: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: parent.Raw})),
					SPIFFEID: expected, ExpiresAt: expiry.Add(tc.reportedExtra),
				})
			}))
			defer server.Close()
			original := http.DefaultTransport
			http.DefaultTransport = server.Client().Transport
			defer func() { http.DefaultTransport = original }()
			credential, err := renewProfileCertificate(context.Background(), adapterservice.Profile{
				PlatformURL: server.URL, PlatformToken: "test-token", TrustDomain: "example.org",
				Namespace: "team-a", Session: "session-1", SessionExpires: expiry,
			})
			if (err != nil) != tc.wantError {
				t.Fatalf("renewal error=%v, want failure=%v", err, tc.wantError)
			}
			if err == nil && !credential.Expires.Equal(expiry.Add(tc.offset)) {
				t.Fatalf("renewal must track actual leaf expiry: %s", credential.Expires)
			}
		})
	}
}
