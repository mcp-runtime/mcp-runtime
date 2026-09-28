package adapter

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"mcp-runtime/internal/agentadapter"
)

// identityFlags binds transport and certificate flags shared by every adapter
// subcommand. Values default to their matching environment variables.
type identityFlags struct {
	runtimeURL     string
	hostHeader     string
	requestTimeout string
	logLevel       string
	disableXFF     bool
	// certificate identity / upstream auth
	trustDomain   string
	authHeader    string
	tlsClientCert string
	tlsClientKey  string
	tlsCABundle   string
	// proxy-only
	maxInboundBytes int64
}

func bindIdentityFlags(cmd *cobra.Command, f *identityFlags) {
	cmd.Flags().StringVar(&f.runtimeURL, "runtime-url", os.Getenv(agentadapter.EnvRuntimeURL),
		"Platform-issued absolute MCP runtime URL (default: $"+agentadapter.EnvRuntimeURL+")")
	cmd.Flags().StringVar(&f.hostHeader, "host-header", os.Getenv(agentadapter.EnvHostHeader),
		"Override the Host header sent to the runtime (default: $"+agentadapter.EnvHostHeader+")")
	cmd.Flags().StringVar(&f.logLevel, "log-level", os.Getenv(agentadapter.EnvLogLevel),
		"Adapter log level: info logs runtime denials (default: $"+agentadapter.EnvLogLevel+")")
	cmd.Flags().BoolVar(&f.disableXFF, "no-xforwarded", parseEnvBool(agentadapter.EnvSetXForwarded, false),
		"Do not set X-Forwarded-* headers when forwarding to the runtime")
	cmd.Flags().StringVar(&f.requestTimeout, "request-timeout", os.Getenv(agentadapter.EnvRequestTimeout),
		"HTTP request timeout for adapter→runtime calls, e.g. 30s (default: $"+agentadapter.EnvRequestTimeout+")")
	cmd.Flags().StringVar(&f.trustDomain, "trust-domain", os.Getenv(EnvMTLSTrustDomain),
		"Optional platform SPIFFE trust domain override for adapter certificate enrollment; default: $"+EnvMTLSTrustDomain)
	cmd.Flags().StringVar(&f.authHeader, "auth-header", os.Getenv(agentadapter.EnvAuthHeader),
		"Static OAuth Authorization value when the local MCP client does not send one, e.g. \"Bearer <token>\" (default: $"+agentadapter.EnvAuthHeader+")")
	cmd.Flags().StringVar(&f.tlsClientCert, "tls-client-cert", os.Getenv(agentadapter.EnvTLSClientCert),
		"Path to PEM client certificate for mTLS to the runtime (default: $"+agentadapter.EnvTLSClientCert+")")
	cmd.Flags().StringVar(&f.tlsClientKey, "tls-client-key", os.Getenv(agentadapter.EnvTLSClientKey),
		"Path to PEM client key for mTLS to the runtime (default: $"+agentadapter.EnvTLSClientKey+")")
	cmd.Flags().StringVar(&f.tlsCABundle, "tls-ca-bundle", os.Getenv(agentadapter.EnvTLSCABundle),
		"Path to PEM CA bundle to verify the runtime's TLS certificate (default: $"+agentadapter.EnvTLSCABundle+")")
}

// resolved holds the validated cross-cutting pieces of an adapter config —
// runtime URL, transport, and shared display fields — that
// every subcommand needs before building its transport-specific config.
type resolved struct {
	runtimeURL *url.URL
	transport  *agentadapter.RuntimeTransport
	hostHeader string
	logLevel   string
}

