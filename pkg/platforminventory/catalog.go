// Package platforminventory defines component identity, ownership and placement.
// It does not provision resources or authorize namespace access.
package platforminventory

import "strings"

type Owner string

const (
	Operator      Owner = "operator"
	Platform      Owner = "platform"
	Observability Owner = "observability"
	LogCollector  Owner = "log-collector"
	Certificates  Owner = "certificates"
	Registry      Owner = "registry"
	Ingress       Owner = "ingress"
)

type Capability string

const (
	Control    Capability = "control"
	Telemetry  Capability = "telemetry"
	Routing    Capability = "routing"
	Persistent Capability = "persistent"
	NodeLogs   Capability = "node-logs"
	Optional   Capability = "optional"
	External   Capability = "external"
)

// Dependency describes a service relationship, not rollout order. Optional
// dependencies must not block the control plane during telemetry outages.
type Dependency struct {
	Component string
	Optional  bool
}

// Component identifies a stable platform component independently of placement.
type Component struct {
	Owner        Owner        `json:"-"`
	Capabilities []Capability `json:"-"`
	Dependencies []Dependency `json:"-"`
	Sentinel     bool         `json:"-"`
	Key          string
	Display      string
	Namespace    string
	Kind         string
	Resource     string
	Label        string
	Aliases      []string
	PortTarget   *PortTarget
}

// PortTarget defines a port forwarding target.
type PortTarget struct {
	ResourceKind string
	ResourceName string
	LocalPort    int
	RemotePort   int
}

const (
	DefaultNamespace  = "mcp-sentinel"
	OperatorNamespace = "mcp-runtime"
)

