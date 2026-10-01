package sentinel

import (
	"fmt"
	"sort"
	"strings"

	"mcp-runtime/pkg/mcpdefaults"
)

// Component represents a Sentinel stack component.
type Component struct {
	Key        string
	Display    string
	Namespace  string
	Kind       string
	Resource   string
	Label      string
	Aliases    []string
	PortTarget *PortTarget
}

// PortTarget defines a port forwarding target.
type PortTarget struct {
	ResourceKind string
	ResourceName string
	LocalPort    int
	RemotePort   int
}

const (
	DefaultNamespace      = "mcp-sentinel"
	LogCollectorNamespace = mcpdefaults.LogCollectorNamespace
	OperatorNamespace     = "mcp-runtime"
)

// Components is the registry of all Sentinel stack components.
var Components = []Component{
	{
		Key:       "operator",
		Display:   "Operator",
		Namespace: OperatorNamespace,
		Kind:      "deployment",
		Resource:  "mcp-runtime-operator-controller-manager",
		Label:     "mcp-runtime-operator-controller-manager",
	},
	{
		Key:       "clickhouse",
		Display:   "ClickHouse",
		Namespace: DefaultNamespace,
		Kind:      "statefulset",
		Resource:  "clickhouse",
		Label:     "clickhouse",
	},
	{
		Key:       "kafka",
		Display:   "Kafka",
		Namespace: DefaultNamespace,
		Kind:      "statefulset",
		Resource:  "kafka",
		Label:     "kafka",
	},
	{
		Key:       "ingest",
		Display:   "Ingest",
		Namespace: DefaultNamespace,
		Kind:      "deployment",
		Resource:  "mcp-sentinel-ingest",
		Label:     "mcp-sentinel-ingest",
	},
	{
		Key:       "platform-api",
		Display:   "Platform API",
		Namespace: DefaultNamespace,
		Kind:      "deployment",
		Resource:  "mcp-platform-api",
		Label:     "mcp-platform-api",
		Aliases:   []string{"api", "platform"},
		PortTarget: &PortTarget{
			ResourceKind: "service",
			ResourceName: "mcp-platform-api",
			LocalPort:    8080,
			RemotePort:   8080,
		},
	},
	{
		Key:       "runtime-api",
		Display:   "Runtime Control",
		Namespace: DefaultNamespace,
		Kind:      "deployment",
		Resource:  "mcp-runtime-api",
		Label:     "mcp-runtime-api",
		Aliases:   []string{"runtime"},
		PortTarget: &PortTarget{
			ResourceKind: "service",
			ResourceName: "mcp-runtime-api",
			LocalPort:    8084,
			RemotePort:   8084,
		},
	},
	{
		Key:       "analytics-api",
		Display:   "Analytics API",
		Namespace: DefaultNamespace,
		Kind:      "deployment",
		Resource:  "mcp-analytics-api",
		Label:     "mcp-analytics-api",
		Aliases:   []string{"analytics"},
		PortTarget: &PortTarget{
			ResourceKind: "service",
			ResourceName: "mcp-analytics-api",
			LocalPort:    8085,
			RemotePort:   8085,
		},
	},
	{
		Key:       "processor",
		Display:   "Processor",
		Namespace: DefaultNamespace,
		Kind:      "deployment",
		Resource:  "mcp-sentinel-processor",
		Label:     "mcp-sentinel-processor",
	},
	{
		Key:       "ui",
		Display:   "UI",
		Namespace: DefaultNamespace,
		Kind:      "deployment",
		Resource:  "mcp-sentinel-ui",
		Label:     "mcp-sentinel-ui",
		PortTarget: &PortTarget{
			ResourceKind: "service",
			ResourceName: "mcp-sentinel-ui",
			LocalPort:    8082,
			RemotePort:   8082,
		},
	},
	{
		Key:       "gateway",
		Display:   "Gateway",
		Namespace: DefaultNamespace,
		Kind:      "deployment",
		Resource:  "mcp-sentinel-gateway",
		Label:     "mcp-sentinel-gateway",
	},
	{
		Key:       "prometheus",
		Display:   "Prometheus",
		Namespace: DefaultNamespace,
		Kind:      "deployment",
		Resource:  "prometheus",
		Label:     "prometheus",
		Aliases:   []string{"prom"},
		PortTarget: &PortTarget{
			ResourceKind: "service",
			ResourceName: "prometheus",
			LocalPort:    9090,
			RemotePort:   9090,
		},
	},
	{
		Key:       "grafana",
		Display:   "Grafana",
		Namespace: DefaultNamespace,
		Kind:      "deployment",
		Resource:  "grafana",
		Label:     "grafana",
		PortTarget: &PortTarget{
			ResourceKind: "service",
			ResourceName: "grafana",
			LocalPort:    3000,
			RemotePort:   3000,
		},
	},
	{
		Key:       "otel-collector",
		Display:   "OTel Collector",
		Namespace: DefaultNamespace,
		Kind:      "deployment",
		Resource:  "otel-collector",
		Label:     "otel-collector",
		Aliases:   []string{"otel"},
	},
	{
		Key:       "tempo",
		Display:   "Tempo",
		Namespace: DefaultNamespace,
		Kind:      "statefulset",
		Resource:  "tempo",
		Label:     "tempo",
	},
	{
		Key:       "loki",
		Display:   "Loki",
		Namespace: DefaultNamespace,
		Kind:      "statefulset",
		Resource:  "loki",
		Label:     "loki",
	},
	{
		Key:       "promtail",
		Display:   "Promtail",
		Namespace: LogCollectorNamespace,
		Kind:      "daemonset",
		Resource:  "promtail",
		Label:     "promtail",
	},
}

