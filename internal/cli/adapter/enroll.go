package adapter

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/platformapi"
	"mcp-runtime/pkg/certauth"
)

func newEnrollCmd(_ *core.Runtime) *cobra.Command {
	var flags platformSessionFlags
	var outputDir string
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
			return enrollAdapterCertificate(cmd.Context(), client, flags, outputDir, trustDomain, cmd.OutOrStdout())
		},
	}
	bindPlatformSessionFlags(cmd, &flags)
	_ = cmd.Flags().MarkHidden("auto-refresh")
	cmd.Flags().StringVar(&outputDir, "output-dir", ".", "Directory for client.crt, client.key, and ca.crt")
	cmd.Flags().StringVar(&trustDomain, "trust-domain", os.Getenv(EnvMTLSTrustDomain), "Optional platform SPIFFE trust domain override; defaults to the domain returned by the platform")
	return cmd
}

func enrollAdapterCertificate(ctx context.Context, client *platformapi.PlatformClient, flags platformSessionFlags, outputDir, trustDomain string, out interface{ Write([]byte) (int, error) }) error {
	if strings.TrimSpace(trustDomain) == "" {
		return fmt.Errorf("--trust-domain must not be empty")
	}
	cred, err := issueAdapterCredential(ctx, client, flags, trustDomain)
	if err != nil {
		return err
	}

	dir := filepath.Clean(outputDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create output directory: %w", err)
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
	_, _ = fmt.Fprintf(out, "use --tls-client-cert %s --tls-client-key %s --tls-ca-bundle %s\n",
		filepath.Join(dir, "client.crt"), filepath.Join(dir, "client.key"), filepath.Join(dir, "ca.crt"))
	return nil
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envTruthy(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
