package agentadapter

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProxyConfigRequiresRuntimeAndClientCertificate(t *testing.T) {
	t.Parallel()

	cfg := ProxyConfig{}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), EnvRuntimeURL) {
		t.Fatalf("Validate() error = %v, want missing runtime URL", err)
	}
	runtimeURL, err := url.Parse("https://runtime.example/mcp")
	if err != nil {
		t.Fatal(err)
	}
	cfg.RuntimeURL = runtimeURL
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "TLS client certificate") {
		t.Fatalf("Validate() error = %v, want missing certificate", err)
	}
	cfg.Transport = &RuntimeTransport{Base: NewHTTPTransportWithTLS(&tls.Config{
		Certificates: []tls.Certificate{{}},
	})}
	cfg.CertificateIdentity = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want certificate config accepted", err)
	}
}

func TestBuildTLSConfigLoadsCABundle(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer upstream.Close()

	caPath := filepath.Join(t.TempDir(), "ca.pem")
	caPEM := pemForCertificate(upstream.Certificate())
	if err := os.WriteFile(caPath, caPEM, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := BuildTLSConfig("", "", caPath)
	if err != nil {
		t.Fatalf("BuildTLSConfig() error = %v", err)
	}
	if cfg.RootCAs == nil {
		t.Fatal("RootCAs = nil, want custom CA pool")
	}
}

func TestBuildTLSConfigRejectsDirectoryCABundle(t *testing.T) {
	t.Parallel()

	_, err := BuildTLSConfig("", "", t.TempDir())
	if err == nil {
		t.Fatal("BuildTLSConfig() error = nil, want directory rejection")
	}
	if !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("BuildTLSConfig() error = %q, want regular file message", err)
	}
}

func pemForCertificate(cert *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
}
