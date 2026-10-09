// Package adapter routes the adapter top-level command for the certificate-
// authenticated HTTP proxy.
package adapter

import (
	"github.com/spf13/cobra"

	"mcp-runtime/internal/cli/core"
)

// New returns the adapter command.
func New(runtime *core.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "adapter",
		Short: "Run the optional MCP HTTP adapter or enroll a client certificate",
		Long: `Adapter commands forward agent MCP traffic to a configured platform-issued
runtime route. By default the adapter presents a session-bound client certificate.
When the target server enables OAuth, it also forwards the bearer token and the
gateway binds the certificate identity to the OAuth subject.

Available commands:

  mcp-runtime adapter proxy   Local Streamable HTTP listener (for SDKs that
                              speak MCP over HTTP).
  mcp-runtime adapter enroll  Enroll and save a client certificate.

The adapter enrolls a session and certificate through the platform API when
` + "`--server`" + ` and ` + "`--agent`" + ` are supplied. An existing certificate can be
provided with the TLS client flags.

An explicitly selected header mode forwards application credentials for the
MCP server to authenticate, without certificate enrollment or a platform login.
Direct HTTP clients can send those credentials without an adapter.`,
	}
	cmd.AddCommand(newProxyCmd(runtime))
	cmd.AddCommand(newEnrollCmd(runtime))
	return cmd
}
