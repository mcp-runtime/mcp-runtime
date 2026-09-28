package adapter

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"mcp-runtime/internal/agentadapter"
	"mcp-runtime/internal/cli/core"
)

func newStdioCmd(_ *core.Runtime) *cobra.Command {
	var flags identityFlags
	var sessionFlags platformSessionFlags

	cmd := &cobra.Command{
		Use:   "stdio",
		Short: "Bridge stdio MCP traffic to the configured runtime route",
		Long: `Read newline-delimited MCP JSON-RPC messages from stdin, forward each to the
configured platform runtime route over Streamable HTTP, and write the JSON-RPC
responses back to stdout. Designed for IDE-style MCP clients (e.g. Cursor,
Claude Desktop) that launch an MCP server as a subprocess.

Use certificate files or provide --server and --agent to enroll a certificate at startup.
When the target server enables OAuth, the local MCP client must send Authorization
or --auth-header must provide it. The gateway then binds the verified certificate
identity to the OAuth subject. Anonymous mode skips the client certificate for
public/read-only discovery routes.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := flags.toShimConfig()
			if err != nil {
				return err
			}
			if !cfg.Anonymous && (cfg.RuntimeURL == nil || cfg.RuntimeURL.Scheme != "https") {
				return fmt.Errorf("certificate identity requires an https --runtime-url (or $%s)", agentadapter.EnvRuntimeURL)
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			if cfg.Anonymous {
				if err := cfg.Validate(); err != nil {
					return err
				}
				return agentadapter.RunStdioShim(ctx, cfg, agentadapter.StdioOptions{
					Stdin:  os.Stdin,
					Stdout: os.Stdout,
				})
			}

			transport, stopAuth, err := resolveAuth(ctx, flags, &sessionFlags, cfg.Transport, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			defer stopAuth()
			cfg.Transport = transport
			cfg.CertificateIdentity = true
			if err := cfg.Validate(); err != nil {
				return err
			}

			return agentadapter.RunStdioShim(ctx, cfg, agentadapter.StdioOptions{
				Stdin:  os.Stdin,
				Stdout: os.Stdout,
			})
		},
	}

	bindIdentityFlags(cmd, &flags)
	bindStdioFlags(cmd, &flags)
	bindPlatformSessionFlags(cmd, &sessionFlags)
	return cmd
}
