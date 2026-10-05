package runtimeapi

import (
	"context"
	"errors"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	runtimeaccess "mcp-runtime-api/internal/runtimeapi/access"
	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	mcpaccess "mcp-runtime/pkg/access"
)

func principalCanAdministerServerLabels(p principal, namespace string, serverLabels map[string]string) bool {
	if p.Role == roleAdmin {
		return true
	}
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		return false
	}
	if team, ok := p.TeamForNamespace(namespace); ok && strings.TrimSpace(team.Role) == teamRoleOwner {
		return true
	}
	userID := strings.TrimSpace(p.UserID())
	if userID == "" {
		return false
	}
	owner := strings.TrimSpace(serverLabels[platformUserIDLabel])
	if owner == userID {
		return true
	}
	if owner != "" {
		return false
	}
	if _, ok := p.TeamForNamespace(namespace); ok {
		return false
	}
	if namespace == sharedCatalogNamespace || isModeCatalogNamespace(namespace) {
		return false
	}
	return strings.TrimSpace(p.Namespace) == namespace
}

func principalCanAdministerMCPServer(p principal, server mcpv1alpha1.MCPServer) bool {
	return principalCanAdministerServerLabels(p, server.Namespace, server.Labels)
}

func (s *AccessService) principalCanAdministerAccessServer(ctx context.Context, server mcpv1alpha1.MCPServer) bool {
	p, ok := principalFromContext(ctx)
	if !ok {
		return true
	}
	return principalCanAdministerMCPServer(p, server)
}

func (s *AccessService) canAdministerAccessServerRef(ctx context.Context, namespace string, ref mcpaccess.ServerReference) (bool, error) {
	ref.Namespace = mcpaccess.Namespace(strings.TrimSpace(string(ref.Namespace)))
	if ref.Namespace == "" {
		ref.Namespace = mcpaccess.Namespace(runtimeaccess.DefaultAccessNamespace(namespace))
	}
	targetServer, err := s.accessMgr.GetMCPServerRef(ctx, ref)
	if err != nil {
		if mcpaccess.IsMCPServerNotFoundForRef(err) || apierrors.IsNotFound(err) {
			if p, ok := principalFromContext(ctx); ok {
				return principalCanAdministerServerLabels(p, namespace, nil), nil
			}
			return false, nil
		}
		return false, err
	}
	return s.principalCanAdministerAccessServer(ctx, *targetServer), nil
}

func (s *AccessService) canAdministerNamedServer(ctx context.Context, namespace, name string) (bool, error) {
	p, ok := principalFromContext(ctx)
	if !ok || p.Role == roleAdmin {
		return true, nil
	}
	control := s.controlPlane()
	if control == nil {
		return false, errors.New("kubernetes not available")
	}
	current, err := control.GetServer(ctx, namespace, name)
	if err != nil {
		return false, err
	}
	return principalCanAdministerMCPServer(p, *current), nil
}

func sensitiveServerReadStatus(err error) (int, string) {
	if err == nil {
		return 403, "forbidden server"
	}
	if apierrors.IsNotFound(err) {
		return 404, "server not found"
	}
	return 500, "failed to inspect server"
}
