package sentinel

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
)

// SentinelManager operates the bundled mcp-sentinel stack via kubectl.
type SentinelManager struct {
	kubectl *core.KubectlClient
	logger  *zap.Logger
}

type sentinelComponent struct {
	Key        string
	Display    string
	Namespace  string
	Kind       string
	Resource   string
	Label      string
	Aliases    []string
	PortTarget *sentinelPortTarget
}

type sentinelPortTarget struct {
	ResourceKind string
	ResourceName string
	LocalPort    int
	RemotePort   int
}

var sentinelComponents = []sentinelComponent{
	{Key: "clickhouse", Display: "ClickHouse", Namespace: core.DefaultAnalyticsNamespace, Kind: "statefulset", Resource: "clickhouse", Label: "clickhouse"},
	{Key: "kafka", Display: "Kafka", Namespace: core.DefaultAnalyticsNamespace, Kind: "statefulset", Resource: "kafka", Label: "kafka"},
	{Key: "ingest", Display: "Ingest", Namespace: core.DefaultAnalyticsNamespace, Kind: "deployment", Resource: "mcp-sentinel-ingest", Label: "mcp-sentinel-ingest"},
	{
		Key:       "platform-api",
		Display:   "Platform API",
		Namespace: core.DefaultAnalyticsNamespace,
		Kind:      "deployment",
		Resource:  "mcp-platform-api",
		Label:     "mcp-platform-api",
		Aliases:   []string{"api", "platform"},
		PortTarget: &sentinelPortTarget{
			ResourceKind: "service",
			ResourceName: "mcp-platform-api",
			LocalPort:    8080,
			RemotePort:   8080,
		},
	},
	{
		Key:       "runtime-api",
		Display:   "Runtime Control",
		Namespace: core.DefaultAnalyticsNamespace,
		Kind:      "deployment",
		Resource:  "mcp-runtime-api",
		Label:     "mcp-runtime-api",
		Aliases:   []string{"runtime"},
		PortTarget: &sentinelPortTarget{
			ResourceKind: "service",
			ResourceName: "mcp-runtime-api",
			LocalPort:    8084,
			RemotePort:   8084,
		},
	},
	{
		Key:       "analytics-api",
		Display:   "Analytics API",
		Namespace: core.DefaultAnalyticsNamespace,
		Kind:      "deployment",
		Resource:  "mcp-analytics-api",
		Label:     "mcp-analytics-api",
		Aliases:   []string{"analytics"},
		PortTarget: &sentinelPortTarget{
			ResourceKind: "service",
			ResourceName: "mcp-analytics-api",
			LocalPort:    8085,
			RemotePort:   8085,
		},
	},
	{Key: "processor", Display: "Processor", Namespace: core.DefaultAnalyticsNamespace, Kind: "deployment", Resource: "mcp-sentinel-processor", Label: "mcp-sentinel-processor"},
	{
		Key:       "ui",
		Display:   "UI",
		Namespace: core.DefaultAnalyticsNamespace,
		Kind:      "deployment",
		Resource:  "mcp-sentinel-ui",
		Label:     "mcp-sentinel-ui",
		PortTarget: &sentinelPortTarget{
			ResourceKind: "service",
			ResourceName: "mcp-sentinel-ui",
			LocalPort:    8082,
			RemotePort:   8082,
		},
	},
	{Key: "gateway", Display: "Gateway", Namespace: core.DefaultAnalyticsNamespace, Kind: "deployment", Resource: "mcp-sentinel-gateway", Label: "mcp-sentinel-gateway"},
	{
		Key:       "prometheus",
		Display:   "Prometheus",
		Namespace: core.DefaultAnalyticsNamespace,
		Kind:      "deployment",
		Resource:  "prometheus",
		Label:     "prometheus",
		Aliases:   []string{"prom"},
		PortTarget: &sentinelPortTarget{
			ResourceKind: "service",
			ResourceName: "prometheus",
			LocalPort:    9090,
			RemotePort:   9090,
		},
	},
	{
		Key:       "grafana",
		Display:   "Grafana",
		Namespace: core.DefaultAnalyticsNamespace,
		Kind:      "deployment",
		Resource:  "grafana",
		Label:     "grafana",
		PortTarget: &sentinelPortTarget{
			ResourceKind: "service",
			ResourceName: "grafana",
			LocalPort:    3000,
			RemotePort:   3000,
		},
	},
	{Key: "otel-collector", Display: "OTel Collector", Namespace: core.DefaultAnalyticsNamespace, Kind: "deployment", Resource: "otel-collector", Label: "otel-collector", Aliases: []string{"otel"}},
	{Key: "tempo", Display: "Tempo", Namespace: core.DefaultAnalyticsNamespace, Kind: "statefulset", Resource: "tempo", Label: "tempo"},
	{Key: "loki", Display: "Loki", Namespace: core.DefaultAnalyticsNamespace, Kind: "statefulset", Resource: "loki", Label: "loki"},
	{Key: "promtail", Display: "Promtail", Namespace: core.LogCollectorNamespace, Kind: "daemonset", Resource: "promtail", Label: "promtail"},
}

