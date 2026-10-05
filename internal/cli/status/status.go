// Package status owns the status top-level command and platform status output.
package status

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/platformapi"
	"mcp-runtime/internal/cli/platformstatus"
	"mcp-runtime/internal/cli/registry/config"
	"mcp-runtime/pkg/authfile"
)

type manager struct {
	logger  *zap.Logger
	kubectl core.KubectlRunner
}

func newManager(runtime *core.Runtime) *manager {
	return &manager{
		logger:  runtime.Logger(),
		kubectl: runtime.KubectlRunner(),
	}
}

// New returns the status command.
func New(runtime *core.Runtime) *cobra.Command {
	mgr := newManager(runtime)
	return &cobra.Command{
		Use:   "status",
		Short: "Show platform status",
		Long:  "Show MCP Runtime platform status using the platform API when logged in, with optional cluster diagnostics when kubectl is available.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return ShowPlatformStatus(mgr.logger, mgr.kubectl)
		},
	}
}

// ShowPlatformStatus prints the MCP platform status table and MCP server list.
func ShowPlatformStatus(logger *zap.Logger, kubectl core.KubectlRunner) error {
	core.Header("MCP Platform Status")
	core.DefaultPrinter.Println()

	if plat, err := platformapi.NewPlatformClient(); err == nil {
		return showPlatformStatusFromAPI(logger, plat, kubectl)
	}
	return showClusterPlatformStatus(kubectl)
}

func showPlatformStatusFromAPI(logger *zap.Logger, plat *platformapi.PlatformClient, kubectl core.KubectlRunner) error {
	tableData := [][]string{
		{"Component", "Namespace", "Resource", "Status", "Details"},
	}

	platformStatus := core.Green("OK")
	platformDetails := "Authenticated"
	var platformErr error
	validationCtx, cancelValidation := context.WithTimeout(context.Background(), 10*time.Second)
	if err := plat.ValidateCredentials(validationCtx); err != nil {
		platformErr = err
		platformStatus = core.Red("ERROR")
		platformDetails = err.Error()
	}
	cancelValidation()
	tableData = append(tableData, []string{"Platform API", "-", "auth", platformStatus, platformDetails})

	if host := strings.TrimSpace(resolveStatusRegistryHost()); host != "" {
		tableData = append(tableData, []string{"Registry", "-", "host", core.Cyan("CONFIGURED"), host})
	}

	clusterErr := platformstatus.CheckClusterStatusQuiet(kubectl)
	clusterReachable := clusterErr == nil
	if clusterReachable {
		tableData = append(tableData, platformstatus.WorkloadStatusRow(
			kubectl,
			platformstatus.PlatformWorkload{Component: "Registry", Namespace: core.NamespaceRegistry, Kind: "deployment", Name: core.RegistryDeploymentName},
			true,
		))
		tableData = append(tableData, platformstatus.WorkloadStatusRow(
			kubectl,
			platformstatus.PlatformWorkload{Component: "Operator", Namespace: core.NamespaceMCPRuntime, Kind: "deployment", Name: core.OperatorDeploymentName},
			true,
		))
	} else {
		clusterDetails := "Kubernetes API unavailable"
		if clusterErr != nil {
			clusterDetails = clusterErr.Error()
		}
		tableData = append(tableData, []string{"Cluster", "-", "kube-api", core.Yellow("SKIPPED"), clusterDetails})
	}

	core.TableBoxed(tableData)
	core.DefaultPrinter.Println()
	core.Section("MCP Servers")
	if platformErr != nil {
		core.Warn("Skipping MCP server list because the platform API request failed")
		if strings.Contains(platformErr.Error(), "API 401") {
			core.Info("Remedy: saved platform credentials were rejected; run `mcp-runtime auth login` to sign in again")
		} else if strings.Contains(strings.ToLower(platformErr.Error()), "no such host") {
			core.Info("Remedy: the platform hostname did not resolve; check DNS/network access and retry")
		} else {
			core.Info("Remedy: check the platform API URL and network access, then retry `mcp-runtime status`")
		}
		if clusterErr != nil {
			core.Info("Cluster remedy: check `kubectl config current-context`, kubeconfig API server address, network access, and node-list permissions")
		}
		core.DefaultPrinter.Println()
		return nil
	}

	listCtx, cancelList := context.WithTimeout(context.Background(), 10*time.Second)
	servers, err := plat.ListRuntimeServers(listCtx, "")
	cancelList()
	if err != nil {
		core.Warn("Failed to list MCP servers from platform API: " + err.Error())
	} else if len(servers) == 0 {
		core.Warn("No MCP servers deployed")
	} else {
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "NAMESPACE\tNAME\tIMAGE\tSTATUS\tREADY")
		for _, server := range servers {
			image := strings.TrimSpace(server.Image)
			if tag := strings.TrimSpace(server.ImageTag); tag != "" && !strings.Contains(image, ":") {
				image = image + ":" + tag
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", server.Namespace, server.Name, image, server.Status, server.Ready)
		}
		_ = tw.Flush()
	}

	core.DefaultPrinter.Println()
	core.Info("Use 'mcp-runtime server list' for detailed server info")
	return nil
}

