package ingressmanifest

// APIPath describes a Traefik/Kubernetes ingress route for /api/v1 split services.
type APIPath struct {
	Path     string
	PathType string
	Service  string
	Port     int
}

// PlatformAPIPaths returns platform-namespace ingress rules, most-specific first.
func PlatformAPIPaths() []APIPath {
	return []APIPath{
		{Path: "/api/v1/runtime/registry/push", PathType: "Exact", Service: "mcp-runtime-api", Port: 8084},
		{Path: "/api/v1/admin/namespaces", PathType: "Prefix", Service: "mcp-platform-api", Port: 8080},
		{Path: "/api/v1/admin/audit", PathType: "Prefix", Service: "mcp-platform-api", Port: 8080},
		{Path: "/api/v1/auth", PathType: "Prefix", Service: "mcp-platform-api", Port: 8080},
		{Path: "/api/v1/users", PathType: "Prefix", Service: "mcp-platform-api", Port: 8080},
		{Path: "/api/v1/registry/pull-credentials", PathType: "Exact", Service: "mcp-platform-api", Port: 8080},
		{Path: "/api/v1/registry/token", PathType: "Exact", Service: "mcp-platform-api", Port: 8080},
		{Path: "/api/v1/registry/authz", PathType: "Prefix", Service: "mcp-platform-api", Port: 8080},
		{Path: "/api/v1/user/registry-credentials", PathType: "Prefix", Service: "mcp-platform-api", Port: 8080},
		{Path: "/api/v1/user/activity", PathType: "Prefix", Service: "mcp-platform-api", Port: 8080},
		{Path: "/api/v1/admin/deployments", PathType: "Prefix", Service: "mcp-runtime-api", Port: 8084},
		{Path: "/api/v1/user/api-keys", PathType: "Prefix", Service: "mcp-runtime-api", Port: 8084},
		{Path: "/api/v1/admin", PathType: "Prefix", Service: "mcp-runtime-api", Port: 8084},
		{Path: "/api/v1/runtime", PathType: "Prefix", Service: "mcp-runtime-api", Port: 8084},
		{Path: "/api/v1/deployments", PathType: "Prefix", Service: "mcp-runtime-api", Port: 8084},
		{Path: "/api/v1/dashboard", PathType: "Prefix", Service: "mcp-runtime-api", Port: 8084},
	}
}

// ObservabilityAPIPaths returns observability-namespace ingress rules.
func ObservabilityAPIPaths() []APIPath {
	return []APIPath{
		{Path: "/api/v1/user/analytics", PathType: "Prefix", Service: "mcp-analytics-api", Port: 8085},
		{Path: "/api/v1/events", PathType: "Prefix", Service: "mcp-analytics-api", Port: 8085},
		{Path: "/api/v1/event-types", PathType: "Prefix", Service: "mcp-analytics-api", Port: 8085},
		{Path: "/api/v1/stats", PathType: "Prefix", Service: "mcp-analytics-api", Port: 8085},
		{Path: "/api/v1/sources", PathType: "Prefix", Service: "mcp-analytics-api", Port: 8085},
		{Path: "/api/v1/analytics", PathType: "Prefix", Service: "mcp-analytics-api", Port: 8085},
	}
}
