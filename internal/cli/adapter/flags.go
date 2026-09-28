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

// identityFlags binds flags shared by every adapter subcommand. Flag values
// default to the matching environment variable so existing
// MCP_RUNTIME_* deployments keep working.
type identityFlags struct {
	runtimeURL      string
	humanID         string
	agentID         string
	teamID          string
	sessionID       string
	hostHeader      string
	protocolVersion string
	requestTimeout  string
	logLevel        string
	disableXFF      bool
	// upstream auth / TLS
	authMode      string
	trustDomain   string
	authHeader    string
	tlsClientCert string
	tlsClientKey  string
	tlsCABundle   string
	tlsInsecure   bool
	// proxy-only
	maxInboundBytes int64
	// stdio-only
	anonymous        bool
	anonymousMethods string
	toolsCacheTTL    string
}

// mtlsEnabled reports whether --auth selected certificate-based identity.
func (f identityFlags) mtlsEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(f.authMode), "mtls")
}

func bindIdentityFlags(cmd *cobra.Command, f *identityFlags) {
	cmd.Flags().StringVar(&f.runtimeURL, "runtime-url", os.Getenv(agentadapter.EnvRuntimeURL),
		"Platform-issued absolute MCP runtime URL (default: $"+agentadapter.EnvRuntimeURL+")")
	cmd.Flags().StringVar(&f.humanID, "human-id", os.Getenv(agentadapter.EnvHumanID),
		"Issued human identity (default: $"+agentadapter.EnvHumanID+")")
	cmd.Flags().StringVar(&f.agentID, "agent-id", os.Getenv(agentadapter.EnvAgentID),
		"Issued agent identity (default: $"+agentadapter.EnvAgentID+")")
	cmd.Flags().StringVar(&f.teamID, "team-id", os.Getenv(agentadapter.EnvTeamID),
		"Issued team identity for team-scoped grants (default: $"+agentadapter.EnvTeamID+")")
	cmd.Flags().StringVar(&f.sessionID, "session-id", os.Getenv(agentadapter.EnvSessionID),
		"Issued agent session identity (default: $"+agentadapter.EnvSessionID+")")
	cmd.Flags().StringVar(&f.hostHeader, "host-header", os.Getenv(agentadapter.EnvHostHeader),
		"Override the Host header sent to the runtime (default: $"+agentadapter.EnvHostHeader+")")
	cmd.Flags().StringVar(&f.protocolVersion, "protocol-version", os.Getenv(agentadapter.EnvProtocolVersion),
		"MCP protocol version header the stdio adapter sends for legacy (initialize-based) requests; requests that declare a version in params._meta use that version, and the HTTP proxy forwards the client's own header (default: $"+agentadapter.EnvProtocolVersion+" or "+agentadapter.DefaultProtocolVersion+")")
	cmd.Flags().StringVar(&f.logLevel, "log-level", os.Getenv(agentadapter.EnvLogLevel),
		"Adapter log level: info logs runtime denials (default: $"+agentadapter.EnvLogLevel+")")
	cmd.Flags().BoolVar(&f.disableXFF, "no-xforwarded", parseEnvBool(agentadapter.EnvSetXForwarded, false),
		"Do not set X-Forwarded-* headers when forwarding to the runtime")
	cmd.Flags().StringVar(&f.requestTimeout, "request-timeout", os.Getenv(agentadapter.EnvRequestTimeout),
		"HTTP request timeout for adapter→runtime calls, e.g. 30s (default: $"+agentadapter.EnvRequestTimeout+")")
	cmd.Flags().StringVar(&f.authMode, "auth", envOrDefault(EnvAdapterAuthMode, "header"),
		"Adapter auth mode: mtls (session-bound SPIFFE client certificate; recommended) or header "+
			"(legacy local/dev only); default: $"+EnvAdapterAuthMode+" or header")
	cmd.Flags().StringVar(&f.trustDomain, "trust-domain", os.Getenv(EnvMTLSTrustDomain),
		"Optional platform SPIFFE trust domain override for adapter certificate enrollment; default: $"+EnvMTLSTrustDomain)
	cmd.Flags().StringVar(&f.authHeader, "auth-header", os.Getenv(agentadapter.EnvAuthHeader),
		"Static Authorization header value for runtime requests, e.g. \"Bearer <token>\" (default: $"+agentadapter.EnvAuthHeader+")")
	cmd.Flags().StringVar(&f.tlsClientCert, "tls-client-cert", os.Getenv(agentadapter.EnvTLSClientCert),
		"Path to PEM client certificate for mTLS to the runtime (default: $"+agentadapter.EnvTLSClientCert+")")
	cmd.Flags().StringVar(&f.tlsClientKey, "tls-client-key", os.Getenv(agentadapter.EnvTLSClientKey),
		"Path to PEM client key for mTLS to the runtime (default: $"+agentadapter.EnvTLSClientKey+")")
	cmd.Flags().StringVar(&f.tlsCABundle, "tls-ca-bundle", os.Getenv(agentadapter.EnvTLSCABundle),
		"Path to PEM CA bundle to verify the runtime's TLS certificate (default: $"+agentadapter.EnvTLSCABundle+")")
	cmd.Flags().BoolVar(&f.tlsInsecure, "tls-insecure", envTruthy(agentadapter.EnvTLSInsecure),
		"Skip verification of the runtime TLS certificate (local/Kind only; default: $"+agentadapter.EnvTLSInsecure+")")
}

