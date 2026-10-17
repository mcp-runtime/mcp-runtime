package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"os"
	"sync/atomic"
	"time"
)

// Client trust must reload alongside serving certificates. Root rollover uses
// old+new trust first, then new-only trust after old leaves have retired.
type clientCATrust struct {
	pool *x509.CertPool
	hash [sha256.Size]byte
}

type clientCAReloader struct {
	file  string
	trust atomic.Pointer[clientCATrust]
}

func readClientCATrust(file string) (*clientCATrust, error) {
	// #nosec G304 G703 -- administrator-configured mounted public CA file; regular-file/size and public-certificate checks below, never a request path.
	source, err := os.Open(file)
	if err != nil {
		return nil, errors.New("client CA bundle is unreadable")
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, errors.New("client CA bundle must be a bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(source, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, errors.New("client CA bundle exceeds its read bound")
	}
	pool := x509.NewCertPool()
	remaining, count := bytes.TrimSpace(data), 0
	for len(remaining) > 0 {
		if !bytes.HasPrefix(remaining, []byte("-----BEGIN CERTIFICATE-----")) {
			return nil, errors.New("client CA bundle must contain only public certificates")
		}
		block, rest := pem.Decode(remaining)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return nil, errors.New("client CA bundle must contain only public certificates")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !cert.IsCA || !cert.BasicConstraintsValid || cert.KeyUsage&x509.KeyUsageCertSign == 0 {
			return nil, errors.New("client CA bundle contains an invalid signing CA")
		}
		pool.AddCert(cert)
		count++
		remaining = bytes.TrimSpace(rest)
	}
	if count == 0 {
		return nil, errors.New("client CA bundle contains no certificates")
	}
	return &clientCATrust{pool: pool, hash: sha256.Sum256(data)}, nil
}

func gatewayTLSConfig(clientCAFile string) (*tls.Config, *clientCAReloader, error) {
	if clientCAFile == "" {
		return nil, nil, errors.New("TLS_CLIENT_CA_FILE is required")
	}
	trust, err := readClientCATrust(clientCAFile)
	if err != nil {
		return nil, nil, err
	}
	reloader := &clientCAReloader{file: clientCAFile}
	reloader.trust.Store(trust)
	config := &tls.Config{MinVersion: tls.VersionTLS12, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: trust.pool}
	config.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
		current := config.Clone()
		current.GetConfigForClient = nil
		current.ClientCAs = reloader.trust.Load().pool
		return current, nil
	}
	// VerifyConnection also runs on resumed TLS connections. Without this,
	// resumption can keep a removed root trusted after the overlap has ended.
	config.VerifyConnection = func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 {
			return errors.New("verified client certificate is required")
		}
		intermediates := x509.NewCertPool()
		for _, cert := range state.PeerCertificates[1:] {
			intermediates.AddCert(cert)
		}
		_, err := state.PeerCertificates[0].Verify(x509.VerifyOptions{Roots: reloader.trust.Load().pool, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
		if err != nil {
			return errors.New("client certificate is not trusted by the current workload roots")
		}
		return nil
	}
	return config, reloader, nil
}

func (r *clientCAReloader) reload() error {
	trust, err := readClientCATrust(r.file)
	if err != nil {
		return err // retain the complete last validated trust snapshot
	}
	if current := r.trust.Load(); current == nil || current.hash != trust.hash {
		r.trust.Store(trust)
	}
	return nil
}

func (r *clientCAReloader) watch(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Compare contents, rather than modtime: projected Secret revisions
			// may have equal timestamps or rotate only ca.crt, not the leaf.
			if err := r.reload(); err != nil {
				log.Printf("gateway client CA reload failed; keeping previous trust: %v", err)
			}
		}
	}
}
