//go:build darwin

package adapterd

import (
	"encoding/base64"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

const keychainService = "org.mcpruntime.adapter"

var securityBinary = "/usr/bin/security"

// KeychainStore stores secrets in the login keychain via the security tool.
type KeychainStore struct{ keychainPath string }

func NewKeychainStore() SecretStore { return KeychainStore{} }

func (s KeychainStore) Put(account, secret string) error {
	// Send one command over stdin: the password prompt reads and confirms only
	// one line, so it cannot preserve multiline PEM. Base64 avoids command
	// injection and keeps the credential out of the process argument list.
	if strings.ContainsAny(account+s.keychainPath, "\r\n\x00") {
		return fmt.Errorf("keychain account and path must not contain control characters")
	}
	command := "add-generic-password -U -s " + keychainService + " -a " + strconv.Quote(account) + " -w " + strconv.Quote(base64.StdEncoding.EncodeToString([]byte(secret)))
	if s.keychainPath != "" {
		command += " " + strconv.Quote(s.keychainPath)
	}
	// Apple's SecurityTool/macOS/security.c has a 4096-byte line buffer.
	// Reject oversize input before spawning rather than truncating a private key
	// or treating its remainder as another command.
	if len(command)+1 >= 4096 {
		return fmt.Errorf("credential exceeds the macOS security input limit; use MCP_RUNTIME_ADAPTER_FILE_SECRETS=1 for an owner-protected file store")
	}
	cmd := exec.Command(securityBinary, "-q", "-i")
	cmd.Stdin = strings.NewReader(command + "\n")
	if err := cmd.Run(); err != nil {
		// Interactive security errors can echo command input. Never return it.
		return fmt.Errorf("keychain put: %w", err)
	}
	return nil
}

func (s KeychainStore) Get(account string) (string, error) {
	args := []string{"find-generic-password", "-s", keychainService, "-a", account, "-w"}
	if s.keychainPath != "" {
		args = append(args, s.keychainPath)
	}
	cmd := exec.Command(securityBinary, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("keychain get %s: %w", account, err)
	}
	decoded, err := base64.StdEncoding.DecodeString(string(trimNewline(out)))
	if err != nil {
		return "", fmt.Errorf("keychain credential encoding is invalid")
	}
	return string(decoded), nil
}

func (s KeychainStore) Delete(account string) error {
	args := []string{"delete-generic-password", "-s", keychainService, "-a", account}
	if s.keychainPath != "" {
		args = append(args, s.keychainPath)
	}
	cmd := exec.Command(securityBinary, args...)
	_, err := cmd.CombinedOutput()
	return err
}

func trimNewline(b []byte) []byte {
	if len(b) > 0 && b[len(b)-1] == '\n' {
		b = b[:len(b)-1]
	}
	return b
}
