package adapter

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/pkg/certauth"
)

func TestAdapterCommandRegistersProxyAndStdio(t *testing.T) {
	t.Parallel()

	cmd := New(core.NewRuntime(nil))
	subs := map[string]bool{}
	for _, child := range cmd.Commands() {
		subs[child.Use] = true
	}
	for _, want := range []string{"proxy", "stdio"} {
		if !subs[want] {
			t.Fatalf("adapter command missing %q subcommand; got %v", want, subs)
		}
	}
}

func TestWriteCredentialFileUsesOutputRoot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := certauth.WritePrivateFile(dir, "client.key", []byte("secret"), 0o600); err != nil {
		t.Fatalf("WritePrivateFile() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "client.key"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "secret" {
		t.Fatalf("credential data = %q, want secret", string(data))
	}
	if err := certauth.WritePrivateFile(dir, "../escape", []byte("nope"), 0o600); err == nil {
		t.Fatal("WritePrivateFile() allowed path traversal")
	}
}

func TestProxyCommandValidatesIdentity(t *testing.T) {
	t.Parallel()

	cmd := New(core.NewRuntime(nil))
	cmd.SetArgs([]string{"proxy", "--runtime-url", "http://localhost:18080/demo/mcp", "--human-id", "h", "--agent-id", "a"})
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	cmd.SetOut(&stderr)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want missing session error")
	}
	if !strings.Contains(err.Error(), "MCP_RUNTIME_SESSION_ID") {
		t.Fatalf("Execute() error = %q, want session validation error", err)
	}
}

func TestProxyCommandRejectsBadRuntimeURL(t *testing.T) {
	t.Parallel()

	cmd := New(core.NewRuntime(nil))
	cmd.SetArgs([]string{"proxy", "--runtime-url", "file:///etc/passwd", "--human-id", "h", "--agent-id", "a", "--session-id", "s"})
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	cmd.SetOut(&stderr)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want URL scheme error")
	}
	if !strings.Contains(err.Error(), "http or https") && !strings.Contains(err.Error(), "absolute HTTP URL") {
		t.Fatalf("Execute() error = %q, want URL scheme error", err)
	}
}

func TestIdentityFlagsToProxyConfigParsesTeamAndTimeout(t *testing.T) {
	t.Parallel()

	flags := identityFlags{
		runtimeURL:     "http://localhost:18080/demo/mcp",
		humanID:        "human-1",
		agentID:        "agent-1",
		teamID:         "team-acme",
		sessionID:      "sess-1",
		requestTimeout: "45s",
	}
	cfg, err := flags.toProxyConfig("")
	if err != nil {
		t.Fatalf("toProxyConfig() error = %v", err)
	}
	if cfg.Identity.TeamID != "team-acme" {
		t.Fatalf("Identity.TeamID = %q, want team-acme", cfg.Identity.TeamID)
	}
	if cfg.Transport == nil || cfg.Transport.Timeout != 45*time.Second {
		t.Fatalf("Transport = %v, want timeout 45s", cfg.Transport)
	}
	if cfg.RuntimeURL == nil || cfg.RuntimeURL.Host != "localhost:18080" {
		t.Fatalf("RuntimeURL = %v, want parsed localhost:18080", cfg.RuntimeURL)
	}
}

func TestIdentityFlagsToConfigRejectsBadTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "missing unit", value: "30", want: "is invalid"},
		{name: "zero", value: "0s", want: "greater than zero"},
		{name: "negative", value: "-5s", want: "greater than zero"},
		{name: "garbage", value: "soon", want: "is invalid"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			flags := identityFlags{
				runtimeURL:     "http://localhost:18080/demo/mcp",
				humanID:        "h",
				agentID:        "a",
				sessionID:      "s",
				requestTimeout: tt.value,
			}
			_, err := flags.toProxyConfig("")
			if err == nil {
				t.Fatalf("toProxyConfig() error = nil, want %s", tt.want)
			}
			if !strings.Contains(err.Error(), "request-timeout") || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("toProxyConfig() error = %q, want request-timeout/%s", err, tt.want)
			}
		})
	}
}

func TestIdentityFlagsTLSInsecureRequiresLoopbackRuntime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		runtimeURL string
		wantErr    bool
	}{
		{name: "localhost", runtimeURL: "https://localhost:18080/mcp"},
		{name: "ipv4 loopback", runtimeURL: "https://127.0.0.1:18080/mcp"},
		{name: "ipv6 loopback", runtimeURL: "https://[::1]:18080/mcp"},
		{name: "remote host", runtimeURL: "https://platform.mcpruntime.org/mcp", wantErr: true},
		{name: "missing URL", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			flags := identityFlags{runtimeURL: tt.runtimeURL, tlsInsecure: true}
			cfg, err := flags.toProxyConfig("")
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "restricted to loopback") {
					t.Fatalf("toProxyConfig() error = %v, want loopback restriction", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("toProxyConfig() error = %v", err)
			}
			transport, ok := cfg.Transport.Base.(*http.Transport)
			if !ok || transport.TLSClientConfig == nil || !transport.TLSClientConfig.InsecureSkipVerify {
				t.Fatalf("Transport TLS config = %#v, want loopback TLS verification opt-out", cfg.Transport)
			}
		})
	}
}
