package adapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"mcp-runtime/internal/agentadapter"
)

func TestReadProxyFileConfigRejectsSecretsInErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "adapter.yaml")
	secret := "super-secret-token-value"
	body := "authMode: header\nruntimeURL: https://mcp.example.com/pilot/mcp\ncredentialHeaders:\n  X-Example-Credential:\n    env: EXAMPLE_TOKEN\n    file: ./token\nunknown: " + secret + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := readProxyFileConfig(path)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("err=%v", err)
	}
}

func TestResolveHeaderConfigFlagOverridesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "adapter.yaml")
	body := "authMode: header\nruntimeURL: https://mcp.example.com/pilot/mcp\ncredentialHeaders:\n  X-Example-Credential:\n    file: token\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := newHeaderTestCommand()
	_, _, err := resolveHeaderConfig(cmd, identityFlags{runtimeURL: "https://mcp.example.com/other/mcp"}, headerConfigFlags{configFile: path})
	if err == nil {
		t.Fatal("URL override with file credentials was accepted")
	}
}

func TestHeaderConfigRelativeFileAndDuplicateEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "adapter.yaml")
	body := "authMode: header\nruntimeURL: https://mcp.example.com/pilot/mcp\ncredentialHeaders:\n  Private-Token:\n    file: token\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := newHeaderTestCommand()
	resolved, sources, err := resolveHeaderConfig(cmd, identityFlags{}, headerConfigFlags{configFile: path})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.authMode != agentadapter.AuthModeHeader {
		t.Fatalf("mode=%q", resolved.authMode)
	}
	if sources["Private-Token"].File != filepath.Join(dir, "token") {
		t.Fatalf("file=%q", sources["Private-Token"].File)
	}
	_, _, err = resolveHeaderConfig(cmd, identityFlags{}, headerConfigFlags{headerEnv: []string{"X-Example=ONE", "x-example=TWO"}})
	if err == nil {
		t.Fatal("duplicate header env was accepted")
	}
}

func newHeaderTestCommand() *cobra.Command {
	cmd := &cobra.Command{}
	var flags identityFlags
	var header headerConfigFlags
	bindIdentityFlags(cmd, &flags)
	bindHeaderConfigFlags(cmd, &flags, &header)
	return cmd
}
