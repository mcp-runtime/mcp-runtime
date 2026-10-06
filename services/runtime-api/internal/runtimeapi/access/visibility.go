package access

import mcpv1alpha1 "mcp-runtime/api/v1alpha1"
import mcpaccess "mcp-runtime/pkg/access"

// AccessRefVisibleWithServerCache resolves a server reference against a cache
// and falls back to namespace-level label visibility when the server is absent.
func AccessRefVisibleWithServerCache(
	resourceNamespace string,
	ref mcpaccess.ServerReference,
	cache map[string]mcpv1alpha1.MCPServer,
	canAdministerServer func(mcpv1alpha1.MCPServer) bool,
	canAdministerServerLabels func(namespace string, serverLabels map[string]string) bool,
) bool {
	serverNamespace := AccessServerRefNamespace(resourceNamespace, ref)
	if server, ok := cache[AccessServerCacheKey(serverNamespace, string(ref.Name))]; ok {
		return canAdministerServer(server)
	}
	return canAdministerServerLabels(DefaultAccessNamespace(resourceNamespace), nil)
}
