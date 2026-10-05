package platformstack

import (
	"fmt"
	"sort"
	"strings"

	"mcp-runtime/pkg/platforminventory"
)

// Component and PortTarget retain the platform API's existing field names.
type Component = platforminventory.Component
type PortTarget = platforminventory.PortTarget

const (
	PlatformNamespace      = platforminventory.PlatformNamespace
	ObservabilityNamespace = platforminventory.ObservabilityNamespace
	LogCollectorNamespace  = platforminventory.LogCollectorNamespace
	OperatorNamespace      = platforminventory.OperatorNamespace
)

// Components preserves the historical public status and management surface.
var Components = platforminventory.PlatformComponents(true)

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

// IsCoreComponent returns true if the component is part of the core platform runtime.
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
