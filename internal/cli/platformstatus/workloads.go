package platformstatus

import (
	"fmt"
	"strconv"
	"strings"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/kubeerr"
)

// PlatformWorkload identifies a namespaced workload for status tables.
type PlatformWorkload struct {
	Component string
	Namespace string
	Kind      string
	Name      string
}

// DefaultPlatformStatusWorkloads lists bundled analytics stack workloads for status output.
var DefaultPlatformStatusWorkloads = []PlatformWorkload{
	{Component: "ClickHouse", Namespace: core.DefaultAnalyticsNamespace, Kind: "statefulset", Name: "clickhouse"},
	{Component: "Kafka", Namespace: core.DefaultAnalyticsNamespace, Kind: "statefulset", Name: "kafka"},
	{Component: "Ingest", Namespace: core.DefaultAnalyticsNamespace, Kind: "deployment", Name: "mcp-sentinel-ingest"},
	{Component: "Processor", Namespace: core.DefaultAnalyticsNamespace, Kind: "deployment", Name: "mcp-sentinel-processor"},
	{Component: "Platform API", Namespace: core.DefaultAnalyticsNamespace, Kind: "deployment", Name: "mcp-platform-api"},
	{Component: "Runtime Control", Namespace: core.DefaultAnalyticsNamespace, Kind: "deployment", Name: "mcp-runtime-api"},
	{Component: "Analytics API", Namespace: core.DefaultAnalyticsNamespace, Kind: "deployment", Name: "mcp-analytics-api"},
	{Component: "UI", Namespace: core.DefaultAnalyticsNamespace, Kind: "deployment", Name: "mcp-sentinel-ui"},
	{Component: "Gateway", Namespace: core.DefaultAnalyticsNamespace, Kind: "deployment", Name: "mcp-sentinel-gateway"},
	{Component: "Prometheus", Namespace: core.DefaultAnalyticsNamespace, Kind: "deployment", Name: "prometheus"},
	{Component: "Grafana", Namespace: core.DefaultAnalyticsNamespace, Kind: "deployment", Name: "grafana"},
	{Component: "OTel Collector", Namespace: core.DefaultAnalyticsNamespace, Kind: "deployment", Name: "otel-collector"},
	{Component: "Tempo", Namespace: core.DefaultAnalyticsNamespace, Kind: "statefulset", Name: "tempo"},
	{Component: "Loki", Namespace: core.DefaultAnalyticsNamespace, Kind: "statefulset", Name: "loki"},
	{Component: "Promtail", Namespace: core.LogCollectorNamespace, Kind: "daemonset", Name: "promtail"},
}

// AnalyticsNamespaceInstalled reports whether the analytics namespace exists.
func AnalyticsNamespaceInstalled(kubectl core.KubectlRunner, clusterReachable bool) (bool, error) {
	if !clusterReachable {
		return false, nil
	}

	output, err := runKubectlCombinedOutput(kubectl, []string{"get", "namespace", core.DefaultAnalyticsNamespace, "-o", "jsonpath={.metadata.name}"})
	if err == nil {
		return strings.TrimSpace(output) == core.DefaultAnalyticsNamespace, nil
	}
	if strings.TrimSpace(output) == "" {
		return false, fmt.Errorf("empty output from namespace probe")
	}

	lower := strings.ToLower(output)
	if strings.Contains(lower, "not found") || strings.Contains(lower, "notfound") {
		return false, nil
	}

	return false, fmt.Errorf("%s", kubeerr.CommandDetail(output, err))
}

// AnalyticsStackRow builds a table row for the analytics namespace aggregate status.
func AnalyticsStackRow(status, details string) []string {
	ns := core.DefaultAnalyticsNamespace
	return []string{"Analytics Stack", ns, "namespace/" + ns, status, details}
}

// WorkloadStatusRow renders one workload row for platform status tables.
func WorkloadStatusRow(kubectl core.KubectlRunner, workload PlatformWorkload, clusterReachable bool) []string {
	resource := fmt.Sprintf("%s/%s", workload.Kind, workload.Name)
	if !clusterReachable {
		return []string{workload.Component, workload.Namespace, resource, core.Red("ERROR"), "Cluster unavailable"}
	}

	st, details := workloadReadinessStatus(kubectl, workload)
	return []string{workload.Component, workload.Namespace, resource, st, details}
}

func workloadReadinessStatus(kubectl core.KubectlRunner, workload PlatformWorkload) (string, string) {
	jsonPath, err := workloadReadyJSONPath(workload.Kind)
	if err != nil {
		return core.Red("ERROR"), err.Error()
	}

	output, cmdErr := runKubectlCombinedOutput(kubectl, []string{
		"get", workload.Kind, workload.Name,
		"-n", workload.Namespace,
		"-o", "jsonpath=" + jsonPath,
	})
	if cmdErr != nil {
		return core.Red("ERROR"), kubeerr.CommandDetail(output, cmdErr)
	}

	if workloadReady(output) {
		return core.Green("OK"), "Ready: " + output
	}
	return core.Yellow("PENDING"), "Ready: " + output
}

func workloadReadyJSONPath(kind string) (string, error) {
	switch strings.ToLower(kind) {
	case "deployment", "statefulset":
		return "{.status.readyReplicas}/{.spec.replicas}", nil
	case "daemonset":
		return "{.status.numberReady}/{.status.desiredNumberScheduled}", nil
	default:
		return "", fmt.Errorf("unsupported workload kind %q", kind)
	}
}

func workloadReady(value string) bool {
	parts := strings.Split(strings.TrimSpace(value), "/")
	if len(parts) != 2 {
		return false
	}

	ready, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return false
	}
	desired, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return false
	}
	return desired > 0 && ready >= desired
}
