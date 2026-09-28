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
	"strconv"
	"strings"
	"time"
)

const (
	EnvRuntimeURL       = "MCP_RUNTIME_URL"
	EnvHumanID          = "MCP_RUNTIME_HUMAN_ID"
	EnvAgentID          = "MCP_RUNTIME_AGENT_ID"
	EnvTeamID           = "MCP_RUNTIME_TEAM_ID"
	EnvSessionID        = "MCP_RUNTIME_SESSION_ID"
	EnvHostHeader       = "MCP_RUNTIME_HOST_HEADER"
	EnvListenAddr       = "MCP_RUNTIME_LISTEN_ADDR"
	EnvProtocolVersion  = "MCP_RUNTIME_PROTOCOL_VERSION"
	EnvSetXForwarded    = "MCP_RUNTIME_SET_XFF"
	EnvRequestTimeout   = "MCP_RUNTIME_REQUEST_TIMEOUT"
	EnvLogLevel         = "MCP_RUNTIME_LOG_LEVEL"
	EnvAnonymous        = "MCP_RUNTIME_ANONYMOUS"
	EnvAnonymousMethods = "MCP_RUNTIME_ANONYMOUS_METHODS"
	EnvAuthHeader       = "MCP_RUNTIME_AUTH_HEADER"
	EnvTLSClientCert    = "MCP_RUNTIME_TLS_CLIENT_CERT"
	EnvTLSClientKey     = "MCP_RUNTIME_TLS_CLIENT_KEY"
	EnvTLSCABundle      = "MCP_RUNTIME_TLS_CA_BUNDLE"
	EnvMaxInboundBytes  = "MCP_RUNTIME_MAX_INBOUND_BYTES"
	EnvToolsCacheTTL    = "MCP_RUNTIME_TOOLS_CACHE_TTL"

	DefaultListenAddr      = "127.0.0.1:8099"
	DefaultProtocolVersion = "2025-06-18"

	MCPProtocolHeader = "Mcp-Protocol-Version"
	MCPSessionHeader  = "Mcp-Session-Id"
)

type envLookup func(string) string

// ProxyConfig configures the local HTTP reverse-proxy adapter that exposes
// Streamable HTTP MCP to an agent SDK.
type ProxyConfig struct {
	RuntimeURL        *url.URL
	Identity          Identity
	Transport         *RuntimeTransport
	HostHeader        string
	ListenAddr        string
	ProtocolVersion   string
	LogLevel          string
	LogWriter         io.Writer
	DisableXForwarded bool
	// MaxInboundBytes caps the size of JSON-RPC request bodies the proxy
	// buffers when capturing metadata. Zero (or negative) means use
	// DefaultMaxInboundBytes (16 MiB). Over-cap requests respond with 413.
	MaxInboundBytes int64
	// MetricsHandler, when set, is served at /metrics. Typical use: a
	// Prometheus exporter wired to the OTel MeterProvider that backs
	// RuntimeTransport.Meter. Nil → /metrics returns 404.
	MetricsHandler http.Handler
	// IdentityProvider overrides Identity per-request when set. Used by
	// callers that rotate identity at runtime (e.g. auto-refreshed
	// platform-issued adapter sessions). Nil → static Identity is used.
	IdentityProvider IdentityProvider
}

// ShimConfig configures the stdio adapter that bridges newline-delimited
// JSON-RPC MCP traffic to the runtime over HTTP.
type ShimConfig struct {
	RuntimeURL      *url.URL
	Identity        Identity
	Transport       *RuntimeTransport
	HostHeader      string
	ProtocolVersion string
	LogLevel        string
	LogWriter       io.Writer
	// Anonymous, when true, relaxes identity validation so the shim can forward
	// to public/read-only runtime routes without a session or human/agent ID.
	// Only methods in AnonymousMethods are forwarded; all others are rejected
	// with a JSON-RPC error before reaching the runtime.
	Anonymous bool
	// AnonymousMethods is the allowlist used when Anonymous is true. When empty
	// the DefaultAnonymousMethods list applies.
	AnonymousMethods []string
	// ToolsCacheTTL enables a process-local tools/list response cache when
	// set to a positive duration. Zero (or negative) disables the cache.
	// Entries are keyed by identity + runtime URL and invalidated on a
	// tools/list_changed notification or when the TTL expires.
	ToolsCacheTTL time.Duration
	// IdentityProvider overrides Identity per-request when set.
	// See ProxyConfig.IdentityProvider for the contract.
	IdentityProvider IdentityProvider
}

