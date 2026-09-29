package agentadapter

import (
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadProxyConfigRequiresRuntimeURL(t *testing.T) {
	t.Parallel()

	_, err := loadProxyConfig(func(string) string { return "" })
	if err == nil {
		t.Fatal("loadProxyConfig() error = nil, want missing environment error")
	}
	if !strings.Contains(err.Error(), EnvRuntimeURL) {
		t.Fatalf("loadProxyConfig() error = %q, missing %s", err, EnvRuntimeURL)
	}
}

func TestProxyConfigRequiresClientCertificate(t *testing.T) {
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
	cfg.CertificateIdentity = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want certificate config accepted", err)
	}
}

func TestLoadProxyConfigRejectsNonHTTPRuntimeURL(t *testing.T) {
	t.Parallel()

	env := map[string]string{
		EnvRuntimeURL: "file:///tmp/mcp.sock",
		EnvHumanID:    "human-1",
		EnvAgentID:    "agent-1",
		EnvSessionID:  "session-1",
	}
	_, err := loadProxyConfig(func(key string) string { return env[key] })
	if err == nil {
		t.Fatal("loadProxyConfig() error = nil, want URL scheme error")
	}
	if !strings.Contains(err.Error(), "must be an absolute HTTP URL") && !strings.Contains(err.Error(), "must use http or https") {
		t.Fatalf("loadProxyConfig() error = %q, want HTTP URL error", err)
	}
}

func TestLoadProxyConfigAppliesDefaults(t *testing.T) {
	t.Parallel()

	env := map[string]string{
		EnvRuntimeURL: "http://localhost:18080/demo/mcp",
		EnvHumanID:    "human-1",
		EnvAgentID:    "agent-1",
		EnvSessionID:  "session-1",
	}
	cfg, err := loadProxyConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("loadProxyConfig() error = %v", err)
	}
	if cfg.ListenAddr != DefaultListenAddr {
		t.Fatalf("ListenAddr = %q, want %q", cfg.ListenAddr, DefaultListenAddr)
	}
	if cfg.ProtocolVersion != DefaultProtocolVersion {
		t.Fatalf("ProtocolVersion = %q, want %q", cfg.ProtocolVersion, DefaultProtocolVersion)
	}
	if cfg.DisableXForwarded {
		t.Fatal("DisableXForwarded = true, want default proxy X-Forwarded headers enabled")
	}
	if cfg.Transport != nil {
		t.Fatalf("Transport = %#v, want nil so adapters get the default RuntimeTransport", cfg.Transport)
	}
	if cfg.LogLevel != "" {
		t.Fatalf("LogLevel = %q, want empty default", cfg.LogLevel)
	}
}

func TestLoadConfigParsesOptionalTeamID(t *testing.T) {
	t.Parallel()

	env := map[string]string{
		EnvRuntimeURL: "http://localhost:18080/demo/mcp",
		EnvHumanID:    "human-1",
		EnvAgentID:    "agent-1",
		EnvTeamID:     "team-acme",
		EnvSessionID:  "session-1",
	}
	cfg, err := loadProxyConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("loadProxyConfig() error = %v", err)
	}
	if cfg.Identity.TeamID != "team-acme" {
		t.Fatalf("Identity.TeamID = %q, want team-acme", cfg.Identity.TeamID)
	}
}

func TestLoadConfigTeamIDIsOptional(t *testing.T) {
	t.Parallel()

	env := map[string]string{
		EnvRuntimeURL: "http://localhost:18080/demo/mcp",
		EnvHumanID:    "human-1",
		EnvAgentID:    "agent-1",
		EnvSessionID:  "session-1",
	}
	cfg, err := loadProxyConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("loadProxyConfig() error = %v", err)
	}
	if cfg.Identity.TeamID != "" {
		t.Fatalf("Identity.TeamID = %q, want empty default", cfg.Identity.TeamID)
	}
}

func TestLoadConfigParsesOptionalRuntimeControls(t *testing.T) {
	t.Parallel()

	env := map[string]string{
		EnvRuntimeURL:     "http://localhost:18080/demo/mcp",
		EnvHumanID:        "human-1",
		EnvAgentID:        "agent-1",
		EnvSessionID:      "session-1",
		EnvSetXForwarded:  "false",
		EnvRequestTimeout: "300s",
		EnvLogLevel:       "info",
	}
	cfg, err := loadProxyConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("loadProxyConfig() error = %v", err)
	}
	if !cfg.DisableXForwarded {
		t.Fatal("DisableXForwarded = false, want true when MCP_RUNTIME_SET_XFF=false")
	}
	if cfg.Transport == nil || cfg.Transport.Timeout != 300*time.Second {
		t.Fatalf("Transport.Timeout = %v, want 300s", cfg.Transport)
	}
	if cfg.LogLevel != "info" {
		t.Fatalf("LogLevel = %q, want info", cfg.LogLevel)
	}
}

func TestLoadConfigRejectsInvalidRuntimeControls(t *testing.T) {
	t.Parallel()

	baseEnv := map[string]string{
		EnvRuntimeURL: "http://localhost:18080/demo/mcp",
		EnvHumanID:    "human-1",
		EnvAgentID:    "agent-1",
		EnvSessionID:  "session-1",
	}
	tests := []struct {
		name string
		key  string
		val  string
	}{
		{name: "set xff", key: EnvSetXForwarded, val: "maybe"},
		{name: "request timeout", key: EnvRequestTimeout, val: "0s"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := map[string]string{}
			for key, val := range baseEnv {
				env[key] = val
			}
			env[tt.key] = tt.val
			_, err := loadProxyConfig(func(key string) string { return env[key] })
			if err == nil {
				t.Fatal("loadProxyConfig() error = nil, want invalid runtime control error")
			}
			if !strings.Contains(err.Error(), tt.key) {
				t.Fatalf("loadProxyConfig() error = %q, want %s", err, tt.key)
			}
		})
	}
}

func TestBuildTLSConfigLoadsCABundle(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
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

func TestParseNonNegativeBytes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{in: "0", want: 0},
		{in: "1024", want: 1024},
		{in: "-1", wantErr: true},
		{in: "abc", wantErr: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			got, err := parseNonNegativeBytes(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseNonNegativeBytes(%q) error = %v, wantErr = %v", tt.in, err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Fatalf("parseNonNegativeBytes(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}
