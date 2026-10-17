package serviceutil

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// ClientTLSFromEnv configures an authenticated encrypted transport. Supplying
// any TLS file enables TLS; an explicit false flag cannot silently ignore it.
// Paths are administrator configuration, never caller-controlled request data.
func ClientTLSFromEnv(prefix string) (*tls.Config, error) {
	caFile := strings.TrimSpace(os.Getenv(prefix + "_TLS_CA_FILE"))
	certFile := strings.TrimSpace(os.Getenv(prefix + "_TLS_CERT_FILE"))
	keyFile := strings.TrimSpace(os.Getenv(prefix + "_TLS_KEY_FILE"))
	enabled := caFile != "" || certFile != "" || keyFile != ""
	if value := strings.TrimSpace(os.Getenv(prefix + "_TLS_ENABLED")); value != "" {
		explicit, err := strconv.ParseBool(value)
		if err != nil {
			return nil, fmt.Errorf("%s_TLS_ENABLED must be a boolean", prefix)
		}
		if !explicit && enabled {
			return nil, fmt.Errorf("%s TLS files cannot be combined with disabled TLS", prefix)
		}
		enabled = explicit
	}
	if !enabled {
		return nil, nil
	}
	config := &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile != "" {
		data, err := readTransportTLSFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("%s TLS CA file is unavailable", prefix)
		}
		config.RootCAs, err = transportPublicRoots(data)
		if err != nil {
			return nil, fmt.Errorf("%s TLS CA file is invalid: %w", prefix, err)
		}
	}
	if (certFile == "") != (keyFile == "") {
		return nil, fmt.Errorf("%s TLS requires both client certificate and key", prefix)
	}
	if certFile != "" {
		pair, err := transportTLSKeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("%s TLS client certificate and key are invalid", prefix)
		}
		config.Certificates = []tls.Certificate{pair}
		config.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			current, err := transportTLSKeyPair(certFile, keyFile)
			if err != nil {
				return nil, fmt.Errorf("%s TLS client certificate renewal is unavailable", prefix)
			}
			return &current, nil
		}
	}
	return config, nil
}

// ListenAndServeHTTP honors the service's explicit HTTPS configuration and never
// falls back to cleartext when configured TLS material is missing or invalid.
func ListenAndServeHTTP(server *http.Server) error {
	config, err := HTTPServerTLSFromEnv()
	if err != nil {
		return err
	}
	if config == nil {
		return server.ListenAndServe()
	}
	server.TLSConfig = config
	return server.ListenAndServeTLS("", "")
}

// HTTPServerTLSFromEnv validates the complete serving configuration before a
// listener opens. A nil result is the explicit development/plain-HTTP mode.
func HTTPServerTLSFromEnv() (*tls.Config, error) {
	certFile, keyFile := strings.TrimSpace(os.Getenv("HTTP_TLS_CERT_FILE")), strings.TrimSpace(os.Getenv("HTTP_TLS_KEY_FILE"))
	if certFile == "" && keyFile == "" {
		return nil, nil
	}
	if certFile == "" || keyFile == "" {
		return nil, fmt.Errorf("HTTP TLS requires both certificate and key")
	}
	_, err := transportTLSKeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("HTTP TLS certificate and key are invalid")
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		// Mounted cert-manager Secrets rotate atomically. Load on each new
		// handshake so a renewed serving leaf takes effect without restart.
		current, err := transportTLSKeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("HTTP TLS certificate renewal is unavailable")
		}
		return &current, nil
	}}, nil
}

func transportPublicRoots(data []byte) (*x509.CertPool, error) {
	pool, usable, count := x509.NewCertPool(), 0, 0
	now := time.Now()
	for remaining := bytes.TrimSpace(data); len(remaining) > 0; {
		if !bytes.HasPrefix(remaining, []byte("-----BEGIN CERTIFICATE-----")) {
			return nil, fmt.Errorf("only public CA certificates are permitted")
		}
		block, rest := pem.Decode(remaining)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 || count >= 16 {
			return nil, fmt.Errorf("CA bundle is malformed or exceeds 16 certificates")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !cert.IsCA || !cert.BasicConstraintsValid || cert.KeyUsage&x509.KeyUsageCertSign == 0 {
			return nil, fmt.Errorf("CA bundle contains an invalid signing certificate")
		}
		pool.AddCert(cert)
		if !now.Before(cert.NotBefore) && now.Before(cert.NotAfter) {
			usable++
		}
		count++
		remaining = bytes.TrimSpace(rest)
	}
	if usable == 0 {
		return nil, fmt.Errorf("CA bundle contains no currently valid signing CA")
	}
	return pool, nil
}

func transportTLSKeyPair(certFile, keyFile string) (tls.Certificate, error) {
	cert, err := readTransportTLSFile(certFile)
	if err != nil {
		return tls.Certificate{}, err
	}
	key, err := readTransportTLSFile(keyFile)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.X509KeyPair(cert, key)
}

func readTransportTLSFile(file string) ([]byte, error) {
	// #nosec G304 G703 -- administrator-configured TLS mount, bounded regular file; errors never print file data or private material.
	source, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, fmt.Errorf("TLS material must be a bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(source, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, fmt.Errorf("TLS material exceeds its read limit")
	}
	return data, nil
}
