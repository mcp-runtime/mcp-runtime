// Package adapter routes the agent-side HTTP adapter commands.
package adapter

import (
	"github.com/spf13/cobra"

	"mcp-runtime/internal/cli/core"
)

// New returns the adapter command.
func New(runtime *core.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "adapter",
		Short: "Run the certificate authenticated HTTP adapter",
		Long: `Adapter commands forward Streamable HTTP MCP traffic to a configured platform-issued
runtime route. The adapter always presents a session-bound client certificate.
When the target server enables OAuth, it also forwards the bearer token and the
gateway binds the certificate identity to the OAuth subject.

  mcp-runtime adapter proxy   Local Streamable HTTP listener.
  mcp-runtime adapter enroll  Enroll and save a client certificate.

The adapter enrolls a session and certificate through the platform API when
` + "`--server`" + ` and ` + "`--agent`" + ` are supplied. An existing certificate can be
provided with the TLS client flags.`,
	}
	cmd.AddCommand(newProxyCmd(runtime))
	cmd.AddCommand(newEnrollCmd(runtime))
	return cmd
}
