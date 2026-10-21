//go:build darwin || linux

package adapter

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteOwnerOnlyConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.json")
	if err := writeOwnerOnlyConfig(path, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config mode %04o, want 0600", got)
	}
	if err := writeOwnerOnlyConfig(path, []byte("{}")); !os.IsExist(err) {
		t.Fatalf("second write error = %v, want file-exists error", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{}" {
		t.Fatalf("existing config was overwritten: %q", data)
	}
}

func TestLaunchdEnvPreservesConfiguredRuntimeDirectory(t *testing.T) {
	t.Setenv("MCP_RUNTIME_CONFIG_DIR", "/private/tmp/mcp-runtime-test")
	t.Setenv("MCP_RUNTIME_ADAPTER_FILE_SECRETS", "1")
	got := launchdEnv()
	want := map[string]string{
		"MCP_RUNTIME_CONFIG_DIR":           "/private/tmp/mcp-runtime-test",
		"MCP_RUNTIME_ADAPTER_FILE_SECRETS": "1",
	}
	if len(got) != len(want) {
		t.Fatalf("launchd environment = %#v, want %#v", got, want)
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("launchd environment %s = %q, want %q", key, got[key], value)
		}
	}
}

func TestServiceReinstallFromManagedBinaryPreservesExecutable(t *testing.T) {
	home := t.TempDir()
	source := filepath.Join(home, "source-cli")
	const binaryContents = "test executable contents"
	if err := os.WriteFile(source, []byte(binaryContents), 0o755); err != nil {
		t.Fatal(err)
	}
	managed, err := installServiceBinary(source, home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := installServiceBinary(managed, home); err != nil {
		t.Fatalf("reinstall from the running executable: %v", err)
	}
	if _, err := installServiceBinary(filepath.Join(home, "missing-cli"), home); err == nil {
		t.Fatal("expected missing source failure")
	}
	got, err := os.ReadFile(managed)
	if err != nil || string(got) != binaryContents {
		t.Fatalf("managed executable was corrupted: content=%q error=%v", got, err)
	}
	info, err := os.Stat(managed)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("managed executable permissions: info=%v error=%v", info, err)
	}
}