func showClusterPlatformStatus(kubectl core.KubectlRunner) error {
	tableData := [][]string{
		{"Component", "Namespace", "Resource", "Status", "Details"},
	}

	clusterReachable := true
	clusterStatus := core.Green("OK")
	clusterDetails := "Connected"
	if err := platformstatus.CheckClusterStatusQuiet(kubectl); err != nil {
		clusterReachable = false
		clusterStatus = core.Red("ERROR")
		clusterDetails = err.Error()
	}
	tableData = append(tableData, []string{"Cluster", "-", "kube-api", clusterStatus, clusterDetails})
	tableData = append(tableData, []string{"Platform API", "-", "auth", core.Yellow("SKIPPED"), "Not logged in; run mcp-runtime auth login"})

	extRegistry, err := config.Resolve(nil, config.Env{
		URL:      core.DefaultCLIConfig.ProvisionedRegistryURL,
		Username: core.DefaultCLIConfig.ProvisionedRegistryUsername,
		Password: core.DefaultCLIConfig.ProvisionedRegistryPassword,
	})
	switch {
	case err != nil:
		core.Warn("Failed to load external registry config: " + err.Error())
		tableData = append(tableData, []string{"Registry", "-", "config", core.Red("ERROR"), err.Error()})
	case extRegistry != nil && extRegistry.URL != "":
		tableData = append(tableData, []string{"Registry", "-", "external", core.Cyan("EXTERNAL"), "Configured: " + extRegistry.URL})
	default:
		tableData = append(tableData, platformstatus.WorkloadStatusRow(
			kubectl,
			platformstatus.PlatformWorkload{Component: "Registry", Namespace: core.NamespaceRegistry, Kind: "deployment", Name: core.RegistryDeploymentName},
			clusterReachable,
		))
	}

	tableData = append(tableData, platformstatus.WorkloadStatusRow(
		kubectl,
		platformstatus.PlatformWorkload{Component: "Operator", Namespace: core.NamespaceMCPRuntime, Kind: "deployment", Name: core.OperatorDeploymentName},
		clusterReachable,
	))

	switch installed, analyticsErr := platformstatus.AnalyticsNamespaceInstalled(kubectl, clusterReachable); {
	case !clusterReachable:
		tableData = append(tableData, platformstatus.AnalyticsStackRow(core.Red("ERROR"), "Cluster unavailable"))
	case analyticsErr != nil:
		tableData = append(tableData, platformstatus.AnalyticsStackRow(core.Red("ERROR"), analyticsErr.Error()))
	case !installed:
		tableData = append(tableData, platformstatus.AnalyticsStackRow(core.Yellow("SKIPPED"), "Namespace not found"))
	default:
		for _, workload := range platformstatus.DefaultPlatformStatusWorkloads {
			tableData = append(tableData, platformstatus.WorkloadStatusRow(kubectl, workload, true))
		}
	}

	core.TableBoxed(tableData)

	core.DefaultPrinter.Println()
	core.Section("MCP Servers")

	if !clusterReachable {
		core.Warn("Skipping MCP server status: cluster unavailable and platform API credentials are not configured")
		core.DefaultPrinter.Println()
		core.Info("Use 'mcp-runtime auth login' or 'mcp-runtime server list' for platform-backed status")
		return nil
	}

	cmd, err := kubectl.CommandArgs([]string{"get", "mcpserver", "--all-namespaces", "-o", "custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,IMAGE:.spec.image,REPLICAS:.spec.replicas,PATH:.spec.ingressPath"})
	if err != nil {
		core.Warn("Failed to list MCP servers: " + err.Error())
	} else {
		output, execErr := cmd.CombinedOutput()
		if execErr != nil {
			errDetails := strings.TrimSpace(string(output))
			if errDetails == "" {
				errDetails = execErr.Error()
			}
			core.Warn("Failed to list MCP servers: " + errDetails)
		} else if len(strings.TrimSpace(string(output))) == 0 {
			core.Warn("No MCP servers deployed")
		} else {
			lines := strings.Split(strings.TrimSpace(string(output)), "\n")
			if len(lines) <= 1 {
				core.Warn("No MCP servers deployed")
			} else {
				serverData := [][]string{}
				for _, line := range lines {
					fields := strings.Fields(line)
					serverData = append(serverData, fields)
				}
				core.Table(serverData)
			}
		}
	}

	core.DefaultPrinter.Println()
	core.Info("Use 'mcp-runtime server list' for detailed server info")

	return nil
}

func resolveStatusRegistryHost() string {
	if host := strings.TrimSpace(authfile.CurrentRegistryHost()); host != "" {
		return strings.TrimSuffix(host, "/")
	}
	if ext, err := config.Resolve(nil, config.Env{
		URL:      core.DefaultCLIConfig.ProvisionedRegistryURL,
		Username: core.DefaultCLIConfig.ProvisionedRegistryUsername,
		Password: core.DefaultCLIConfig.ProvisionedRegistryPassword,
	}); err == nil && ext != nil && strings.TrimSpace(ext.URL) != "" {
		return strings.TrimSuffix(strings.TrimSpace(ext.URL), "/")
	}
	return ""
}