// catalog keeps the historical Sentinel management order.
var catalog = []Component{
	{
		Key:   "operator",
		Owner: Operator, Sentinel: true,
		Capabilities: []Capability{Control},
		Display:      "Operator",
		Namespace:    OperatorNamespace,
		Kind:         "deployment",
		Resource:     "mcp-runtime-operator-controller-manager",
		Label:        "mcp-runtime-operator-controller-manager",
	},
	{
		Key:   "clickhouse",
		Owner: Observability, Sentinel: true,
		Capabilities: []Capability{Telemetry, Persistent},
		Display:      "ClickHouse",
		Namespace:    DefaultNamespace,
		Kind:         "statefulset",
		Resource:     "clickhouse",
		Label:        "clickhouse",
	},
	{
		Key:   "kafka",
		Owner: Observability, Sentinel: true,
		Capabilities: []Capability{Telemetry, Persistent},
		Display:      "Kafka",
		Namespace:    DefaultNamespace,
		Kind:         "statefulset",
		Resource:     "kafka",
		Label:        "kafka",
	},
	{
		Key:   "ingest",
		Owner: Observability, Sentinel: true,
		Capabilities: []Capability{Telemetry},
		Dependencies: []Dependency{{Component: "kafka", Optional: false}},
		Display:      "Ingest",
		Namespace:    DefaultNamespace,
		Kind:         "deployment",
		Resource:     "mcp-sentinel-ingest",
		Label:        "mcp-sentinel-ingest",
	},
	{
		Key:   "platform-api",
		Owner: Platform, Sentinel: true,
		Capabilities: []Capability{Control},
		Dependencies: []Dependency{{Component: "postgres", Optional: false}, {Component: "runtime-api", Optional: false}},
		Display:      "Platform API",
		Namespace:    DefaultNamespace,
		Kind:         "deployment",
		Resource:     "mcp-platform-api",
		Label:        "mcp-platform-api",
		Aliases:      []string{"api", "platform"},
		PortTarget: &PortTarget{
			ResourceKind: "service",
			ResourceName: "mcp-platform-api",
			LocalPort:    8080,
			RemotePort:   8080,
		},
	},
	{
		Key:   "runtime-api",
		Owner: Platform, Sentinel: true,
		Capabilities: []Capability{Control},
		Dependencies: []Dependency{{Component: "platform-api", Optional: false}, {Component: "clickhouse", Optional: true}},
		Display:      "Runtime Control",
		Namespace:    DefaultNamespace,
		Kind:         "deployment",
		Resource:     "mcp-runtime-api",
		Label:        "mcp-runtime-api",
		Aliases:      []string{"runtime"},
		PortTarget: &PortTarget{
			ResourceKind: "service",
			ResourceName: "mcp-runtime-api",
			LocalPort:    8084,
			RemotePort:   8084,
		},
	},
	{
		Key:   "analytics-api",
		Owner: Observability, Sentinel: true,
		Capabilities: []Capability{Telemetry},
		Dependencies: []Dependency{{Component: "clickhouse", Optional: false}, {Component: "platform-api", Optional: false}},
		Display:      "Analytics API",
		Namespace:    DefaultNamespace,
		Kind:         "deployment",
		Resource:     "mcp-analytics-api",
		Label:        "mcp-analytics-api",
		Aliases:      []string{"analytics"},
		PortTarget: &PortTarget{
			ResourceKind: "service",
			ResourceName: "mcp-analytics-api",
			LocalPort:    8085,
			RemotePort:   8085,
		},
	},
	{
		Key:   "processor",
		Owner: Observability, Sentinel: true,
		Capabilities: []Capability{Telemetry},
		Dependencies: []Dependency{{Component: "kafka", Optional: false}, {Component: "clickhouse", Optional: false}},
		Display:      "Processor",
		Namespace:    DefaultNamespace,
		Kind:         "deployment",
		Resource:     "mcp-sentinel-processor",
		Label:        "mcp-sentinel-processor",
	},
	{
		Key:   "ui",
		Owner: Platform, Sentinel: true,
		Capabilities: []Capability{Control},
		Dependencies: []Dependency{{Component: "platform-api", Optional: false}, {Component: "runtime-api", Optional: false}, {Component: "analytics-api", Optional: true}},
		Display:      "UI",
		Namespace:    DefaultNamespace,
		Kind:         "deployment",
		Resource:     "mcp-sentinel-ui",
		Label:        "mcp-sentinel-ui",
		PortTarget: &PortTarget{
			ResourceKind: "service",
			ResourceName: "mcp-sentinel-ui",
			LocalPort:    8082,
			RemotePort:   8082,
		},
	},
	{
		Key:   "gateway",
		Owner: Platform, Sentinel: true,
		Capabilities: []Capability{Routing},
		Dependencies: []Dependency{{Component: "ui", Optional: false}, {Component: "platform-api", Optional: false}, {Component: "runtime-api", Optional: false}, {Component: "analytics-api", Optional: true}, {Component: "grafana", Optional: true}},
		Display:      "Gateway",
		Namespace:    DefaultNamespace,
		Kind:         "deployment",
		Resource:     "mcp-sentinel-gateway",
		Label:        "mcp-sentinel-gateway",
	},
	{
		Key:   "prometheus",
		Owner: Observability, Sentinel: true,
		Capabilities: []Capability{Telemetry},
		Display:      "Prometheus",
		Namespace:    DefaultNamespace,
		Kind:         "deployment",
		Resource:     "prometheus",
		Label:        "prometheus",
		Aliases:      []string{"prom"},
		PortTarget: &PortTarget{
			ResourceKind: "service",
			ResourceName: "prometheus",
			LocalPort:    9090,
			RemotePort:   9090,
		},
	},
	{
		Key:   "grafana",
		Owner: Observability, Sentinel: true,
		Capabilities: []Capability{Telemetry},
		Dependencies: []Dependency{{Component: "prometheus", Optional: true}, {Component: "loki", Optional: true}, {Component: "tempo", Optional: true}},
		Display:      "Grafana",
		Namespace:    DefaultNamespace,
		Kind:         "deployment",
		Resource:     "grafana",
		Label:        "grafana",
		PortTarget: &PortTarget{
			ResourceKind: "service",
			ResourceName: "grafana",
			LocalPort:    3000,
			RemotePort:   3000,
		},
	},
	{
		Key:   "otel-collector",
		Owner: Observability, Sentinel: true,
		Capabilities: []Capability{Telemetry},
		Dependencies: []Dependency{{Component: "tempo", Optional: false}},
		Display:      "OTel Collector",
		Namespace:    DefaultNamespace,
		Kind:         "deployment",
		Resource:     "otel-collector",
		Label:        "otel-collector",
		Aliases:      []string{"otel"},
	},
	{
		Key:   "tempo",
		Owner: Observability, Sentinel: true,
		Capabilities: []Capability{Telemetry, Persistent},
		Display:      "Tempo",
		Namespace:    DefaultNamespace,
		Kind:         "statefulset",
		Resource:     "tempo",
		Label:        "tempo",
	},
	{
		Key:   "loki",
		Owner: Observability, Sentinel: true,
		Capabilities: []Capability{Telemetry, Persistent},
		Display:      "Loki",
		Namespace:    DefaultNamespace,
		Kind:         "statefulset",
		Resource:     "loki",
		Label:        "loki",
	},
	{
		Key:   "promtail",
		Owner: LogCollector, Sentinel: true,
		Capabilities: []Capability{Telemetry, NodeLogs},
		Dependencies: []Dependency{{Component: "loki", Optional: false}},
		Display:      "Promtail",
		Namespace:    DefaultNamespace,
		Kind:         "daemonset",
		Resource:     "promtail",
		Label:        "promtail",
	},

	{Key: "registry", Display: "Registry", Owner: Registry, Namespace: "registry", Kind: "deployment", Resource: "registry", Label: "registry", Capabilities: []Capability{Persistent}},
	{Key: "traefik", Display: "Traefik", Owner: Ingress, Namespace: "traefik", Kind: "deployment", Resource: "traefik", Label: "traefik", Capabilities: []Capability{Routing, External}},

	{Key: "postgres", Display: "Postgres", Owner: Platform, Namespace: DefaultNamespace, Kind: "statefulset", Resource: "mcp-sentinel-postgres", Label: "mcp-sentinel-postgres", Capabilities: []Capability{Control, Persistent}},
	{Key: "mcp-auth", Display: "MCP Auth", Owner: Platform, Namespace: DefaultNamespace, Kind: "deployment", Resource: "mcp-auth-server", Label: "mcp-auth-server", Capabilities: []Capability{Control, Persistent, Optional}},
	{Key: "gateway-proxy", Display: "Gateway Proxy Image", Owner: Operator, Namespace: OperatorNamespace, Kind: "deployment", Resource: "mcp-runtime-operator-controller-manager", Label: "mcp-runtime-operator-controller-manager", Capabilities: []Capability{Control}},
	{Key: "cert-manager-controller", Display: "cert-manager", Owner: Certificates, Namespace: "cert-manager", Kind: "deployment", Resource: "cert-manager", Capabilities: []Capability{External, Optional}},
	{Key: "cert-manager-webhook", Display: "cert-manager webhook", Owner: Certificates, Namespace: "cert-manager", Kind: "deployment", Resource: "cert-manager-webhook", Capabilities: []Capability{External, Optional}},
	{Key: "cert-manager-cainjector", Display: "cert-manager CA injector", Owner: Certificates, Namespace: "cert-manager", Kind: "deployment", Resource: "cert-manager-cainjector", Capabilities: []Capability{External, Optional}},
	{Key: "doctor-smoke", Display: "Doctor Smoke Image", Owner: Operator, Capabilities: []Capability{Optional}},
}

