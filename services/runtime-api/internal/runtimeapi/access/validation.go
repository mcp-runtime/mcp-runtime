package access

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation/field"
	mcpaccess "mcp-runtime/pkg/access"
	"mcp-runtime/pkg/access/specvalidation"
)

func ValidateTeamIDValue(name, value string) error {
	value = strings.TrimSpace(value)
	if issue := specvalidation.TeamIDField(field.NewPath(name), value); issue != nil {
		if issue.Type == field.ErrorTypeTooLong {
			return fmt.Errorf("%s must be at most 128 characters", name)
		}
		return fmt.Errorf("%s must not contain whitespace", name)
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