// GetComponentKeys returns sorted list of all component keys.
func GetComponentKeys() []string {
	keys := make([]string, 0, len(Components))
	for _, c := range Components {
		keys = append(keys, c.Key)
	}
	sort.Strings(keys)
	return keys
}

// FindComponent finds a component by key or alias.
func FindComponent(name string) (*Component, error) {
	candidate := strings.ToLower(strings.TrimSpace(name))
	for i := range Components {
		c := &Components[i]
		if c.Key == candidate {
			return c, nil
		}
		for _, alias := range c.Aliases {
			if alias == candidate {
				return c, nil
			}
		}
	}
	return nil, fmt.Errorf("unknown component %q (valid: %s)", name, strings.Join(GetComponentKeys(), ", "))
}

// FindPortTarget finds a component with a port forwarding target.
func FindPortTarget(name string) (*PortTarget, error) {
	component, err := FindComponent(name)
	if err != nil {
		return nil, err
	}
	if component.PortTarget == nil {
		return nil, fmt.Errorf("component %q has no port-forward target", name)
	}
	return component.PortTarget, nil
}

// IsCoreComponent returns true if the component is part of the core Sentinel runtime.
func IsCoreComponent(key string) bool {
	core := map[string]bool{
		"platform-api":  true,
		"runtime-api":   true,
		"analytics-api": true,
		"api":           true,
		"ingest":        true,
		"processor":     true,
		"gateway":       true,
		"ui":            true,
	}
	return core[key]
}

// IsAnalyticsComponent returns true if the component is part of analytics/observability.
func IsAnalyticsComponent(key string) bool {
	analytics := map[string]bool{
		"clickhouse":     true,
		"kafka":          true,
		"prometheus":     true,
		"grafana":        true,
		"otel-collector": true,
		"tempo":          true,
		"loki":           true,
		"promtail":       true,
	}
	return analytics[key]
}

// ComponentStatus represents the runtime status of a component.
type ComponentStatus struct {
	Key       string `json:"key"`
	Display   string `json:"display"`
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`
	Resource  string `json:"resource"`
	Status    string `json:"status"`
	Ready     string `json:"ready"`
	Message   string `json:"message,omitempty"`
}

// StatusFromWorkload creates a ComponentStatus from Kubernetes workload info.
func StatusFromWorkload(component Component, readyReplicas, desiredReplicas int32, message string) ComponentStatus {
	status := ComponentStatus{
		Key:       component.Key,
		Display:   component.Display,
		Namespace: component.Namespace,
		Kind:      component.Kind,
		Resource:  component.Resource,
		Ready:     fmt.Sprintf("%d/%d", readyReplicas, desiredReplicas),
	}

	if desiredReplicas == 0 {
		status.Status = "NotDeployed"
		status.Message = "No replicas configured"
	} else if readyReplicas == desiredReplicas {
		status.Status = "Ready"
	} else if readyReplicas > 0 {
		status.Status = "Degraded"
		if message != "" {
			status.Message = message
		}
	} else {
		status.Status = "NotReady"
		if message != "" {
			status.Message = message
		}
	}

	return status
}
