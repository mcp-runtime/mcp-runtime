package adapterd

import (
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaunchdEscapesConfiguredPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.plist")
	binary := "/Applications/Adapter & <Tools>/mcp-runtime"
	directory := "/tmp/adapter & <profile>"
	if err := WriteLaunchdPlist(path, binary, map[string]string{"MCP_RUNTIME_CONFIG_DIR": directory}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	values := map[string]bool{}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("generated plist is invalid XML: %v", err)
		}
		if text, ok := token.(xml.CharData); ok {
			values[string(text)] = true
		}
	}
	if !values[binary] || !values[directory] {
		t.Fatal("plist did not preserve escaped binary and config paths")
	}
}

func TestLaunchdPlistContainsBinary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.plist")
	if err := WriteLaunchdPlist(path, "/usr/local/bin/mcp-runtime", nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "/usr/local/bin/mcp-runtime") || !strings.Contains(text, "KeepAlive") {
		t.Fatalf("plist %s", text)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
}
