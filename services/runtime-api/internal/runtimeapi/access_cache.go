package runtimeapi

import (
	"context"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	runtimeaccess "mcp-runtime-api/internal/runtimeapi/access"
	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	sentinelaccess "mcp-runtime/pkg/access"
)

type accessServerCache map[string]mcpv1alpha1.MCPServer

func (s *AccessService) grantVisibleToPrincipal(ctx context.Context, grant sentinelaccess.MCPAccessGrant) bool {
	p, ok := principalFromContext(ctx)
	return !ok || principalCanReadGrantSubject(p, grant)
}

func (s *AccessService) sessionVisibleToPrincipal(ctx context.Context, session sentinelaccess.MCPAgentSession) bool {
	p, ok := principalFromContext(ctx)
	return !ok || principalCanReadSessionSubject(p, session)
}

func principalCanReadGrantSubject(p principal, grant sentinelaccess.MCPAccessGrant) bool {
	if p.Role == roleAdmin {
		return true
	}
	if team, ok := p.TeamForNamespace(grant.Namespace); ok && team.Role == teamRoleOwner {
		return true
	}
	subject := grant.Spec.Subject
	if subject.HumanID != "" && string(subject.HumanID) != p.UserID() {
		return false
	}
	if subject.TeamID != "" {
		for _, team := range p.Teams {
			if team.ID == string(subject.TeamID) {
				return true
			}
		}
		return false
	}
	return subject.HumanID != "" && string(subject.HumanID) == p.UserID()
}

func principalCanReadSessionSubject(p principal, session sentinelaccess.MCPAgentSession) bool {
	if p.Role == roleAdmin {
		return true
	}
	if team, ok := p.TeamForNamespace(session.Namespace); ok && team.Role == teamRoleOwner {
		return true
	}
	return session.Spec.Subject.HumanID != "" && string(session.Spec.Subject.HumanID) == p.UserID()
}

func (s *AccessService) accessServerCacheForGrantRefs(ctx context.Context, namespace string, grants []sentinelaccess.MCPAccessGrant) (accessServerCache, error) {
	refs := make([]sentinelaccess.ServerReference, 0, len(grants))
	for _, grant := range grants {
		refs = append(refs, grant.Spec.ServerRef)
	}
	return s.accessServerCacheForRefs(ctx, namespace, refs)
}

func (s *AccessService) accessServerCacheForSessionRefs(ctx context.Context, namespace string, sessions []sentinelaccess.MCPAgentSession) (accessServerCache, error) {
	refs := make([]sentinelaccess.ServerReference, 0, len(sessions))
	for _, session := range sessions {
		refs = append(refs, session.Spec.ServerRef)
	}
	return s.accessServerCacheForRefs(ctx, namespace, refs)
}

func (s *AccessService) accessServerCacheForRefs(ctx context.Context, namespace string, refs []sentinelaccess.ServerReference) (accessServerCache, error) {
	namespaces := map[string]struct{}{}
	for _, ref := range refs {
		if strings.TrimSpace(string(ref.Name)) == "" {
			continue
		}
		namespaces[runtimeaccess.AccessServerRefNamespace(namespace, ref)] = struct{}{}
	}

	cache := accessServerCache{}
	for ns := range namespaces {
		servers, err := s.accessMgr.ListMCPServers(ctx, ns)
		if err != nil {
			return nil, err
		}
		for _, server := range servers.Items {
			cache[runtimeaccess.AccessServerCacheKey(server.Namespace, server.Name)] = server
		}
	}
	return cache, nil
}

func (s *AccessService) grantDisabledForApply(ctx context.Context, req accessGrantRequest) (bool, error) {
	if req.Disabled != nil {
		return *req.Disabled, nil
	}
	existing, err := s.accessMgr.GetGrant(ctx, req.Name, runtimeaccess.DefaultAccessNamespace(req.Namespace))
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return existing.Spec.Disabled, nil
}