// DefaultAnonymousMethods is the set of MCP methods the stdio shim allows in
// anonymous mode when no explicit AnonymousMethods list is configured. These
// are read-only discovery methods and the protocol handshake (initialize for
// legacy revisions, server/discover for 2026-07-28 and later).
var DefaultAnonymousMethods = []string{
	"initialize",
	"notifications/initialized",
	"server/discover",
	"ping",
	"tools/list",
	"resources/list",
	"prompts/list",
}

// LoadProxyConfigFromEnv loads HTTP proxy configuration from environment
// variables.
func LoadProxyConfigFromEnv() (ProxyConfig, error) { return loadProxyConfig(os.Getenv) }

// LoadShimConfigFromEnv loads stdio shim configuration from environment
// variables.
func LoadShimConfigFromEnv() (ShimConfig, error) { return loadShimConfig(os.Getenv) }

func loadProxyConfig(lookup envLookup) (ProxyConfig, error) {
	parsed, err := parseSharedEnv(lookup)
	if err != nil {
		return ProxyConfig{}, err
	}
	cfg := ProxyConfig{
		RuntimeURL:      parsed.runtimeURL,
		Identity:        parsed.identity,
		Transport:       parsed.transport,
		HostHeader:      parsed.hostHeader,
		ProtocolVersion: parsed.protocolVersion,
		LogLevel:        parsed.logLevel,
		ListenAddr:      strings.TrimSpace(lookup(EnvListenAddr)),
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = DefaultListenAddr
	}
	if raw := strings.TrimSpace(lookup(EnvSetXForwarded)); raw != "" {
		setXForwarded, err := parseAdapterBool(raw)
		if err != nil {
			return ProxyConfig{}, fmt.Errorf("%s is invalid: %w", EnvSetXForwarded, err)
		}
		cfg.DisableXForwarded = !setXForwarded
	}
	if raw := strings.TrimSpace(lookup(EnvMaxInboundBytes)); raw != "" {
		n, err := parseNonNegativeBytes(raw)
		if err != nil {
			return ProxyConfig{}, fmt.Errorf("%s is invalid: %w", EnvMaxInboundBytes, err)
		}
		cfg.MaxInboundBytes = n
	}
	if err := cfg.Validate(); err != nil {
		return ProxyConfig{}, err
	}
	return cfg, nil
}

func loadShimConfig(lookup envLookup) (ShimConfig, error) {
	parsed, err := parseSharedEnv(lookup)
	if err != nil {
		return ShimConfig{}, err
	}
	cfg := ShimConfig{
		RuntimeURL:      parsed.runtimeURL,
		Identity:        parsed.identity,
		Transport:       parsed.transport,
		HostHeader:      parsed.hostHeader,
		ProtocolVersion: parsed.protocolVersion,
		LogLevel:        parsed.logLevel,
	}
	if raw := strings.TrimSpace(lookup(EnvAnonymous)); raw != "" {
		anon, err := parseAdapterBool(raw)
		if err != nil {
			return ShimConfig{}, fmt.Errorf("%s is invalid: %w", EnvAnonymous, err)
		}
		cfg.Anonymous = anon
	}
	if raw := strings.TrimSpace(lookup(EnvAnonymousMethods)); raw != "" {
		cfg.AnonymousMethods = SplitTrimmed(raw, ",")
	}
	if raw := strings.TrimSpace(lookup(EnvToolsCacheTTL)); raw != "" {
		ttl, err := time.ParseDuration(raw)
		if err != nil {
			return ShimConfig{}, fmt.Errorf("%s is invalid: %w", EnvToolsCacheTTL, err)
		}
		cfg.ToolsCacheTTL = ttl
	}
	if err := cfg.Validate(); err != nil {
		return ShimConfig{}, err
	}
	return cfg, nil
}

// parseNonNegativeBytes parses an int64 byte size from a string. Zero is
// allowed and signals "use the default" to callers; negative values or
// unparseable input return an error.
func parseNonNegativeBytes(s string) (int64, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("expected non-negative integer bytes, got %q", s)
	}
	if n < 0 {
		return 0, fmt.Errorf("must be zero or positive")
	}
	return n, nil
}

// Validate enforces the runtime identity invariants for the HTTP proxy.
func (cfg ProxyConfig) Validate() error {
	return validateRequiredIdentity(cfg.RuntimeURL, cfg.Identity)
}

