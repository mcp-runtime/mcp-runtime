package access

import (
	"fmt"
	"strings"

	mcpaccess "mcp-runtime/pkg/access"
)

func ValidateTeamIDValue(name, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if strings.ContainsAny(value, " \t\r\n") {
		return fmt.Errorf("%s must not contain whitespace", name)
	}
	if len(value) > 128 {
		return fmt.Errorf("%s must be at most 128 characters", name)
	}
	return nil
}

func BindAccessServerRefNamespace(resourceNamespace string, serverRef *mcpaccess.ServerReference) error {
	resourceNamespace = DefaultAccessNamespace(resourceNamespace)
	serverRef.Namespace = mcpaccess.Namespace(strings.TrimSpace(string(serverRef.Namespace)))
	if serverRef.Namespace == "" {
		serverRef.Namespace = mcpaccess.Namespace(resourceNamespace)
		return nil
	}
	if string(serverRef.Namespace) != resourceNamespace {
		return fmt.Errorf("serverRef.namespace %q must match access resource namespace %q", serverRef.Namespace, resourceNamespace)
	}
	return nil
}
