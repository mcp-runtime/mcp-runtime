package core

import (
	"mcp-runtime/pkg/mcpdefaults"
	"mcp-runtime/pkg/platforminventory"
)

// This file defines constants used across the CLI, including:
//   - Kubernetes namespace names
//   - Deployment and resource names
//   - Label selectors
//   - Default values
const (
	// NamespaceMCPRuntime is the namespace for the MCP runtime operator.
	NamespaceMCPRuntime = "mcp-runtime"

	// NamespaceRegistry is the namespace for the container registry.
	NamespaceRegistry = "registry"

	// NamespaceMCPServers is the default namespace for MCP server deployments.
	NamespaceMCPServers = mcpdefaults.MCPServersNamespace

	// PlatformNamespace holds control-plane services.
	PlatformNamespace = platforminventory.PlatformNamespace

	// ObservabilityNamespace holds the event pipeline and telemetry stack.
	ObservabilityNamespace = platforminventory.ObservabilityNamespace

	// LogCollectorNamespace isolates the node log collector and its hostPath access.
	LogCollectorNamespace = mcpdefaults.LogCollectorNamespace
)

// Deployment and resource names.
const (
	// OperatorDeploymentName is the name of the operator deployment.
	OperatorDeploymentName = "mcp-runtime-operator-controller-manager"

	// OperatorManagerContainerName is the main manager container in the operator deployment.
	OperatorManagerContainerName = "manager"

	// RegistryDeploymentName is the name of the registry deployment.
	RegistryDeploymentName = "registry"

	// RegistryServiceName is the name of the registry service.
	RegistryServiceName = "registry"

	// RegistryPVCName is the name of the registry persistent volume claim.
	RegistryPVCName = "registry-storage"
)

// CRD identifiers.
const (
	// MCPServerCRDName is the full name of the MCPServer CRD.
	MCPServerCRDName = "mcpservers.mcpruntime.org"

	// CertManagerCRDName is the full name of the cert-manager Certificate CRD.
	CertManagerCRDName = "certificates.cert-manager.io"
)

// Labels used for resource identification.
const (
	// LabelApp is the standard app label key.
	LabelApp = "app"

	// LabelManagedBy is the label indicating the managing controller.
	LabelManagedBy = "app.kubernetes.io/managed-by"

	// LabelManagedByValue is the value for the managed-by label.
	LabelManagedByValue = "mcp-runtime"
)

// Selector strings for kubectl queries.
const (
	// SelectorRegistry is the label selector for registry pods.
	SelectorRegistry = "app=registry"

	// SelectorOperator is the label selector for operator pods.
	SelectorOperator = "control-plane=controller-manager"

	// SelectorManagedBy is the label selector for MCP-managed resources.
	SelectorManagedBy = "app.kubernetes.io/managed-by=mcp-runtime"
)

// ComponentNamespace returns the install namespace for a catalog component.
func ComponentNamespace(key string) string {
	component, ok := platforminventory.Lookup(key)
	if !ok {
		return ""
	}
	return component.Namespace
}
