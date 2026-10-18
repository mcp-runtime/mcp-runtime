package serviceutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type transportTestCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newTransportTestCA(t *testing.T) transportTestCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "disposable-transport-root"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return transportTestCA{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}
func (ca transportTestCA) leaf(t *testing.T, server bool) ([]byte, []byte, tls.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "disposable-transport-leaf"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(30 * time.Minute), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	if server {
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		template.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
		template.DNSNames = []string{"localhost"}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return certPEM, keyPEM, pair
}
func writeTransportTestFile(t *testing.T, file string, data []byte) {
	t.Helper()
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestConfiguredTransportTLSVerifiesPeersAndReloadsClientLeaf(t *testing.T) {
	ca := newTransportTestCA(t)
	_, _, serverPair := ca.leaf(t, true)
	clientCert, clientKey, _ := ca.leaf(t, false)
	directory := t.TempDir()
	caFile, certFile, keyFile := filepath.Join(directory, "ca.crt"), filepath.Join(directory, "tls.crt"), filepath.Join(directory, "tls.key")
	writeTransportTestFile(t, caFile, ca.pem)
	writeTransportTestFile(t, certFile, clientCert)
	writeTransportTestFile(t, keyFile, clientKey)
	t.Setenv("KAFKA_TLS_ENABLED", "true")
	t.Setenv("KAFKA_TLS_CA_FILE", caFile)
	t.Setenv("KAFKA_TLS_CERT_FILE", certFile)
	t.Setenv("KAFKA_TLS_KEY_FILE", keyFile)
	config, err := ClientTLSFromEnv("KAFKA")
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca.cert)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.TLS.PeerCertificates[0].SerialNumber.String())
	}))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{serverPair}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool}
	server.StartTLS()
	defer server.Close()
	transport := &http.Transport{TLSClientConfig: config, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	get := func() string {
		response, err := client.Get(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	old := get()
	newCert, newKey, _ := ca.leaf(t, false)
	writeTransportTestFile(t, certFile, newCert)
	writeTransportTestFile(t, keyFile, newKey)
	if current := get(); current == old {
		t.Fatal("mounted client certificate renewal was not used on a new connection")
	}
	for name, change := range map[string]func(*tls.Config){"foreign-root": func(c *tls.Config) {
		other := newTransportTestCA(t)
		c.RootCAs = x509.NewCertPool()
		c.RootCAs.AddCert(other.cert)
	}, "wrong-host": func(c *tls.Config) { c.ServerName = "foreign.invalid" }, "missing-client": func(c *tls.Config) { c.Certificates = nil; c.GetClientCertificate = nil }} {
		t.Run(name, func(t *testing.T) {
			invalid := config.Clone()
			change(invalid)
			tr := &http.Transport{TLSClientConfig: invalid}
			defer tr.CloseIdleConnections()
			if response, err := (&http.Client{Transport: tr, Timeout: time.Second}).Get(server.URL); err == nil {
				response.Body.Close()
				t.Fatal("unverified peer accepted")
			}
		})
	}
}

func TestHTTPTransportTLSUsesRenewedServingLeaf(t *testing.T) {
	ca := newTransportTestCA(t)
	cert, key, _ := ca.leaf(t, true)
	directory := t.TempDir()
	certFile, keyFile := filepath.Join(directory, "tls.crt"), filepath.Join(directory, "tls.key")
	writeTransportTestFile(t, certFile, cert)
	writeTransportTestFile(t, keyFile, key)
	t.Setenv("HTTP_TLS_CERT_FILE", certFile)
	t.Setenv("HTTP_TLS_KEY_FILE", keyFile)
	config, err := HTTPServerTLSFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }), ReadHeaderTimeout: time.Second}
	go func() { _ = server.Serve(tls.NewListener(listener, config)) }()
	defer server.Close()
	pool := x509.NewCertPool()
	pool.AddCert(ca.cert)
	tr := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}, DisableKeepAlives: true}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 3 * time.Second}
	get := func() string {
		response, err := client.Get("https://" + listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		return response.TLS.PeerCertificates[0].SerialNumber.String()
	}
	old := get()
	cert, key, _ = ca.leaf(t, true)
	writeTransportTestFile(t, certFile, cert)
	writeTransportTestFile(t, keyFile, key)
	if get() == old {
		t.Fatal("renewed HTTPS serving certificate was not observed")
	}
	writeTransportTestFile(t, keyFile, []byte("private material must not appear in errors"))
	if err := ListenAndServeHTTP(&http.Server{Addr: "invalid-address"}); err == nil || strings.Contains(err.Error(), "private material") {
		t.Fatalf("invalid configured TLS must fail before listening without echoing key data: %v", err)
	}
}

func TestConfiguredTransportTLSCannotSilentlyDowngrade(t *testing.T) {
	t.Setenv("KAFKA_TLS_CA_FILE", filepath.Join(t.TempDir(), "missing.crt"))
	for _, enabled := range []string{"true", "false", "invalid", ""} {
		t.Setenv("KAFKA_TLS_ENABLED", enabled)
		if config, err := ClientTLSFromEnv("KAFKA"); err == nil || config != nil {
			t.Fatalf("bad TLS configuration became plaintext: config=%v err=%v", config, err)
		}
	}
	ca := newTransportTestCA(t)
	if _, err := transportPublicRoots(append(append([]byte{}, ca.pem...), []byte("-----BEGIN PRIVATE KEY-----\ncanary\n-----END PRIVATE KEY-----\n")...)); err == nil {
		t.Fatal("private material accepted in public CA bundle")
	}
}
