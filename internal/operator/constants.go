// Package operator provides the Kubernetes operator for MCPServer resources.
package operator

import "mcp-runtime/pkg/mcpdefaults"

// Resource defaults for MCPServer deployments.
const (
	// DefaultRequestCPU is the default CPU request for containers.
	DefaultRequestCPU = "50m"
	// DefaultRequestMemory is the default memory request for containers.
	DefaultRequestMemory = "64Mi"
	// DefaultLimitCPU is the default CPU limit for containers.
	DefaultLimitCPU = "500m"
	// DefaultLimitMemory is the default memory limit for containers.
	DefaultLimitMemory = "256Mi"
)

// MCPServer defaults.
const (
	// DefaultReplicas is the default number of replicas.
	DefaultReplicas = 1
	// DefaultPort is the default container port.
	DefaultPort = mcpdefaults.MCPServerPort
	// DefaultGatewayPort is the default container port for the MCP proxy sidecar.
	DefaultGatewayPort = mcpdefaults.MCPGatewayPort
	// DefaultGatewayMetricsPort is the default Prometheus scrape port for the MCP gateway sidecar.
	DefaultGatewayMetricsPort = 9103
	// DefaultServicePort is the default service port.
	DefaultServicePort = 80
)

// Labels used by the operator.
const (
	// LabelApp is the standard app label key.
	LabelApp = "app"
	// LabelManagedBy is the label indicating the managing controller.
	LabelManagedBy = "app.kubernetes.io/managed-by"
	// LabelManagedByValue is the value for the managed-by label.
	LabelManagedByValue = "mcp-runtime"
)

// Secret names.
const (
	// DefaultRegistrySecretName is the default name for registry pull secrets.
	// #nosec G101 -- This is a secret name, not a credential.
	DefaultRegistrySecretName = "mcp-runtime-registry-pull"
)

// Ingress configuration.
const (
	// DefaultIngressClass is the default ingress class.
	DefaultIngressClass = "traefik"
	// DefaultIngressPathType is the default path type for ingress rules.
	DefaultIngressPathType = "Prefix"
	// IngressReadinessModeStrict requires Ingress.status.loadBalancer.ingress to be populated.
	IngressReadinessModeStrict = "strict"
	// IngressReadinessModePermissive treats an Ingress with rules as ready when LB status is absent.
	IngressReadinessModePermissive = "permissive"
)

// Requeue delays for reconciliation.
const (
	// RequeueDelayNotReady is the delay before requeueing when resources are not ready.
	RequeueDelayNotReady = 10 // seconds
)

// Analytics defaults applied when the gateway sidecar emits events.
const (
	// defaultAnalyticsEventType is the event type label attached to analytics
	// events when the user has not overridden Spec.Analytics.EventType.
	defaultAnalyticsEventType = "mcp.request"
	// defaultAnalyticsSourceSuffix is appended to the MCPServer name to derive
	// the analytics Source label when Spec.Analytics.Source is empty.
	defaultAnalyticsSourceSuffix = "-gateway"
	// analyticsTokenAudience is the audience of the projected service account
	// token a gateway sidecar presents to mcp-ingest. Ingest validates it with
	// TokenReview, so no Secret has to cross from mcp-observability into the
	// server namespace. It must match INGEST_SA_TOKEN_AUDIENCE in k8s/06-ingest.yaml.
	analyticsTokenAudience = mcpdefaults.AnalyticsTokenAudience
	// analyticsTokenVolumeName and analyticsTokenMountDir locate the projected token.
	// #nosec G101 -- volume name and mount path only; no secret value is embedded.
	analyticsTokenVolumeName = "analytics-token"        // #nosec G101 -- volume name, not a credential
	analyticsTokenMountDir   = "/var/run/mcp-analytics" // #nosec G101 -- mount path, not a credential
	// analyticsTokenExpirySeconds is the requested token lifetime; the kubelet
	// refreshes the file well before it expires.
	analyticsTokenExpirySeconds int64 = 3600
)
