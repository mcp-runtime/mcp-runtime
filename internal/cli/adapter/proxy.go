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

func newProxyCmd(_ *core.Runtime) *cobra.Command {
	var flags identityFlags
	var sessionFlags platformSessionFlags
	var listenAddr string

	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "Run a local Streamable HTTP MCP proxy that forwards to the runtime",
		Long: `Start a local HTTP listener that accepts Streamable HTTP MCP traffic from an
agent SDK and forwards each request to the configured platform runtime route
with a platform-issued client certificate for the enrolled agent session.

Use certificate files or provide --server and --agent to enroll a certificate at startup.
When the target server enables OAuth, the local MCP client must send Authorization
or --auth-header must provide it. The gateway then binds the verified certificate
identity to the OAuth subject.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := flags.toProxyConfig(listenAddr)
			if err != nil {
				return err
			}
			if cfg.RuntimeURL == nil || cfg.RuntimeURL.Scheme != "https" {
				return fmt.Errorf("certificate identity requires an https --runtime-url (or $%s)", agentadapter.EnvRuntimeURL)
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

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

			fmt.Fprintf(cmd.ErrOrStderr(), "mcp-runtime adapter proxy listening on %s -> %s\n",
				cfg.ListenAddr, cfg.RuntimeURL.String())
			return agentadapter.RunHTTPProxy(ctx, cfg)
		},
	}

	bindIdentityFlags(cmd, &flags)
	bindProxyFlags(cmd, &flags)
	bindPlatformSessionFlags(cmd, &sessionFlags)
	cmd.Flags().StringVar(&listenAddr, "listen", os.Getenv(agentadapter.EnvListenAddr),
		"Local listen address (default: $"+agentadapter.EnvListenAddr+" or "+agentadapter.DefaultListenAddr+")")
	return cmd
}