// resolve parses and validates the shared adapter fields on the CLI side so
// error messages reference the user-facing flag name instead of the env var.
func (f identityFlags) resolve() (resolved, error) {
	out := resolved{
		hostHeader: strings.TrimSpace(f.hostHeader),
		logLevel:   strings.TrimSpace(f.logLevel),
	}

	if raw := strings.TrimSpace(f.requestTimeout); raw != "" {
		timeout, err := time.ParseDuration(raw)
		if err != nil {
			return resolved{}, fmt.Errorf("--request-timeout (or $%s) is invalid: %w", agentadapter.EnvRequestTimeout, err)
		}
		if timeout <= 0 {
			return resolved{}, fmt.Errorf("--request-timeout (or $%s) must be greater than zero", agentadapter.EnvRequestTimeout)
		}
		out.transport = &agentadapter.RuntimeTransport{Timeout: timeout}
	}
	if raw := strings.TrimSpace(f.authHeader); raw != "" {
		if out.transport == nil {
			out.transport = &agentadapter.RuntimeTransport{}
		}
		out.transport.AuthHeader = raw
	}
	tlsCert := strings.TrimSpace(f.tlsClientCert)
	tlsKey := strings.TrimSpace(f.tlsClientKey)
	tlsCA := strings.TrimSpace(f.tlsCABundle)
	if tlsCert != "" || tlsKey != "" || tlsCA != "" {
		tlsCfg, err := agentadapter.BuildTLSConfig(tlsCert, tlsKey, tlsCA)
		if err != nil {
			return resolved{}, fmt.Errorf("TLS config: %w", err)
		}
		if out.transport == nil {
			out.transport = &agentadapter.RuntimeTransport{}
		}
		out.transport.Base = newHTTPTransportWithTLS(tlsCfg)
	}

	if raw := strings.TrimSpace(f.runtimeURL); raw != "" {
		parsed, err := url.Parse(raw)
		if err != nil {
			return resolved{}, fmt.Errorf("--runtime-url is invalid: %w", err)
		}
		if parsed.Scheme == "" || parsed.Host == "" {
			return resolved{}, fmt.Errorf("--runtime-url must be an absolute HTTP URL")
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return resolved{}, fmt.Errorf("--runtime-url must use http or https")
		}
		out.runtimeURL = parsed
	}
	return out, nil
}

// toProxyConfig produces an agentadapter.ProxyConfig from the resolved
// shared fields plus proxy-only listener/XFF settings.
func (f identityFlags) toProxyConfig(listenAddr string) (agentadapter.ProxyConfig, error) {
	r, err := f.resolve()
	if err != nil {
		return agentadapter.ProxyConfig{}, err
	}
	listen := strings.TrimSpace(listenAddr)
	if listen == "" {
		listen = agentadapter.DefaultListenAddr
	}
	return agentadapter.ProxyConfig{
		RuntimeURL:        r.runtimeURL,
		Transport:         r.transport,
		HostHeader:        r.hostHeader,
		ListenAddr:        listen,
		LogLevel:          r.logLevel,
		DisableXForwarded: f.disableXFF,
		MaxInboundBytes:   f.maxInboundBytes,
	}, nil
}

// bindProxyFlags adds proxy-specific flags on top of the shared identity flags.
func bindProxyFlags(cmd *cobra.Command, f *identityFlags) {
	cmd.Flags().Int64Var(&f.maxInboundBytes, "max-inbound-bytes",
		parseEnvInt64(agentadapter.EnvMaxInboundBytes, 0),
		"Maximum inbound JSON-RPC body bytes the proxy buffers before responding with 413 "+
			"(default: $"+agentadapter.EnvMaxInboundBytes+" or 16777216)")
}

func parseEnvInt64(name string, def int64) int64 {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return def
	}
	return n
}

func parseEnvBool(name string, def bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	switch value {
	case "":
		return def
	case "1", "t", "true", "y", "yes", "on":
		// MCP_RUNTIME_SET_XFF=true means *enable* X-Forwarded, so disableXFF=false.
		return false
	case "0", "f", "false", "n", "no", "off":
		return true
	default:
		return def
	}
}

// newHTTPTransportWithTLS delegates to the shared agentadapter helper so the
// env and flag paths produce identical transports.
func newHTTPTransportWithTLS(cfg *tls.Config) *http.Transport {
	return agentadapter.NewHTTPTransportWithTLS(cfg)
}
