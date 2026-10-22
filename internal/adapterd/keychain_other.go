//go:build !darwin

package adapterd

import "fmt"

// NewKeychainStore is unavailable off macOS. Callers use FileSecretStore.
func NewKeychainStore() SecretStore {
	return unsupportedSecrets{}
}

type unsupportedSecrets struct{}

func (unsupportedSecrets) Put(string, string) error {
	return fmt.Errorf("macOS Keychain is unavailable on this operating system")
}
func (unsupportedSecrets) Get(string) (string, error) {
	return "", fmt.Errorf("macOS Keychain is unavailable on this operating system")
}
func (unsupportedSecrets) Delete(string) error {
	return fmt.Errorf("macOS Keychain is unavailable on this operating system")
}
