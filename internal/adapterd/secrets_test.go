package adapterd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileSecretsIgnorePredictableTemporarySymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.json")
	victim := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, path+".tmp"); err != nil {
		t.Fatal(err)
	}
	store := NewFileSecretStore(path)
	if err := store.Put("profile", "test-secret"); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Get("profile"); err != nil || got != "test-secret" {
		t.Fatalf("stored secret unavailable: %v", err)
	}
	data, err := os.ReadFile(victim)
	if err != nil || string(data) != "keep" {
		t.Fatal("predictable temporary symlink target was modified")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("secret file must be owner-only")
	}
}
