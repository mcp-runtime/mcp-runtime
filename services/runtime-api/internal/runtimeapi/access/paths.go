package access

import (
	"fmt"
	"strings"

	mcpaccess "mcp-runtime/pkg/access"
	serviceutil "mcp-runtime/pkg/serviceutil"
)

func ExtractNamespacedPath(path, prefix string, expectedParts int) (string, string, error) {
	path = serviceutil.NormalizePublicAPIPath(path)
	prefix = serviceutil.NormalizePublicAPIPath(prefix)
	path = strings.TrimPrefix(path, prefix)
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != expectedParts {
		return "", "", fmt.Errorf("invalid path")
	}
	namespace := strings.TrimSpace(parts[0])
	name := strings.TrimSpace(parts[1])
	if namespace == "" || name == "" {
		return "", "", fmt.Errorf("invalid path")
	}
	if err := mcpaccess.ValidateResourceName("namespace", namespace); err != nil {
		return "", "", err
	}
	if err := mcpaccess.ValidateResourceName("name", name); err != nil {
		return "", "", err
	}
	return namespace, name, nil
}

func AccessServerRefNamespace(resourceNamespace string, ref mcpaccess.ServerReference) string {
	if ns := strings.TrimSpace(string(ref.Namespace)); ns != "" {
		return ns
	}
	return DefaultAccessNamespace(resourceNamespace)
}

func AccessServerCacheKey(namespace, name string) string {
	return strings.TrimSpace(namespace) + "\x00" + strings.TrimSpace(name)
}
