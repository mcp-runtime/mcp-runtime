package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type rolloverCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func makeRolloverCA(t *testing.T, name string) rolloverCA {
	return makeRolloverCAWithValidity(t, name, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
}

func makeRolloverCAWithValidity(t *testing.T, name string, notBefore, notAfter time.Time) rolloverCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name}, NotBefore: notBefore, NotAfter: notAfter, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return rolloverCA{cert, key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

func (ca rolloverCA) leaf(t *testing.T, server bool) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "rollover-client"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(30 * time.Minute), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	if server {
		template.DNSNames = []string{"localhost"}
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der, ca.cert.Raw}, PrivateKey: key}
}

func writeRolloverTrust(t *testing.T, file string, certificates ...[]byte) {
	t.Helper()
	var data []byte
	for _, cert := range certificates {
		data = append(data, cert...)
	}
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestClientCARolloverOverlapAndRetirementIncludingResumption(t *testing.T) {
	oldCA, newCA := makeRolloverCA(t, "old-workload-root"), makeRolloverCA(t, "new-workload-root")
	file := filepath.Join(t.TempDir(), "ca.crt")
	writeRolloverTrust(t, file, oldCA.pem)
	config, reloader, err := gatewayTLSConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	config.Certificates = []tls.Certificate{oldCA.leaf(t, true)}
	// Stable tickets prove retirement also blocks an actual cached TLS session.
	var ticketKey [32]byte
	if _, err := rand.Read(ticketKey[:]); err != nil {
		t.Fatal(err)
	}
	config.SetSessionTicketKeys([][32]byte{ticketKey})
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") }))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.TLS = config
	server.StartTLS()
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(oldCA.cert)
	client := func(certificate tls.Certificate) *http.Client {
		return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: "localhost", Certificates: []tls.Certificate{certificate}, ClientSessionCache: tls.NewLRUClientSessionCache(4)}, DisableKeepAlives: true}, Timeout: 3 * time.Second}
	}
	oldClient, newClient := client(oldCA.leaf(t, false)), client(newCA.leaf(t, false))
	defer oldClient.CloseIdleConnections()
	defer newClient.CloseIdleConnections()
	get := func(client *http.Client) (bool, error) {
		response, err := client.Get(server.URL)
		if err != nil {
			return false, err
		}
		defer response.Body.Close()
		_, err = io.ReadAll(response.Body)
		return response.TLS.DidResume, err
	}
	if _, err := get(oldClient); err != nil {
		t.Fatal(err)
	}
	if resumed, err := get(oldClient); err != nil || !resumed {
		t.Fatalf("old client did not establish a resumable session: resumed=%v err=%v", resumed, err)
	}
	if _, err := get(newClient); err == nil {
		t.Fatal("new root trusted before overlap")
	}
	writeRolloverTrust(t, file, oldCA.pem, newCA.pem)
	if err := reloader.reload(); err != nil {
		t.Fatal(err)
	}
	if _, err := get(oldClient); err != nil {
		t.Fatalf("old client broke during overlap: %v", err)
	}
	if _, err := get(newClient); err != nil {
		t.Fatalf("new client rejected during overlap: %v", err)
	}
	writeRolloverTrust(t, file, newCA.pem)
	if err := reloader.reload(); err != nil {
		t.Fatal(err)
	}
	if _, err := get(oldClient); err == nil {
		t.Fatal("retired old root still accepted through TLS resumption")
	}
	if _, err := get(client(oldCA.leaf(t, false))); err == nil {
		t.Fatal("retired old root accepted in a fresh handshake")
	}
	if _, err := get(newClient); err != nil {
		t.Fatalf("current client broke after retirement: %v", err)
	}
}

func TestClientCATrustRejectsInvalidReplacementAndRetainsLastGood(t *testing.T) {
	ca := makeRolloverCA(t, "good-root")
	file := filepath.Join(t.TempDir(), "ca.crt")
	writeRolloverTrust(t, file, ca.pem)
	_, reloader, err := gatewayTLSConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	original := reloader.trust.Load()
	expired := makeRolloverCAWithValidity(t, "expired", time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour))
	future := makeRolloverCAWithValidity(t, "future", time.Now().Add(time.Hour), time.Now().Add(2*time.Hour))
	for name, data := range map[string][]byte{"expired": expired.pem, "not-yet-valid": future.pem, "empty": nil, "garbage": []byte("not a certificate"), "private": append(append([]byte{}, ca.pem...), []byte("-----BEGIN PRIVATE KEY-----\nsecret\n-----END PRIVATE KEY-----\n")...), "prefix": append([]byte("unexpected private field\n"), ca.pem...), "oversized": []byte(strings.Repeat("x", (1<<20)+1))} {
		t.Run(name, func(t *testing.T) {
			writeRolloverTrust(t, file, data)
			if err := reloader.reload(); err == nil {
				t.Fatal("invalid trust replacement accepted")
			}
			if reloader.trust.Load() != original {
				t.Fatal("invalid replacement changed the last-good snapshot")
			}
		})
	}
	writeRolloverTrust(t, file, expired.pem, ca.pem)
	if err := reloader.reload(); err != nil {
		t.Fatalf("valid root overlapping an expired predecessor rejected: %v", err)
	}
	writeRolloverTrust(t, file, future.pem)
	if _, _, err := gatewayTLSConfig(file); err == nil {
		t.Fatal("invalid initial trust did not fail startup")
	}
	if _, _, err := gatewayTLSConfig(""); err == nil {
		t.Fatal("missing trust path did not fail startup")
	}
}

func TestClientCAWatchDetectsContentChangeWithUnchangedModtime(t *testing.T) {
	oldCA, newCA := makeRolloverCA(t, "old"), makeRolloverCA(t, "new")
	file := filepath.Join(t.TempDir(), "ca.crt")
	writeRolloverTrust(t, file, oldCA.pem)
	_, reloader, err := gatewayTLSConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	previous := reloader.trust.Load()
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go reloader.watch(ctx, 10*time.Millisecond)
	writeRolloverTrust(t, file, newCA.pem)
	if err := os.Chtimes(file, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if reloader.trust.Load() != previous {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("CA-only change with unchanged modtime was never observed")
}
