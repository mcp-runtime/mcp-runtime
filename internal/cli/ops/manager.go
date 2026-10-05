package ops

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"go.uber.org/zap"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/kubeerr"
	"mcp-runtime/internal/cli/platformstatus"
	"mcp-runtime/pkg/platforminventory"
)

// Manager operates the bundled platform stack via kubectl.
type Manager struct {
	kubectl *core.KubectlClient
	logger  *zap.Logger
}

type platformComponent = platforminventory.Component

var platformComponents = platforminventory.SentinelComponents(false)

// NewManager creates a Manager with explicit dependencies.
func NewManager(kubectl *core.KubectlClient, logger *zap.Logger) *Manager {
	return &Manager{kubectl: kubectl, logger: logger}
}

// DefaultManager returns a Manager using the shared runtime clients.
func DefaultManager(runtime *core.Runtime) *Manager {
	return NewManager(runtime.KubectlClient(), runtime.Logger())
}

// ComponentKeys returns sorted valid component names for cobra completion.
func ComponentKeys() []string {
	keys := make([]string, 0, len(platformComponents))
	for _, component := range platformComponents {
		keys = append(keys, component.Key)
	}
	sort.Strings(keys)
	return keys
}

func findPlatformComponent(name string) (*platformComponent, error) {
	candidate := strings.ToLower(strings.TrimSpace(name))
	for i := range platformComponents {
		component := &platformComponents[i]
		if component.Key == candidate {
			return component, nil
		}
		for _, alias := range component.Aliases {
			if alias == candidate {
				return component, nil
			}
		}
	}

	return nil, core.NewWithSentinel(nil, fmt.Sprintf("unknown platform component %q (use one of: %s)", name, strings.Join(ComponentKeys(), ", ")))
}

// ShowStatus prints a status table for platform workloads.
func (m *Manager) ShowStatus() error {
	if err := m.requireAdminClusterAccess(); err != nil {
		return err
	}
	core.Header("MCP Platform Stack Status")
	core.DefaultPrinter.Println()

	tableData := [][]string{{"Component", "Namespace", "Resource", "Status", "Details"}}

	clusterReachable := true
	if err := platformstatus.CheckClusterStatusQuiet(m.kubectl); err != nil {
		clusterReachable = false
		tableData = append(tableData, platformstatus.AnalyticsStackRow(core.Red("ERROR"), err.Error()))
		core.TableBoxed(tableData)
		return nil
	}

	installed, err := platformstatus.AnalyticsNamespaceInstalled(m.kubectl, clusterReachable)
	switch {
	case err != nil:
		tableData = append(tableData, platformstatus.AnalyticsStackRow(core.Red("ERROR"), err.Error()))
	case !installed:
		tableData = append(tableData, platformstatus.AnalyticsStackRow(core.Yellow("SKIPPED"), "Namespace not found"))
	default:
		for _, workload := range platformstatus.DefaultPlatformStatusWorkloads {
			tableData = append(tableData, platformstatus.WorkloadStatusRow(m.kubectl, workload, true))
		}
	}

	core.TableBoxed(tableData)
	return nil
}

// ViewLogs streams logs for a platform component.
func (m *Manager) ViewLogs(component string, follow, previous bool, tail int, since string) error {
	if err := m.requireAdminClusterAccess(); err != nil {
		return err
	}
	target, err := findPlatformComponent(component)
	if err != nil {
		return err
	}

	args := []string{
		"logs",
		"-n", target.Namespace,
		"-l", "app=" + target.Label,
		"--all-containers=true",
		"--prefix=true",
		"--tail", strconv.Itoa(tail),
	}
	if follow {
		args = append(args, "-f")
	}
	if previous {
		args = append(args, "--previous")
	}
	if strings.TrimSpace(since) != "" {
		args = append(args, "--since", strings.TrimSpace(since))
	}

	if err := m.kubectl.RunWithOutput(args, os.Stdout, os.Stderr); err != nil {
		return core.WrapWithSentinelAndContext(nil, err, fmt.Sprintf("failed to stream logs for sentinel component %q: %v", component, err), map[string]any{
			"component": component,
			"namespace": target.Namespace,
		})
	}
	return nil
}

