package adapterd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// SecretStore keeps per-profile credentials out of the profile catalog.
type SecretStore interface {
	Put(account, secret string) error
	Get(account string) (string, error)
	Delete(account string) error
}

// FileSecretStore is an owner-only JSON file. Tests and non-Keychain hosts use it.
type FileSecretStore struct {
	mu   sync.Mutex
	path string
}

func NewFileSecretStore(path string) *FileSecretStore {
	return &FileSecretStore{path: path}
}

func (s *FileSecretStore) Put(account, secret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	items, err := s.load()
	if err != nil {
		return err
	}
	items[account] = secret
	return s.save(items)
}

func (s *FileSecretStore) Get(account string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items, err := s.load()
	if err != nil {
		return "", err
	}
	v, ok := items[account]
	if !ok || v == "" {
		return "", fmt.Errorf("secret %q not found", account)
	}
	return v, nil
}

func (s *FileSecretStore) Delete(account string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	items, err := s.load()
	if err != nil {
		return err
	}
	delete(items, account)
	return s.save(items)
}

func (s *FileSecretStore) load() (map[string]string, error) {
	data, err := os.ReadFile(s.path) // #nosec G304 G703 -- configured local state file; account names are JSON keys, never filesystem paths.
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	items := map[string]string{}
	if len(data) == 0 {
		return items, nil
	}
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("read secrets: %w", err)
	}
	return items, nil
}

func (s *FileSecretStore) save(items map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil { // #nosec G703 -- directory comes from the daemon's local configuration, not MCP requests.
		return err
	}
	data, err := json.Marshal(items)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".adapter-secrets-*") // #nosec G703 -- owner-only configured state directory; random file is created exclusively.
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path) // #nosec G703 -- atomically replace only the configured local state file.
}

func tokenAccount(id string) string { return "local-token:" + id }
func certAccount(id string) string  { return "cert:" + id }
func keyAccount(id string) string   { return "key:" + id }
func caAccount(id string) string    { return "ca:" + id }
