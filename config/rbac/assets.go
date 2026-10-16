// Package rbacassets exposes the canonical operator rules to diagnostics, including
// when the CLI runs outside a source checkout.
package rbacassets

import _ "embed"

// OperatorRole is generated from the operator's kubebuilder markers.
//
//go:embed role.yaml
var OperatorRole []byte

// ManagedSecrets is bound only in managed server namespaces.
//
//go:embed operator_secret_access.yaml
var ManagedSecrets []byte

// ManagedWorkloads is bound only in managed server namespaces.
//
//go:embed operator_workload_access.yaml
var ManagedWorkloads []byte
