package adapter

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mcp-runtime/internal/agentadapter"
	"mcp-runtime/internal/cli/platformapi"
	"mcp-runtime/pkg/certauth"
)

// fakeMTLSServer extends the platform session endpoint with a CSR-signing
// certificates endpoint backed by an in-test CA, so setupMTLS can drive a full
// enroll cycle. certCalls counts certificate issuances.
func fakeMTLSServer(t *testing.T, expiresAt time.Time, certCalls *int32) (*httptest.Server, *platformapi.PlatformClient) {
	t.Helper()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             expiresAt.Add(-time.Hour),
		NotAfter:              expiresAt.Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA cert: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})

	var serial int64 = 2
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/runtime/adapter/sessions":
			var req platformapi.AdapterSessionRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			_ = json.NewEncoder(w).Encode(platformapi.AdapterSession{
				Name:        "adapter-fake",
				Namespace:   "mcp-team-acme",
				HumanID:     "user-123",
				AgentID:     req.AgentID,
				TeamID:      "team-acme",
				ServerName:  req.ServerName,
				TrustDomain: "mcpruntime.org",
				ExpiresAt:   expiresAt,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/runtime/adapter/certificates":
			atomic.AddInt32(certCalls, 1)
			var req platformapi.AdapterCertificateRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			block, _ := pem.Decode([]byte(req.CSR))
			if block == nil {
				http.Error(w, "bad csr", http.StatusBadRequest)
				return
			}
			csr, err := x509.ParseCertificateRequest(block.Bytes)
			if err != nil {
				http.Error(w, "parse csr", http.StatusBadRequest)
				return
			}
			leaf := &x509.Certificate{
				SerialNumber: big.NewInt(serial),
				NotBefore:    expiresAt.Add(-time.Hour),
				NotAfter:     expiresAt,
				URIs:         csr.URIs,
				KeyUsage:     x509.KeyUsageDigitalSignature,
				ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
			}
			serial++
			leafDER, err := x509.CreateCertificate(rand.Reader, leaf, caCert, csr.PublicKey, caKey)
			if err != nil {
				http.Error(w, "sign", http.StatusInternalServerError)
				return
			}
			spiffe := ""
			if len(csr.URIs) == 1 {
				spiffe = csr.URIs[0].String()
			}
			_ = json.NewEncoder(w).Encode(platformapi.AdapterCertificate{
				Certificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})),
				CABundle:    string(caPEM),
				SPIFFEID:    spiffe,
				ExpiresAt:   expiresAt,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	t.Setenv("MCP_PLATFORM_API_URL", server.URL)
	t.Setenv("MCP_PLATFORM_API_TOKEN", "test-token")
	client, err := platformapi.NewPlatformClient()
	if err != nil {
		t.Fatalf("NewPlatformClient: %v", err)
	}
	return server, client
}

func TestBuildSessionCSROnlySpiffeSAN(t *testing.T) {
	keyPEM, csrPEM, spiffeID, err := certauth.BuildSessionCSR("mcpruntime.org", "mcp-team-acme", "adapter-xyz")
	if err != nil {
		t.Fatalf("BuildSessionCSR: %v", err)
	}
	if block, _ := pem.Decode(keyPEM); block == nil || block.Type != "PRIVATE KEY" {
		t.Fatal("key PEM is not a PRIVATE KEY block")
	}
	want := "spiffe://mcpruntime.org/ns/mcp-team-acme/session/adapter-xyz"
	if spiffeID != want {
		t.Fatalf("spiffeID = %q, want %q", spiffeID, want)
	}
	if _, err := certauth.ValidateCSRPEM(string(csrPEM), want); err != nil {
		t.Fatalf("ValidateCSRPEM: %v", err)
	}
}