// Validate enforces the runtime identity invariants for the stdio shim.
// In anonymous mode only the runtime URL is required.
func (cfg ShimConfig) Validate() error {
	if cfg.Anonymous {
		if cfg.RuntimeURL == nil {
			return fmt.Errorf("missing required environment variable: %s", EnvRuntimeURL)
		}
		return nil
	}
	return validateRequiredIdentity(cfg.RuntimeURL, cfg.Identity)
}

// transportOrDefault returns the configured transport, allocating a default
// (no base, no timeout) when the caller did not provide one.
func (cfg ProxyConfig) transportOrDefault() *RuntimeTransport {
	if cfg.Transport != nil {
		return cfg.Transport
	}
	return &RuntimeTransport{}
}

type sharedEnv struct {
	runtimeURL      *url.URL
	identity        Identity
	transport       *RuntimeTransport
	hostHeader      string
	protocolVersion string
	logLevel        string
}

func parseSharedEnv(lookup envLookup) (sharedEnv, error) {
	out := sharedEnv{
		identity: Identity{
			HumanID:   strings.TrimSpace(lookup(EnvHumanID)),
			AgentID:   strings.TrimSpace(lookup(EnvAgentID)),
			TeamID:    strings.TrimSpace(lookup(EnvTeamID)),
			SessionID: strings.TrimSpace(lookup(EnvSessionID)),
		},
		hostHeader:      strings.TrimSpace(lookup(EnvHostHeader)),
		protocolVersion: strings.TrimSpace(lookup(EnvProtocolVersion)),
		logLevel:        strings.TrimSpace(lookup(EnvLogLevel)),
	}
	if out.protocolVersion == "" {
		out.protocolVersion = DefaultProtocolVersion
	}

	if raw := strings.TrimSpace(lookup(EnvRuntimeURL)); raw != "" {
		parsed, err := url.Parse(raw)
		if err != nil {
			return sharedEnv{}, fmt.Errorf("%s is invalid: %w", EnvRuntimeURL, err)
		}
		if parsed.Scheme == "" || parsed.Host == "" {
			return sharedEnv{}, fmt.Errorf("%s must be an absolute HTTP URL", EnvRuntimeURL)
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return sharedEnv{}, fmt.Errorf("%s must use http or https", EnvRuntimeURL)
		}
		out.runtimeURL = parsed
	}
	if raw := strings.TrimSpace(lookup(EnvRequestTimeout)); raw != "" {
		timeout, err := time.ParseDuration(raw)
		if err != nil {
			return sharedEnv{}, fmt.Errorf("%s is invalid: %w", EnvRequestTimeout, err)
		}
		if timeout <= 0 {
			return sharedEnv{}, fmt.Errorf("%s must be greater than zero", EnvRequestTimeout)
		}
		out.transport = &RuntimeTransport{Timeout: timeout}
	}

	// Auth header.
	if raw := strings.TrimSpace(lookup(EnvAuthHeader)); raw != "" {
		if out.transport == nil {
			out.transport = &RuntimeTransport{}
		}
		out.transport.AuthHeader = raw
	}

	// Optional mTLS: client cert/key and/or custom CA bundle.
	tlsCert := strings.TrimSpace(lookup(EnvTLSClientCert))
	tlsKey := strings.TrimSpace(lookup(EnvTLSClientKey))
	tlsCA := strings.TrimSpace(lookup(EnvTLSCABundle))
	if tlsCert != "" || tlsKey != "" || tlsCA != "" {
		tlsCfg, err := BuildTLSConfig(tlsCert, tlsKey, tlsCA)
		if err != nil {
			return sharedEnv{}, err
		}
		if out.transport == nil {
			out.transport = &RuntimeTransport{}
		}
		out.transport.Base = NewHTTPTransportWithTLS(tlsCfg)
	}

	return out, nil
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

func validateRequiredIdentity(runtimeURL *url.URL, id Identity) error {
	var missing []string
	if runtimeURL == nil {
		missing = append(missing, EnvRuntimeURL)
	}
	if strings.TrimSpace(id.HumanID) == "" {
		missing = append(missing, EnvHumanID)
	}
	if strings.TrimSpace(id.AgentID) == "" {
		missing = append(missing, EnvAgentID)
	}
	if strings.TrimSpace(id.SessionID) == "" {
		missing = append(missing, EnvSessionID)
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}
	return nil
}

func parseAdapterBool(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "t", "true", "y", "yes", "on":
		return true, nil
	case "0", "f", "false", "n", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("expected true or false")
	}
}

func cloneURL(in *url.URL) *url.URL {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func SplitTrimmed(s, sep string) []string {
	parts := strings.Split(s, sep)
	out := parts[:0]
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
