//go:build darwin

package adapterd

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeychainStorePutKeepsSecretOutOfArguments(t *testing.T) {
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args")
	stdinPath := filepath.Join(dir, "stdin")
	binary := filepath.Join(dir, "security")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$TEST_ARGS_PATH\"\ncat > \"$TEST_STDIN_PATH\"\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_ARGS_PATH", argsPath)
	t.Setenv("TEST_STDIN_PATH", stdinPath)
	oldBinary := securityBinary
	securityBinary = binary
	t.Cleanup(func() { securityBinary = oldBinary })
	store := KeychainStore{}
	const account = "adapter-test-account"
	const secret = "-----BEGIN TEST-----\ntest-only-secret-value\n-----END TEST-----\n"
	if err := store.Put(account, secret); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read captured args: %v", err)
	}
	if string(args) != "-q\n-i\n" {
		t.Fatalf("unexpected security args %q", args)
	}
	stdin, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "add-generic-password -U -s " + keychainService + " -a \"" + account + "\" -w \"" + base64.StdEncoding.EncodeToString([]byte(secret)) + "\"\n"
	if string(stdin) != want {
		t.Fatal("security stdin did not encode the multiline credential as one command")
	}
}

func TestKeychainStoreRejectsTruncatedOrInjectedCommands(t *testing.T) {
	for _, tc := range []struct{ account, secret string }{
		{"account", strings.Repeat("x", 4096)},
		{"account\ninjected-command", "test"},
	} {
		if err := (KeychainStore{}).Put(tc.account, tc.secret); err == nil {
			t.Fatal("expected invalid command input to be rejected")
		}
	}
}
