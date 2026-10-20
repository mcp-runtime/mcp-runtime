package adapterd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
}