// Catalog returns an independent copy, including nested metadata.
func Catalog() []Component {
	out := make([]Component, len(catalog))
	for i, c := range catalog {
		out[i] = clone(c)
	}
	return out
}

func clone(c Component) Component {
	c.Aliases = append([]string(nil), c.Aliases...)
	c.Capabilities = append([]Capability(nil), c.Capabilities...)
	c.Dependencies = append([]Dependency(nil), c.Dependencies...)
	if c.PortTarget != nil {
		target := *c.PortTarget
		c.PortTarget = &target
	}
	return c
}

// Lookup accepts stable identifiers only. Release manifests cannot select aliases.
func Lookup(key string) (Component, bool) {
	for _, c := range catalog {
		if c.Key == key {
			return clone(c), true
		}
	}
	return Component{}, false
}

// Find accepts the historical case-insensitive CLI aliases.
func Find(name string) (Component, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, c := range catalog {
		if c.Key == name {
			return clone(c), true
		}
		for _, alias := range c.Aliases {
			if alias == name {
				return clone(c), true
			}
		}
	}
	return Component{}, false
}

// SentinelComponents preserves the existing management surface. Image-only,
// optional authentication and database components are deliberately not added
// to restart-all or to the public status response by inventory consolidation.
func SentinelComponents(includeOperator bool) []Component {
	var out []Component
	for _, c := range Catalog() {
		if c.Sentinel && (includeOperator || c.Key != "operator") {
			out = append(out, c)
		}
	}
	return out
}