func TestResolveAuthMTLSValidation(t *testing.T) {
	cases := []struct {
		name    string
		idFlags identityFlags
		session platformSessionFlags
		wantErr string
	}{
		{
			name:    "server required",
			idFlags: identityFlags{trustDomain: "mcpruntime.org"},
			session: platformSessionFlags{agent: "ops"},
			wantErr: "--server",
		},
		{
			name:    "agent required",
			idFlags: identityFlags{trustDomain: "mcpruntime.org"},
			session: platformSessionFlags{server: "demo"},
			wantErr: "--agent",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, stop, err := resolveAuth(context.Background(), tc.idFlags, &tc.session, nil, nil)
			if stop != nil {
				stop()
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}

// The trust domain is a platform setting returned with the adapter session,
// so the adapter needs no flag and rejects an override that disagrees.
func TestResolveAuthMTLSUsesPlatformTrustDomain(t *testing.T) {
	var certCalls int32
	fakeMTLSServer(t, time.Now().Add(time.Hour), &certCalls)
	session := platformSessionFlags{server: "demo", agent: "ops-agent"}

	_, stop, err := resolveAuth(context.Background(), identityFlags{}, &session, nil, nil)
	if err != nil {
		t.Fatalf("resolveAuth without --trust-domain: %v", err)
	}
	stop()
	if atomic.LoadInt32(&certCalls) != 1 {
		t.Fatalf("certCalls = %d, want 1 enrollment using the platform trust domain", atomic.LoadInt32(&certCalls))
	}

	_, stop, err = resolveAuth(context.Background(), identityFlags{trustDomain: "other.example"}, &session, nil, nil)
	if stop != nil {
		stop()
	}
	if err == nil || !strings.Contains(err.Error(), "does not match platform trust domain") {
		t.Fatalf("err = %v, want a trust domain mismatch error", err)
	}
}

func TestResolveAuthMTLSEnrollsCertificate(t *testing.T) {
	var certCalls int32
	fakeMTLSServer(t, time.Now().Add(time.Hour), &certCalls)
	idFlags := identityFlags{trustDomain: "mcpruntime.org"}
	session := platformSessionFlags{server: "demo", agent: "ops-agent"}

	transport, stop, err := resolveAuth(context.Background(), idFlags, &session, nil, nil)
	if err != nil {
		t.Fatalf("resolveAuth: %v", err)
	}
	defer stop()

	if transport == nil || transport.Base == nil {
		t.Fatal("mtls transport must carry a TLS-configured base round-tripper")
	}
	if atomic.LoadInt32(&certCalls) != 1 {
		t.Fatalf("certCalls = %d, want 1 enrollment", atomic.LoadInt32(&certCalls))
	}
}

func TestSetupMTLSPreservesServerTrust(t *testing.T) {
	var certCalls int32
	_, client := fakeMTLSServer(t, time.Now().Add(time.Hour), &certCalls)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.TLS.PeerCertificates) == 0 {
			t.Error("adapter did not present its enrolled client certificate")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	server.TLS = &tls.Config{ClientAuth: tls.RequireAnyClientCert}
	server.StartTLS()
	defer server.Close()
	serverRoots := x509.NewCertPool()
	serverRoots.AddCert(server.Certificate())
	originalRoots := serverRoots.Clone()
	base := &agentadapter.RuntimeTransport{
		Base: agentadapter.NewHTTPTransportWithTLS(&tls.Config{RootCAs: serverRoots, MinVersion: tls.VersionTLS12}),
	}
	transport, stop, err := setupMTLS(context.Background(), client,
		platformSessionFlags{server: "demo", agent: "ops-agent"},
		"mcpruntime.org", base, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	httpClient := &http.Client{Transport: transport.Base, Timeout: 5 * time.Second}
	defer httpClient.CloseIdleConnections()
	resp, err := httpClient.Get(server.URL)
	if err != nil {
		t.Fatalf("HTTPS server signed by configured CA: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if !serverRoots.Equal(originalRoots) {
		t.Fatal("enrollment mutated the caller's server trust pool")
	}
	tlsCfg := transport.Base.(*http.Transport).TLSClientConfig
	if tlsCfg.MinVersion != tls.VersionTLS12 || tlsCfg.InsecureSkipVerify {
		t.Fatal("enrollment changed the caller's TLS verification settings")
	}
	enrolled, err := tlsClientCert(t, transport)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(enrolled.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: tlsCfg.RootCAs, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatalf("enrollment CA missing from trust pool: %v", err)
	}
	// The configured test CA replaces system trust, but does not trust arbitrary
	// servers. A separate server with a different certificate must still fail.
	untrustedCert := *server.Certificate()
	untrustedCert.SerialNumber = big.NewInt(99)
	serverKey := server.TLS.Certificates[0].PrivateKey
	untrustedDER, err := x509.CreateCertificate(rand.Reader, &untrustedCert, &untrustedCert, server.Certificate().PublicKey, serverKey)
	if err != nil {
		t.Fatal(err)
	}
	untrusted := httptest.NewUnstartedServer(http.NotFoundHandler())
	untrusted.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{untrustedDER}, PrivateKey: serverKey}}}
	untrusted.Config.ErrorLog = server.Config.ErrorLog
	untrusted.StartTLS()
	defer untrusted.Close()
	if resp, err := httpClient.Get(untrusted.URL); err == nil {
		resp.Body.Close()
		t.Fatal("untrusted HTTPS server was accepted")
	} else if !strings.Contains(err.Error(), "unknown authority") {
		t.Fatalf("expected untrusted CA rejection, got %v", err)
	}
	wrongHost := tlsCfg.Clone()
	wrongHost.ServerName = "other.example.invalid"
	wrongHostClient := &http.Client{Transport: agentadapter.NewHTTPTransportWithTLS(wrongHost), Timeout: 5 * time.Second}
	defer wrongHostClient.CloseIdleConnections()
	if resp, err := wrongHostClient.Get(server.URL); err == nil {
		resp.Body.Close()
		t.Fatal("HTTPS server with the wrong hostname was accepted")
	}
}

func TestSetupMTLSPreservesSystemRoots(t *testing.T) {
	var certCalls int32
	_, client := fakeMTLSServer(t, time.Now().Add(time.Hour), &certCalls)
	systemRoots, err := x509.SystemCertPool()
	if err != nil {
		t.Fatal(err)
	}
	transport, stop, err := setupMTLS(context.Background(), client,
		platformSessionFlags{server: "demo", agent: "ops-agent"},
		"mcpruntime.org", nil, false, false, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	cfg := transport.Base.(*http.Transport).TLSClientConfig
	if cfg.InsecureSkipVerify {
		t.Fatal("server certificate verification is disabled")
	}
	// Compare with system roots plus the independently issued client CA.
	cred, err := issueAdapterCredential(context.Background(), client,
		platformSessionFlags{server: "demo", agent: "ops-agent"}, "mcpruntime.org")
	if err != nil {
		t.Fatal(err)
	}
	systemRoots.AppendCertsFromPEM(cred.CABundle)
	if !cfg.RootCAs.Equal(systemRoots) {
		t.Fatal("enrollment discarded system HTTPS trust roots")
	}
}

func TestMTLSRefresherRotateSwapsCertificate(t *testing.T) {
	var certCalls int32
	_, client := fakeMTLSServer(t, time.Now().Add(time.Hour), &certCalls)
	transport, stop, err := setupMTLS(
		context.Background(), client,
		platformSessionFlags{server: "demo", agent: "ops-agent"},
		"mcpruntime.org", nil, false, false, nil,
	)
	if err != nil {
		t.Fatalf("setupMTLS: %v", err)
	}
	defer stop()
	if transport == nil || transport.Base == nil {
		t.Fatal("transport base must be set")
	}
	if got := atomic.LoadInt32(&certCalls); got != 1 {
		t.Fatalf("certCalls after setup = %d, want 1", got)
	}

	// The refresher created inside setupMTLS isn't returned, so reconstruct a
	// rotate against the same transport to exercise the swap path directly.
	r := &mtlsRefresher{
		client:      client,
		flags:       platformSessionFlags{server: "demo", agent: "ops-agent"},
		trustDomain: "mcpruntime.org",
		transport:   transport,
		expiry:      time.Now().Add(time.Hour),
	}
	first, err := tlsClientCert(t, transport)
	if err != nil {
		t.Fatalf("initial cert: %v", err)
	}
	r.cert.Store(first)

	if err := r.rotate(context.Background()); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if got := atomic.LoadInt32(&certCalls); got != 2 {
		t.Fatalf("certCalls after rotate = %d, want 2", got)
	}
	second := r.cert.Load()
	if second == nil || len(second.Certificate) == 0 {
		t.Fatal("rotated cert is empty")
	}
	if string(second.Certificate[0]) == string(first.Certificate[0]) {
		t.Fatal("rotate did not replace the stored certificate")
	}
}

// tlsClientCert pulls the current client certificate out of a transport's
// GetClientCertificate callback so tests can compare it across rotations.
func tlsClientCert(t *testing.T, rt *agentadapter.RuntimeTransport) (*tls.Certificate, error) {
	t.Helper()
	httpTransport, ok := rt.Base.(*http.Transport)
	if !ok || httpTransport.TLSClientConfig == nil || httpTransport.TLSClientConfig.GetClientCertificate == nil {
		t.Fatal("transport base is not a TLS http.Transport with GetClientCertificate")
	}
	return httpTransport.TLSClientConfig.GetClientCertificate(&tls.CertificateRequestInfo{})
}
