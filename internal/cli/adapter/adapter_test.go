package adapter

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/pkg/certauth"
)

func TestAdapterCommandRegistersOnlyProxyAndEnroll(t *testing.T) {
	t.Parallel()

	cmd := New(core.NewRuntime(nil))
	subs := map[string]bool{}
	for _, child := range cmd.Commands() {
		subs[child.Use] = true
	}
	if len(subs) != 2 {
		t.Fatalf("unexpected adapter commands: %v", subs)
	}
	for _, want := range []string{"proxy", "enroll"} {
		if !subs[want] {
			t.Fatalf("adapter command missing %q subcommand; got %v", want, subs)
		}
	}
}

func TestAdapterRejectsRemovedStdioCommand(t *testing.T) {
	cmd := New(core.NewRuntime(nil))
	cmd.SetArgs([]string{"stdio"})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("Execute(stdio) = %v, want unknown command", err)
	}
}

func TestEnsureScopedCertDirStaysUnderConfig(t *testing.T) {
	t.Parallel()

	configDir := t.TempDir()
	var scopeHash [32]byte
	for i := range scopeHash {
		scopeHash[i] = byte(i)
	}

	dir, err := ensureScopedCertDir(configDir, scopeHash)
	if err != nil {
		t.Fatalf("ensureScopedCertDir() error = %v", err)
	}
	rel, err := filepath.Rel(configDir, dir)
	if err != nil {
		t.Fatalf("Rel() error = %v", err)
	}
	if !strings.HasPrefix(rel, "certs"+string(filepath.Separator)) {
		t.Fatalf("cert dir %q is not under %q/certs", dir, configDir)
	}
	if strings.Contains(rel, "..") {
		t.Fatalf("cert dir %q escapes config root", dir)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat(%q) error = %v", dir, err)
	}
	if !info.IsDir() {
		t.Fatalf("cert path %q is not a directory", dir)
	}
	if perm := info.Mode().Perm(); perm&0o700 != 0o700 {
		t.Fatalf("cert dir mode = %o, want owner rwx", perm)
	}

	// Nested OpenRoot confinement rejects path components that escape the config root.
	root, err := os.OpenRoot(configDir)
	if err != nil {
		t.Fatalf("OpenRoot() error = %v", err)
	}
	defer root.Close()
	if err := root.MkdirAll(filepath.Join("..", "escape"), 0o700); err == nil {
		t.Fatal("OpenRoot.MkdirAll allowed path traversal outside configDir")
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

func TestProxyCommandRequiresCertificateEnrollment(t *testing.T) {
	t.Parallel()

	cmd := New(core.NewRuntime(nil))
	cmd.SetArgs([]string{"proxy", "--runtime-url", "https://localhost:18080/demo/mcp"})
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	cmd.SetOut(&stderr)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want missing session error")
	}
	if !strings.Contains(err.Error(), "MCP_RUNTIME_ADAPTER_SERVER") {
		t.Fatalf("Execute() error = %q, want certificate enrollment error", err)
	}
}

func TestProxyCommandRejectsBadRuntimeURL(t *testing.T) {
	t.Parallel()

	cmd := New(core.NewRuntime(nil))
	cmd.SetArgs([]string{"proxy", "--runtime-url", "file:///etc/passwd"})
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

func TestIdentityFlagsToProxyConfigParsesTimeout(t *testing.T) {
	t.Parallel()

	flags := identityFlags{
		runtimeURL:     "http://localhost:18080/demo/mcp",
		requestTimeout: "45s",
	}
	cfg, err := flags.toProxyConfig("")
	if err != nil {
		t.Fatalf("toProxyConfig() error = %v", err)
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
