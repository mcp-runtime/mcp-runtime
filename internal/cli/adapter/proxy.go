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
	var headerFlags headerConfigFlags
	var listenAddr string

	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "Run a local Streamable HTTP MCP proxy that forwards to the runtime",
		Long: `Start a local HTTP listener that accepts Streamable HTTP MCP traffic from an
agent SDK and forwards each request to the configured platform runtime route.
The default certificate mode presents an enrolled session's client certificate.

Use certificate files or provide --server and --agent to enroll a certificate at startup.
When the target server enables OAuth, the local MCP client must send Authorization
or --auth-header must provide it. The gateway then binds the verified certificate
identity to the OAuth subject.

Use --auth-mode header for an upstream server that owns credential authentication.
This mode requires verified HTTPS and does not enroll a certificate or create a
platform session. The client can send application credentials directly, or use
--credential-header-env Header-Name=ENV_NAME (repeatable) or --config to inject
local credential sources. Header values are never configured in server metadata.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolvedFlags, sources, err := resolveHeaderConfig(cmd, flags, headerFlags)
			if err != nil {
				return err
			}
			cfg, err := resolvedFlags.toProxyConfig(listenAddr)
			if err != nil {
				return err
			}
			cfg.CredentialHeaders = sources
			if cfg.AuthMode != agentadapter.AuthModeHeader && cfg.AuthMode != "" && cfg.AuthMode != agentadapter.AuthModeCertificate {
				return fmt.Errorf("--auth-mode must be certificate or header")
			}
			if cfg.RuntimeURL == nil || cfg.RuntimeURL.Scheme != "https" {
				if cfg.AuthMode == agentadapter.AuthModeHeader {
					return fmt.Errorf("header mode requires an https --runtime-url (or $%s)", agentadapter.EnvRuntimeURL)
				}
				return fmt.Errorf("certificate identity requires an https --runtime-url (or $%s)", agentadapter.EnvRuntimeURL)
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			if cfg.AuthMode == agentadapter.AuthModeHeader {
				if resolvedFlags.tlsClientCert != "" || resolvedFlags.tlsClientKey != "" || sessionFlags.enabled() || sessionFlags.agent != "" || sessionFlags.autoRefresh {
					return fmt.Errorf("header mode cannot use certificate enrollment, client certificates, --agent, or --auto-refresh")
				}
			} else {
				transport, stopAuth, err := resolveAuth(ctx, resolvedFlags, &sessionFlags, cfg.Transport, cmd.ErrOrStderr())
				if err != nil {
					return err
				}
				defer stopAuth()
				cfg.Transport = transport
				cfg.CertificateIdentity = true
			}
			if err := cfg.Validate(); err != nil {
				return err
			}

			fmt.Fprintf(cmd.ErrOrStderr(), "mcp-runtime adapter proxy listening on %s -> %s\n",
				cfg.ListenAddr, cfg.RuntimeURL.String())
			return agentadapter.RunHTTPProxy(ctx, cfg)
		},
	}

	bindIdentityFlags(cmd, &flags)
	bindHeaderConfigFlags(cmd, &flags, &headerFlags)
	bindProxyFlags(cmd, &flags)
	bindPlatformSessionFlags(cmd, &sessionFlags)
	cmd.Flags().StringVar(&listenAddr, "listen", os.Getenv(agentadapter.EnvListenAddr),
		"Local listen address (default: $"+agentadapter.EnvListenAddr+" or "+agentadapter.DefaultListenAddr+")")
	return cmd
}
