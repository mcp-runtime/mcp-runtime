package platformstatus

import (
	"fmt"
	"strconv"
	"strings"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/kubeerr"
	"mcp-runtime/pkg/platforminventory"
)

// PlatformWorkload identifies a namespaced workload for status tables.
type PlatformWorkload struct {
	Component string
	Namespace string
	Kind      string
	Name      string
}

// DefaultPlatformStatusWorkloads lists bundled analytics stack workloads for status output.
var DefaultPlatformStatusWorkloads = defaultPlatformStatusWorkloads()

func defaultPlatformStatusWorkloads() []PlatformWorkload {
	// Preserve the existing status table order separately from identity/placement.
	keys := []string{"clickhouse", "kafka", "ingest", "processor", "platform-api", "runtime-api", "analytics-api", "ui", "gateway", "prometheus", "grafana", "otel-collector", "tempo", "loki", "promtail"}
	out := make([]PlatformWorkload, 0, len(keys))
	for _, key := range keys {
		c, ok := platforminventory.Lookup(key)
		if !ok {
			panic("unknown status component: " + key)
		}
		out = append(out, PlatformWorkload{Component: c.Display, Namespace: c.Namespace, Kind: c.Kind, Name: c.Resource})
	}
	return out
}

// AnalyticsNamespaceInstalled reports whether the analytics namespace exists.
func AnalyticsNamespaceInstalled(kubectl core.KubectlRunner, clusterReachable bool) (bool, error) {
	if !clusterReachable {
		return false, nil
	}

	output, err := runKubectlCombinedOutput(kubectl, []string{"get", "namespace", core.ComponentNamespace("platform-api"), "-o", "jsonpath={.metadata.name}"})
	if err == nil {
		return strings.TrimSpace(output) == core.ComponentNamespace("platform-api"), nil
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
	ns := core.ComponentNamespace("platform-api")
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
