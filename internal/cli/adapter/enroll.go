package adapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/platformapi"
	"mcp-runtime/pkg/authfile"
	"mcp-runtime/pkg/certauth"
)

func newEnrollCmd(_ *core.Runtime) *cobra.Command {
	var flags platformSessionFlags
	var trustDomain string

	cmd := &cobra.Command{
		Use:   "enroll",
		Short: "Issue platform-managed mTLS files for an external adapter",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !flags.enabled() || strings.TrimSpace(flags.agent) == "" {
				return fmt.Errorf("--server and --agent are required")
			}
			if u := strings.TrimSpace(flags.platformURL); u != "" {
				if err := os.Setenv(EnvPlatformURL, u); err != nil {
					return err
				}
			}
			client, err := platformapi.NewPlatformClient()
			if err != nil {
				return err
			}
			return enrollAdapterCertificate(cmd.Context(), client, flags, trustDomain, cmd.OutOrStdout())
		},
	}
	bindPlatformSessionFlags(cmd, &flags)
	_ = cmd.Flags().MarkHidden("auto-refresh")
	cmd.Flags().StringVar(&trustDomain, "trust-domain", os.Getenv(EnvMTLSTrustDomain), "Optional platform SPIFFE trust domain override; defaults to the domain returned by the platform")
	return cmd
}

func enrollAdapterCertificate(ctx context.Context, client *platformapi.PlatformClient, flags platformSessionFlags, trustDomain string, out interface{ Write([]byte) (int, error) }) error {
	cred, err := issueAdapterCredential(ctx, client, flags, trustDomain)
	if err != nil {
		return err
	}

	configDir, err := authfile.ConfigDir()
	if err != nil {
		return fmt.Errorf("resolve MCP Runtime config directory: %w", err)
	}
	scope := strings.Join([]string{strings.TrimSpace(os.Getenv(EnvPlatformURL)), cred.SPIFFEID, cred.Session.Namespace, cred.Session.ServerName, cred.Session.AgentID}, "\x00")
	scopeHash := sha256.Sum256([]byte(scope))
	dir, err := ensureScopedCertDir(configDir, scopeHash)
	if err != nil {
		return err
	}
	files := []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{"client.crt", cred.CertPEM, 0o600},
		{"client.key", cred.KeyPEM, 0o600},
		{"ca.crt", cred.CABundle, 0o644},
	}
	for _, file := range files {
		if err := certauth.WritePrivateFile(dir, file.name, file.data, file.mode); err != nil {
			return fmt.Errorf("write %s: %w", file.name, err)
		}
	}
	_, _ = fmt.Fprintf(out, "issued %s (expires %s)\n", cred.SPIFFEID, cred.ExpiresAt.Format("2006-01-02T15:04:05Z07:00"))
	_, _ = fmt.Fprintf(out, "saved certificate files in %s\n", dir)
	_, _ = fmt.Fprintf(out, "use --tls-client-cert %s --tls-client-key %s --tls-ca-bundle %s\n",
		filepath.Join(dir, "client.crt"), filepath.Join(dir, "client.key"), filepath.Join(dir, "ca.crt"))
	return nil
}

// ensureScopedCertDir creates <configDir>/certs/<hex(scopeHash)> confined to the
// local MCP Runtime config directory. The digest is hex-only and the directory
// is created via os.Root so path components cannot escape configDir. Mode 0700
// is required for owner traversal; private key material is written 0600.
func ensureScopedCertDir(configDir string, scopeHash [sha256.Size]byte) (string, error) {
	if strings.TrimSpace(configDir) == "" {
		return "", fmt.Errorf("config directory is empty")
	}
	absConfig, err := filepath.Abs(filepath.Clean(configDir))
	if err != nil {
		return "", fmt.Errorf("resolve config directory: %w", err)
	}
	digest := hex.EncodeToString(scopeHash[:])
	if len(digest) != sha256.Size*2 || strings.Trim(digest, "0123456789abcdef") != "" {
		return "", fmt.Errorf("invalid certificate scope digest")
	}

	// Create the intentional local config root so OpenRoot can confine the rest.
	// ConfigDir is MCP_RUNTIME_CONFIG_DIR or ~/.mcpruntime — not a remote/untrusted path.
	if err := os.MkdirAll(absConfig, 0o700); err != nil { // #nosec G703 G302 -- local config root; dir needs owner execute
		return "", fmt.Errorf("create config directory: %w", err)
	}
	root, err := os.OpenRoot(absConfig)
	if err != nil {
		return "", fmt.Errorf("open config directory: %w", err)
	}
	defer root.Close()

	rel := filepath.Join("certs", digest)
	// Directory execute bit is required so the owner can open files under certs/.
	if err := root.MkdirAll(rel, 0o700); err != nil { // #nosec G302 -- directory needs owner execute; files use 0600
		return "", fmt.Errorf("create certificate directory: %w", err)
	}
	if err := root.Chmod(rel, 0o700); err != nil { // #nosec G302 -- directory needs owner execute; files use 0600
		return "", fmt.Errorf("secure certificate directory: %w", err)
	}

	dir := filepath.Join(absConfig, rel)
	if relCheck, err := filepath.Rel(absConfig, dir); err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("certificate directory escapes config directory")
	}
	return dir, nil
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