// resolved holds the validated cross-cutting pieces of an adapter config —
// identity, runtime URL, transport, and the shared display fields — that
// every subcommand needs before building its transport-specific config.
type resolved struct {
	runtimeURL      *url.URL
	identity        agentadapter.Identity
	transport       *agentadapter.RuntimeTransport
	hostHeader      string
	protocolVersion string
	logLevel        string
}

// resolve parses and validates the shared adapter fields on the CLI side so
// error messages reference the user-facing flag name instead of the env var.
func (f identityFlags) resolve() (resolved, error) {
	out := resolved{
		identity: agentadapter.Identity{
			HumanID:   strings.TrimSpace(f.humanID),
			AgentID:   strings.TrimSpace(f.agentID),
			TeamID:    strings.TrimSpace(f.teamID),
			SessionID: strings.TrimSpace(f.sessionID),
		},
		hostHeader:      strings.TrimSpace(f.hostHeader),
		protocolVersion: strings.TrimSpace(f.protocolVersion),
		logLevel:        strings.TrimSpace(f.logLevel),
	}
	if out.protocolVersion == "" {
		out.protocolVersion = agentadapter.DefaultProtocolVersion
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
	if tlsCert != "" || tlsKey != "" || tlsCA != "" || f.tlsInsecure {
		tlsCfg, err := agentadapter.BuildTLSConfigWithOptions(tlsCert, tlsKey, tlsCA, f.tlsInsecure)
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
		Identity:          r.identity,
		Transport:         r.transport,
		HostHeader:        r.hostHeader,
		ListenAddr:        listen,
		ProtocolVersion:   r.protocolVersion,
		LogLevel:          r.logLevel,
		DisableXForwarded: f.disableXFF,
		MaxInboundBytes:   f.maxInboundBytes,
	}, nil
}

// toShimConfig produces an agentadapter.ShimConfig from the resolved shared
// fields plus stdio-only anonymous settings.
func (f identityFlags) toShimConfig() (agentadapter.ShimConfig, error) {
	r, err := f.resolve()
	if err != nil {
		return agentadapter.ShimConfig{}, err
	}
	cfg := agentadapter.ShimConfig{
		RuntimeURL:      r.runtimeURL,
		Identity:        r.identity,
		Transport:       r.transport,
		HostHeader:      r.hostHeader,
		ProtocolVersion: r.protocolVersion,
		LogLevel:        r.logLevel,
		Anonymous:       f.anonymous,
	}
	if f.anonymous && strings.TrimSpace(f.anonymousMethods) != "" {
		cfg.AnonymousMethods = agentadapter.SplitTrimmed(f.anonymousMethods, ",")
	}
	if raw := strings.TrimSpace(f.toolsCacheTTL); raw != "" {
		ttl, err := time.ParseDuration(raw)
		if err != nil {
			return agentadapter.ShimConfig{}, fmt.Errorf("--tools-cache-ttl (or $%s) is invalid: %w", agentadapter.EnvToolsCacheTTL, err)
		}
		if ttl < 0 {
			return agentadapter.ShimConfig{}, fmt.Errorf("--tools-cache-ttl (or $%s) must be zero or positive", agentadapter.EnvToolsCacheTTL)
		}
		cfg.ToolsCacheTTL = ttl
	}
	return cfg, nil
}

// bindStdioFlags adds stdio-specific flags on top of the shared identity flags.
func bindStdioFlags(cmd *cobra.Command, f *identityFlags) {
	cmd.Flags().BoolVar(&f.anonymous, "anonymous",
		parseEnvBoolSimple(agentadapter.EnvAnonymous),
		"Forward to the runtime without a session or issued identity (public/read-only routes); "+
			"only methods in --anonymous-methods are forwarded (default: $"+agentadapter.EnvAnonymous+")")
	cmd.Flags().StringVar(&f.anonymousMethods, "anonymous-methods",
		os.Getenv(agentadapter.EnvAnonymousMethods),
		"Comma-separated list of MCP methods allowed in anonymous mode "+
			"(default: $"+agentadapter.EnvAnonymousMethods+" or "+strings.Join(agentadapter.DefaultAnonymousMethods, ",")+")")
	cmd.Flags().StringVar(&f.toolsCacheTTL, "tools-cache-ttl",
		os.Getenv(agentadapter.EnvToolsCacheTTL),
		"Cache tools/list responses for this duration, e.g. 30s. Empty disables the cache. "+
			"(default: $"+agentadapter.EnvToolsCacheTTL+")")
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

func parseEnvBoolSimple(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "t", "true", "y", "yes", "on":
		return true
	default:
		return false
	}
}

// newHTTPTransportWithTLS delegates to the shared agentadapter helper so the
// env and flag paths produce identical transports.
func newHTTPTransportWithTLS(cfg *tls.Config) *http.Transport {
	return agentadapter.NewHTTPTransportWithTLS(cfg)
}
