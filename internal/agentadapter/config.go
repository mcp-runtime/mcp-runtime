package agentadapter

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
)

const (
	EnvRuntimeURL      = "MCP_RUNTIME_URL"
	EnvHostHeader      = "MCP_RUNTIME_HOST_HEADER"
	EnvListenAddr      = "MCP_RUNTIME_LISTEN_ADDR"
	EnvSetXForwarded   = "MCP_RUNTIME_SET_XFF"
	EnvRequestTimeout  = "MCP_RUNTIME_REQUEST_TIMEOUT"
	EnvLogLevel        = "MCP_RUNTIME_LOG_LEVEL"
	EnvAuthHeader      = "MCP_RUNTIME_AUTH_HEADER"
	EnvTLSClientCert   = "MCP_RUNTIME_TLS_CLIENT_CERT"
	EnvTLSClientKey    = "MCP_RUNTIME_TLS_CLIENT_KEY"
	EnvTLSCABundle     = "MCP_RUNTIME_TLS_CA_BUNDLE"
	EnvMaxInboundBytes = "MCP_RUNTIME_MAX_INBOUND_BYTES"

	DefaultListenAddr = "127.0.0.1:8099"

	MCPProtocolHeader = "Mcp-Protocol-Version"
	MCPSessionHeader  = "Mcp-Session-Id"
)

// ProxyConfig configures the local HTTP reverse-proxy adapter that exposes
// Streamable HTTP MCP to an agent SDK.
type ProxyConfig struct {
	RuntimeURL *url.URL
	Transport  *RuntimeTransport
	// CertificateIdentity confirms that Transport presents a TLS client
	// certificate. The CLI sets it only after loading or enrolling a usable
	// keypair. OAuth-enabled targets additionally require a bearer token.
	CertificateIdentity bool
	HostHeader          string
	ListenAddr          string
	LogLevel            string
	LogWriter           io.Writer
	DisableXForwarded   bool
	// MaxInboundBytes caps the size of JSON-RPC request bodies the proxy
	// buffers when capturing metadata. Zero (or negative) means use
	// DefaultMaxInboundBytes (16 MiB). Over-cap requests respond with 413.
	MaxInboundBytes int64
	// MetricsHandler, when set, is served at /metrics. Typical use: a
	// Prometheus exporter wired to the OTel MeterProvider that backs
	// RuntimeTransport.Meter. Nil → /metrics returns 404.
	MetricsHandler http.Handler
}

// Validate requires a runtime URL and a TLS client certificate.
func (cfg ProxyConfig) Validate() error {
	if err := validateRuntimeURL(cfg.RuntimeURL); err != nil {
		return err
	}
	if !cfg.CertificateIdentity {
		return fmt.Errorf("TLS client certificate is required (%s and %s)", EnvTLSClientCert, EnvTLSClientKey)
	}
	return nil
}

func validateRuntimeURL(runtimeURL *url.URL) error {
	if runtimeURL == nil {
		return fmt.Errorf("missing required environment variable: %s", EnvRuntimeURL)
	}
	return nil
}

// transportOrDefault returns the configured transport, allocating a default
// (no base, no timeout) when the caller did not provide one.
func (cfg ProxyConfig) transportOrDefault() *RuntimeTransport {
	if cfg.Transport != nil {
		return cfg.Transport
	}
	return &RuntimeTransport{}
}

// NewHTTPTransportWithTLS returns an *http.Transport that uses the supplied
// TLS config while preserving http.DefaultTransport's dial timeouts, keep-alive
// settings, and ProxyFromEnvironment behaviour.
func NewHTTPTransportWithTLS(cfg *tls.Config) *http.Transport {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.TLSClientConfig = cfg
	return base
}

// BuildTLSConfig builds a *tls.Config for outbound runtime connections.
// certFile and keyFile must both be set (or both empty) for mTLS.
// caFile, when non-empty, replaces the default system CA pool.
func BuildTLSConfig(certFile, keyFile, caFile string) (*tls.Config, error) {
	cfg := &tls.Config{}
	if certFile != "" || keyFile != "" {
		if certFile == "" || keyFile == "" {
			return nil, fmt.Errorf("%s and %s must both be set for mTLS", EnvTLSClientCert, EnvTLSClientKey)
		}
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("loading TLS client cert/key: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	if caFile != "" {
		pem, err := readRegularFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("reading CA bundle %q: %w", caFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%s contains no valid PEM certificates", EnvTLSCABundle)
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}

func readRegularFile(path string) ([]byte, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve file path: %w", err)
	}
	root, err := os.OpenRoot(filepath.Dir(absPath))
	if err != nil {
		return nil, err
	}
	defer root.Close()

	base := filepath.Base(absPath)
	info, err := root.Stat(base)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file")
	}
	file, err := root.Open(base)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	return io.ReadAll(file)
}

func cloneURL(in *url.URL) *url.URL {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}
