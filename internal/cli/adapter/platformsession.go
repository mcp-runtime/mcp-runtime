package adapter

import (
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const (
	// EnvPlatformURL overrides the platform API base URL used for certificate enrollment.
	EnvPlatformURL = "MCP_PLATFORM_API_URL"
	// EnvAdapterServer / Namespace / Agent identify the enrollment target.
	EnvAdapterServer      = "MCP_RUNTIME_ADAPTER_SERVER"
	EnvAdapterNamespace   = "MCP_RUNTIME_ADAPTER_NAMESPACE"
	EnvAdapterAgent       = "MCP_RUNTIME_ADAPTER_AGENT"
	EnvAdapterAutoRefresh = "MCP_RUNTIME_ADAPTER_AUTO_REFRESH"
	// EnvMTLSTrustDomain optionally overrides the platform-wide SPIFFE trust domain.
	EnvMTLSTrustDomain = "MCP_TRUST_DOMAIN"
	// adapterRefreshLead is how far in advance of expiry the refresher fires.
	// Keep this above the platform refresh buffer so certificate renewal starts early.
	adapterRefreshLead = 5 * time.Minute
	// adapterRefreshFloor caps retry frequency for short-lived certificates.
	adapterRefreshFloor = 30 * time.Second
)

// platformSessionFlags carries the platform certificate enrollment settings.
type platformSessionFlags struct {
	server      string
	namespace   string
	agent       string
	platformURL string
	autoRefresh bool
}

func (f *platformSessionFlags) enabled() bool {
	return f != nil && strings.TrimSpace(f.server) != ""
}

func bindPlatformSessionFlags(cmd *cobra.Command, f *platformSessionFlags) {
	cmd.Flags().StringVar(&f.server, "server", os.Getenv(EnvAdapterServer),
		"MCPServer name to enroll a session-bound adapter certificate for (default: $"+EnvAdapterServer+")")
	cmd.Flags().StringVar(&f.namespace, "namespace", os.Getenv(EnvAdapterNamespace),
		"Namespace of the target MCPServer; defaults to the principal's primary namespace (default: $"+EnvAdapterNamespace+")")
	cmd.Flags().StringVar(&f.agent, "agent", os.Getenv(EnvAdapterAgent),
		"Agent identifier to associate with the certificate identity (default: $"+EnvAdapterAgent+")")
	cmd.Flags().StringVar(&f.platformURL, "platform-url", os.Getenv(EnvPlatformURL),
		"Platform API base URL; overrides the URL stored by mcp-runtime auth login (default: $"+EnvPlatformURL+")")
	cmd.Flags().BoolVar(&f.autoRefresh, "auto-refresh", parseEnvBoolSimple(EnvAdapterAutoRefresh),
		"Renew the in-memory adapter certificate a few minutes before expiry (default: $"+EnvAdapterAutoRefresh+")")
}

func parseEnvBoolSimple(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "t", "true", "y", "yes", "on":
		return true
	default:
		return false
	}
}
