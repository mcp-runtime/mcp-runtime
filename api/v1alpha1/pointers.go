package v1alpha1

// BoolPtr returns a pointer to v for optional CRD boolean fields.
func BoolPtr(v bool) *bool {
	return &v
}

// GatewayIsEnabled reports whether the MCP gateway sidecar should run.
// Omitted gateway, empty gateway: {}, and enabled: true all mean on.
// Only enabled: false opts out.
func GatewayIsEnabled(gateway *GatewayConfig) bool {
	if gateway == nil || gateway.Enabled == nil {
		return true
	}
	return *gateway.Enabled
}
