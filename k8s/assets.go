// Package platformassets exposes selected canonical platform manifests to CLI
// diagnostics without requiring a source checkout.
package platformassets

import _ "embed"

// OperatorBundledAuth limits the operator's writes to the bundled issuer.
//
//go:embed 23-mcp-auth-rbac.yaml
var OperatorBundledAuth []byte
