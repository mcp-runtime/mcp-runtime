// Package user owns administrator platform-account lifecycle commands.
package user

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/platformapi"
	"mcp-runtime/pkg/platform"
)

func New(_ *core.Runtime) *cobra.Command {
	return newCommand(func(ctx context.Context, id string) (platform.UserDeactivationResult, error) {
		client, err := platformapi.NewPlatformClient()
		if err != nil {
			return platform.UserDeactivationResult{}, err
		}
		return client.DeactivateUser(ctx, id)
	})
}

func newCommand(deactivate func(context.Context, string) (platform.UserDeactivationResult, error)) *cobra.Command {
	cmd := &cobra.Command{Use: "user", Short: "Manage platform accounts as an administrator"}
	var yes, jsonOutput bool
	action := &cobra.Command{Use: "deactivate [user-id]", Short: "Permanently deactivate an account and revoke its access", Long: "Permanently disable a platform account, remove its team memberships, and revoke its API keys, registry credentials, refresh tokens and MCP sessions. Audit history and the identity tombstone remain. Transfer sole team ownership first. This differs from team user remove, which removes only one membership.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		id, err := uuid.Parse(strings.TrimSpace(args[0]))
		if err != nil {
			return fmt.Errorf("valid user ID required")
		}
		if !yes {
			return fmt.Errorf("account deactivation permanently revokes access; pass --yes to confirm")
		}
		out, err := deactivate(cmd.Context(), id.String())
		if err != nil {
			return err
		}
		if jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Account %s: %s (already deactivated: %t)\nRevoked API keys: %d; registry credentials: %d; refresh tokens: %d; MCP sessions: %d; removed memberships: %d\n", out.UserID, out.Status, out.AlreadyDeactivated, out.RevokedAPIKeys, out.RevokedRegistryCredentials, out.RevokedRefreshTokens, out.RevokedSessions, out.RemovedMemberships)
		return err
	}}
	action.Flags().BoolVar(&yes, "yes", false, "Confirm permanent account deactivation")
	action.Flags().BoolVar(&jsonOutput, "json", false, "Print the non-secret cleanup result as JSON")
	cmd.AddCommand(action)
	return cmd
}