// ShowEvents lists events from each platform-owned namespace.
func (m *Manager) ShowEvents() error {
	if err := m.requireAdminClusterAccess(); err != nil {
		return err
	}
	for _, namespace := range []string{
		core.NamespaceMCPRuntime,
		core.ComponentNamespace("platform-api"),
		core.ComponentNamespace("analytics-api"),
		core.ComponentNamespace("promtail"),
	} {
		fmt.Fprintf(os.Stdout, "\n%s events:\n", namespace)
		args := []string{"get", "events", "-n", namespace, "--sort-by=.lastTimestamp"}
		if err := m.kubectl.RunWithOutput(args, os.Stdout, os.Stderr); err != nil {
			return core.WrapWithSentinelAndContext(nil, err, fmt.Sprintf("failed to list platform events in %s: %v", namespace, err), map[string]any{
				"namespace": namespace,
				"component": "ops",
			})
		}
	}
	return nil
}

// PortForwardTarget runs kubectl port-forward for a known service target.
func (m *Manager) PortForwardTarget(target string, localPort int, address string) error {
	if err := m.requireAdminClusterAccess(); err != nil {
		return err
	}
	component, err := findPlatformComponent(target)
	if err != nil {
		return err
	}
	if component.PortTarget == nil {
		return core.NewWithSentinel(nil, fmt.Sprintf("component %q does not expose a predefined port-forward target", target))
	}
	portTarget := component.PortTarget
	if localPort <= 0 {
		localPort = portTarget.LocalPort
	}

	args := []string{
		"port-forward",
		"-n", component.Namespace,
		fmt.Sprintf("%s/%s", portTarget.ResourceKind, portTarget.ResourceName),
		fmt.Sprintf("%d:%d", localPort, portTarget.RemotePort),
		"--address", address,
	}

	if err := m.kubectl.RunWithOutput(args, os.Stdout, os.Stderr); err != nil {
		return core.WrapWithSentinelAndContext(nil, err, fmt.Sprintf("failed to port-forward sentinel target %q: %v", target, err), map[string]any{
			"target":    target,
			"namespace": component.Namespace,
			"component": "ops",
		})
	}
	return nil
}

// Restart restarts one component or all platform workloads.
func (m *Manager) Restart(component string, restartAll bool) error {
	if err := m.requireAdminClusterAccess(); err != nil {
		return err
	}
	if restartAll {
		for _, target := range platformComponents {
			args := []string{"rollout", "restart", fmt.Sprintf("%s/%s", target.Kind, target.Resource), "-n", target.Namespace}
			if err := m.kubectl.RunWithOutput(args, os.Stdout, os.Stderr); err != nil {
				return core.WrapWithSentinelAndContext(nil, err, fmt.Sprintf("failed to restart sentinel component %q: %v", target.Key, err), map[string]any{
					"component": target.Key,
					"namespace": target.Namespace,
				})
			}
		}
		return nil
	}

	target, err := findPlatformComponent(component)
	if err != nil {
		return err
	}
	args := []string{"rollout", "restart", fmt.Sprintf("%s/%s", target.Kind, target.Resource), "-n", target.Namespace}
	if err := m.kubectl.RunWithOutput(args, os.Stdout, os.Stderr); err != nil {
		return core.WrapWithSentinelAndContext(nil, err, fmt.Sprintf("failed to restart sentinel component %q: %v", component, err), map[string]any{
			"component": component,
			"namespace": target.Namespace,
		})
	}
	return nil
}

func (m *Manager) requireAdminClusterAccess() error {
	if m.kubectl == nil {
		return core.NewWithSentinel(nil, kubeerr.DirectModeFailureMessage("ops commands require admin cluster access", "kubectl client is unavailable"))
	}
	cmd, err := m.kubectl.CommandArgs([]string{"cluster-info"})
	if err != nil {
		return core.NewWithSentinel(nil, kubeerr.DirectModeFailureMessage("ops commands require admin cluster access", err.Error()))
	}
	output, execErr := cmd.CombinedOutput()
	if execErr != nil {
		detail := kubeerr.CommandDetail(string(output), execErr)
		return core.NewWithSentinel(nil, kubeerr.DirectModeFailureMessage("ops commands require admin cluster access", detail))
	}
	return nil
}
