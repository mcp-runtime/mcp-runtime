package platform

import "mcp-runtime/pkg/platforminventory"

// workloadNamespace resolves a component key or Kubernetes resource name
// through the inventory. Callers name the workload; they do not choose a namespace.
func workloadNamespace(name string) string {
	if component, ok := platforminventory.Lookup(name); ok && component.Namespace != "" {
		return component.Namespace
	}
	for _, component := range platforminventory.Catalog() {
		if component.Resource == name || component.Label == name {
			return component.Namespace
		}
	}
	return ""
}