// NewSentinelManager creates a SentinelManager with explicit dependencies.
func NewSentinelManager(kubectl *core.KubectlClient, logger *zap.Logger) *SentinelManager {
	return &SentinelManager{kubectl: kubectl, logger: logger}
}

// DefaultSentinelManager returns a SentinelManager using the shared runtime clients.
func DefaultSentinelManager(runtime *core.Runtime) *SentinelManager {
	return NewSentinelManager(runtime.KubectlClient(), runtime.Logger())
}

// ComponentKeys returns sorted valid component names for cobra completion.
func ComponentKeys() []string {
	keys := make([]string, 0, len(sentinelComponents))
	for _, component := range sentinelComponents {
		keys = append(keys, component.Key)
	}
	sort.Strings(keys)
	return keys
}

func findSentinelComponent(name string) (*sentinelComponent, error) {
	candidate := strings.ToLower(strings.TrimSpace(name))
	for i := range sentinelComponents {
		component := &sentinelComponents[i]
		if component.Key == candidate {
			return component, nil
		}
		for _, alias := range component.Aliases {
			if alias == candidate {
				return component, nil
			}
		}
	}

	return nil, core.NewWithSentinel(nil, fmt.Sprintf("unknown sentinel component %q (use one of: %s)", name, strings.Join(ComponentKeys(), ", ")))
}

func findSentinelPortTarget(name string) (*sentinelPortTarget, error) {
	component, err := findSentinelComponent(name)
	if err != nil {
		return nil, err
	}
	if component.PortTarget == nil {
		return nil, core.NewWithSentinel(nil, fmt.Sprintf("component %q does not expose a predefined port-forward target", name))
	}
	return component.PortTarget, nil
}

// ShowSentinelStatus prints a status table for sentinel workloads.
func (m *SentinelManager) ShowSentinelStatus() error {
	if err := m.requireAdminClusterAccess(); err != nil {
		return err
	}
	core.Header("MCP Sentinel Status")
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

// ViewSentinelLogs streams logs for a sentinel component.
func (m *SentinelManager) ViewSentinelLogs(component string, follow, previous bool, tail int, since string) error {
	if err := m.requireAdminClusterAccess(); err != nil {
		return err
	}
	target, err := findSentinelComponent(component)
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

// ShowSentinelEvents lists recent events in the analytics namespace.
func (m *SentinelManager) ShowSentinelEvents() error {
	if err := m.requireAdminClusterAccess(); err != nil {
		return err
	}
	args := []string{"get", "events", "-n", core.DefaultAnalyticsNamespace, "--sort-by=.lastTimestamp"}
	if err := m.kubectl.RunWithOutput(args, os.Stdout, os.Stderr); err != nil {
		return core.WrapWithSentinelAndContext(nil, err, fmt.Sprintf("failed to list sentinel events: %v", err), map[string]any{
			"namespace": core.DefaultAnalyticsNamespace,
			"component": "sentinel",
		})
	}
	return nil
}

// PortForwardSentinelTarget runs kubectl port-forward for a known service target.
func (m *SentinelManager) PortForwardSentinelTarget(target string, localPort int, address string) error {
	if err := m.requireAdminClusterAccess(); err != nil {
		return err
	}
	portTarget, err := findSentinelPortTarget(target)
	if err != nil {
		return err
	}
	if localPort <= 0 {
		localPort = portTarget.LocalPort
	}

	args := []string{
		"port-forward",
		"-n", core.DefaultAnalyticsNamespace,
		fmt.Sprintf("%s/%s", portTarget.ResourceKind, portTarget.ResourceName),
		fmt.Sprintf("%d:%d", localPort, portTarget.RemotePort),
		"--address", address,
	}

	if err := m.kubectl.RunWithOutput(args, os.Stdout, os.Stderr); err != nil {
		return core.WrapWithSentinelAndContext(nil, err, fmt.Sprintf("failed to port-forward sentinel target %q: %v", target, err), map[string]any{
			"target":    target,
			"namespace": core.DefaultAnalyticsNamespace,
			"component": "sentinel",
		})
	}
	return nil
}

// RestartSentinel restarts one component or all sentinel workloads.
func (m *SentinelManager) RestartSentinel(component string, restartAll bool) error {
	if err := m.requireAdminClusterAccess(); err != nil {
		return err
	}
	if restartAll {
		for _, target := range sentinelComponents {
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

	target, err := findSentinelComponent(component)
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

func (m *SentinelManager) requireAdminClusterAccess() error {
	if m.kubectl == nil {
		return core.NewWithSentinel(nil, kubeerr.DirectModeFailureMessage("sentinel commands require admin cluster access", "kubectl client is unavailable"))
	}
	cmd, err := m.kubectl.CommandArgs([]string{"cluster-info"})
	if err != nil {
		return core.NewWithSentinel(nil, kubeerr.DirectModeFailureMessage("sentinel commands require admin cluster access", err.Error()))
	}
	output, execErr := cmd.CombinedOutput()
	if execErr != nil {
		detail := kubeerr.CommandDetail(string(output), execErr)
		return core.NewWithSentinel(nil, kubeerr.DirectModeFailureMessage("sentinel commands require admin cluster access", detail))
	}
	return nil
}
