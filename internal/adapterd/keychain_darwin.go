//go:build darwin

package adapterd

import (
	"fmt"
	"os/exec"
)

const keychainService = "org.mcpruntime.adapter"

// KeychainStore stores secrets in the login keychain via the security tool.
type KeychainStore struct{}

func NewKeychainStore() SecretStore { return KeychainStore{} }

func (KeychainStore) Put(account, secret string) error {
	cmd := exec.Command("security", "add-generic-password",
		"-U", "-s", keychainService, "-a", account, "-w", secret)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("keychain put: %w: %s", err, out)
	}
	return nil
}

func (KeychainStore) Get(account string) (string, error) {
	cmd := exec.Command("security", "find-generic-password",
		"-s", keychainService, "-a", account, "-w")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("keychain get %s: %w", account, err)
	}
	return string(trimNewline(out)), nil
}

func (KeychainStore) Delete(account string) error {
	cmd := exec.Command("security", "delete-generic-password",
		"-s", keychainService, "-a", account)
	_, err := cmd.CombinedOutput()
	return err
}

func trimNewline(b []byte) []byte {
	if len(b) > 0 && b[len(b)-1] == '\n' {
		b = b[:len(b)-1]
	}
	return b
}
