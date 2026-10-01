// Package platformrelease defines the MCP Runtime platform component catalog,
// the per-release component manifest format, and version/image helpers shared
// by `mcp-runtime setup` (version stamping) and `mcp-runtime update`.
package platformrelease

import (
	"mcp-runtime/pkg/platforminventory"
	"sort"
)

// Metadata keys written on platform Deployments. They live on Deployment
// metadata (never the pod template) so writing them never triggers a rollout.
const (
	LabelPartOf           = "app.kubernetes.io/part-of"
	LabelPartOfValue      = "mcp-runtime"
	LabelComponent        = "mcpruntime.org/component"
	AnnotationVersion     = "mcpruntime.org/platform-version"
	annotationPreviousPre = "mcpruntime.org/previous-image."
)

// PreviousImageAnnotation returns the annotation key update uses to record the
// image a component ran before the most recent update, for manual recovery.
func PreviousImageAnnotation(component string) string {
	return annotationPreviousPre + component
}

// OptIn classifies components that update only touches when explicitly selected.
type OptIn string

const (
	// OptInNone components are updated by default.
	OptInNone OptIn = ""
	// OptInAuth covers the optional mcp-auth authorization server.
	OptInAuth OptIn = "auth"
	// OptInCertManager covers upstream cert-manager workloads.
	OptInCertManager OptIn = "cert-manager"
)

// Component maps a release-manifest component name to the single workload
// container (or env var) that carries its image. The CLI owns this mapping;
// release manifests only provide image coordinates, so a manifest can never
// direct update at an arbitrary workload.
type Component struct {
	// Name is the stable component identifier used in manifests and --only.
	Name string
	// Namespace and Deployment identify the workload. Empty Deployment means
	// the image has no long-running workload (for example doctor-smoke).
	Namespace  string
	Deployment string
	// Container is the container name inside the Deployment pod template.
	Container string
	// EnvVar, when set, means the image is stored in this env var on the
	// container instead of the container image (gateway proxy sidecar image).
	EnvVar string
	// Repository is the default image repository, used for manifest generation.
	// Empty for components MCP Runtime does not build.
	Repository string
	// Built reports whether MCP Runtime builds and versions this image with
	// the platform release (so the platform-version annotation applies).
	Built bool
	// OptIn marks components excluded unless explicitly selected.
	OptIn OptIn
	// Note is shown in plans when the component changes.
	Note string
}

// HasWorkload reports whether the component is backed by a Deployment.
func (c Component) HasWorkload() bool { return c.Deployment != "" }

const (
	nsRuntime = platforminventory.OperatorNamespace

	// OperatorDeployment is the operator controller-manager Deployment name.
	OperatorDeployment = "mcp-runtime-operator-controller-manager"
	// OperatorNamespace is the namespace the operator runs in.
	OperatorNamespace = nsRuntime
)

// catalog is ordered in rollout order: cert-manager first (when selected),
// then the operator, then Sentinel services, then mcp-auth.
var catalog = withInventoryPlacement([]Component{
	{Name: "cert-manager-controller", Container: "cert-manager-controller", OptIn: OptInCertManager},
	{Name: "cert-manager-webhook", Container: "cert-manager-webhook", OptIn: OptInCertManager},
	{Name: "cert-manager-cainjector", Container: "cert-manager-cainjector", OptIn: OptInCertManager},
	{Name: "operator", Container: "manager", Repository: "mcp-runtime-operator", Built: true},
	{Name: "gateway-proxy", Container: "manager", EnvVar: "MCP_GATEWAY_PROXY_IMAGE", Repository: "mcp-gateway", Built: true,
		Note: "operator re-renders MCPServer gateway sidecars; tenant MCP server pods will restart"},
	{Name: "platform-api", Container: "platform-api", Repository: "mcp-platform-api", Built: true},
	{Name: "runtime-api", Container: "runtime-api", Repository: "mcp-runtime-api", Built: true},
	{Name: "analytics-api", Container: "analytics-api", Repository: "mcp-analytics-api", Built: true},
	{Name: "ingest", Container: "ingest", Repository: "mcp-ingest", Built: true},
	{Name: "processor", Container: "processor", Repository: "mcp-processor", Built: true},
	{Name: "ui", Container: "ui", Repository: "mcp-ui", Built: true},
	{Name: "doctor-smoke", Repository: "mcp-runtime-doctor-smoke", Built: true},
	{Name: "mcp-auth", Container: "auth-server", OptIn: OptInAuth},
})

func withInventoryPlacement(components []Component) []Component {
	for i := range components {
		c, ok := platforminventory.Lookup(components[i].Name)
		if !ok {
			panic("unknown release component: " + components[i].Name)
		}
		if c.Kind != "" && c.Kind != "deployment" {
			panic("release component must be deployment-backed: " + c.Key)
		}
		components[i].Namespace = c.Namespace
		components[i].Deployment = c.Resource
	}
	return components
}

// Catalog returns a copy of the component catalog in rollout order.
func Catalog() []Component {
	out := make([]Component, len(catalog))
	copy(out, catalog)
	return out
}

// Lookup returns the catalog component with the given name.
func Lookup(name string) (Component, bool) {
	for _, c := range catalog {
		if c.Name == name {
			return c, true
		}
	}
	return Component{}, false
}

// ComponentNames returns all catalog component names, sorted.
func ComponentNames() []string {
	names := make([]string, 0, len(catalog))
	for _, c := range catalog {
		names = append(names, c.Name)
	}
	sort.Strings(names)
	return names
}
