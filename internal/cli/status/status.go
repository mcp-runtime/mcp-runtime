// Package status owns the quick platform API readiness check.
package status

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/platformapi"
	"mcp-runtime/pkg/authfile"
)

// New returns the status command.
func New(_ *core.Runtime) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show platform status",
		Long:  "Check whether the platform API is reachable and accepts your saved login, with a five-second timeout. Use server list, cluster status, or sentinel status for details.",
		Args:  cobra.NoArgs,
		RunE:  showPlatformStatus,
	}
}

func showPlatformStatus(cmd *cobra.Command, _ []string) error {
	_, base, _, err := authfile.ResolveToken()
	if err != nil {
		fmt.Fprintln(cmd.OutOrStdout(), "Status: LOGIN REQUIRED")
		return fmt.Errorf("load platform credentials: %w; run mcp-runtime auth login --api-url <platform-url>", err)
	}
	client, err := platformapi.NewPlatformClient()
	if err != nil {
		fmt.Fprintln(cmd.OutOrStdout(), "Status: NOT CONFIGURED")
		return err
	}
	base = platformapi.NormalizeBaseURL(base)
	fmt.Fprintln(cmd.OutOrStdout(), "Platform:", base)
	ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
	defer cancel()
	if err := client.ValidateCredentials(ctx); err != nil {
		if strings.Contains(err.Error(), "API 401:") || strings.Contains(err.Error(), "HTTP 401") {
			fmt.Fprintln(cmd.OutOrStdout(), "Status: LOGIN REQUIRED")
			return fmt.Errorf("saved platform credentials were rejected; run mcp-runtime auth login --api-url %s: %w", base, err)
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Status: NOT READY")
		return fmt.Errorf("platform API check failed: %w", err)
	}
	fmt.Fprintln(cmd.OutOrStdout(), "Status: READY")
	return nil
}
