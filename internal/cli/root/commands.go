package root

import (
	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"mcp-runtime/internal/cli/access"
	"mcp-runtime/internal/cli/adapter"
	"mcp-runtime/internal/cli/admin"
	"mcp-runtime/internal/cli/agent"
	"mcp-runtime/internal/cli/auth"
	"mcp-runtime/internal/cli/bootstrap"
	"mcp-runtime/internal/cli/catalog"
	"mcp-runtime/internal/cli/cluster"
	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/registry"
	"mcp-runtime/internal/cli/sentinel"
	"mcp-runtime/internal/cli/server"
	"mcp-runtime/internal/cli/setup"
	"mcp-runtime/internal/cli/status"
	"mcp-runtime/internal/cli/team"
	"mcp-runtime/internal/cli/update"
)

// AddCommands registers every top-level mcp-runtime command on root.
func AddCommands(root *cobra.Command, logger *zap.Logger) {
	runtime := core.NewRuntime(logger)
	clusterMgr := cluster.DefaultClusterManager(logger)

	root.AddCommand(cluster.NewWithManager(clusterMgr))
	root.AddCommand(catalog.New(runtime))
	root.AddCommand(registry.New(runtime))
	root.AddCommand(server.New(runtime))
	root.AddCommand(access.New(runtime))
	root.AddCommand(agent.New(runtime))
	root.AddCommand(adapter.New(runtime))
	root.AddCommand(admin.New(runtime))
	root.AddCommand(auth.New(runtime))
	root.AddCommand(bootstrap.New(runtime))
	root.AddCommand(setup.New(runtime, clusterMgr))
	root.AddCommand(update.New(runtime))
	root.AddCommand(status.New(runtime))
	root.AddCommand(sentinel.New(runtime))
	root.AddCommand(team.New(runtime))
}
