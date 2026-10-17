package certauth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"testing"
	"time"

	"mcp-runtime/pkg/identity"
)

const benchmarkSessionURI = "spiffe://benchmark.test/ns/team/session/benchmark-session"

// Generates ephemeral public/private fixtures in memory, outside timing. The
// trusted chain is real; no InsecureSkipVerify or fake VerifiedChains is used.
func benchmarkCertificates(b *testing.B) (tls.Certificate, tls.Certificate, *x509.CertPool, *x509.Certificate) {
	b.Helper()
	now := time.Now()
	key := func() *ecdsa.PrivateKey {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			b.Fatal(err)
		}
		return k
	}
	rootKey := key()
	rootTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "benchmark ephemeral root"}, IsCA: true, BasicConstraintsValid: true,
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTemplate, rootTemplate, rootKey.Public(), rootKey)
	if err != nil {
		b.Fatal(err)
	}
	root, err := x509.ParseCertificate(rootDER)
	if err != nil {
		b.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(root)
	issue := func(serial int64, usage x509.ExtKeyUsage) tls.Certificate {
		leafKey := key()
		uri, err := url.Parse(benchmarkSessionURI)
		if err != nil {
			b.Fatal(err)
		}
		template := &x509.Certificate{SerialNumber: big.NewInt(serial), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}}
		if usage == x509.ExtKeyUsageServerAuth {
			template.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
		} else {
			template.URIs = []*url.URL{uri}
		}
		der, err := x509.CreateCertificate(rand.Reader, template, root, leafKey.Public(), rootKey)
		if err != nil {
			b.Fatal(err)
		}
		leaf, err := x509.ParseCertificate(der)
		if err != nil {
			b.Fatal(err)
		}
		return tls.Certificate{Certificate: [][]byte{der, rootDER}, PrivateKey: leafKey, Leaf: leaf}
	}
	server, client := issue(2, x509.ExtKeyUsageServerAuth), issue(3, x509.ExtKeyUsageClientAuth)
	return server, client, roots, client.Leaf
}

func BenchmarkSessionCertificateChain(b *testing.B) {
	_, _, roots, leaf := benchmarkCertificates(b)
	opts := x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, CurrentTime: time.Now()}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := leaf.Verify(opts); err != nil || !identity.CertificateHasURI(leaf, benchmarkSessionURI) {
			b.Fatalf("session certificate verification: %v", err)
		}
	}
}

func BenchmarkSessionMTLSTransport(b *testing.B) {
	for _, reuse := range []bool{false, true} {
		name := "full-handshake"
		if reuse {
			name = "reused-connection"
		}
		b.Run(name, func(b *testing.B) {
			serverCert, clientCert, roots, _ := benchmarkCertificates(b)
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 || !identity.CertificateHasURI(r.TLS.PeerCertificates[0], benchmarkSessionURI) {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				_, _ = io.WriteString(w, "ok")
			}))
			server.TLS = &tls.Config{Certificates: []tls.Certificate{serverCert}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots, MinVersion: tls.VersionTLS13}
			server.StartTLS()
			b.Cleanup(server.Close)
			transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, Certificates: []tls.Certificate{clientCert}, MinVersion: tls.VersionTLS13},
				DisableKeepAlives: !reuse, MaxIdleConnsPerHost: 1, ForceAttemptHTTP2: false}
			b.Cleanup(transport.CloseIdleConnections)
			client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
			request := func(timed bool) {
				reused := false
				ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused }})
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
				if err != nil {
					b.Fatal(err)
				}
				response, err := client.Do(req)
				if err != nil {
					b.Fatal(err)
				}
				body, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil || response.StatusCode != http.StatusOK || string(body) != "ok" || timed && reused != reuse || response.TLS.DidResume {
					b.Fatalf("failed mTLS/connection invariant: status=%d reused=%v resumed=%v error=%v", response.StatusCode, reused, response.TLS.DidResume, err)
				}
			}
			request(false)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				request(true)
			}
		})
	}
}
