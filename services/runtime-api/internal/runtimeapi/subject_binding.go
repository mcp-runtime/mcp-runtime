package runtimeapi

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	runtimeaccess "mcp-runtime-api/internal/runtimeapi/access"
	mcpaccess "mcp-runtime/pkg/access"
	"mcp-runtime/pkg/k8sclient"
)

func writeK8sApplyError(w http.ResponseWriter, kind, namespace, name string, err error) {
	code, msg := k8sclient.HTTPStatusFromK8sError(err)
	log.Printf("apply %s %s/%s failed (status=%d): %v", kind, namespace, name, code, err)
	writeAPIError(w, code, fmt.Sprintf("failed to apply %s: %s", kind, msg))
}

func (s *AccessService) bindAccessSubjectTeamID(ctx context.Context, namespace, serverTeamID string, subject *mcpaccess.SubjectRef) error {
	subject.TeamID = mcpaccess.TeamID(strings.TrimSpace(string(subject.TeamID)))
	serverTeamID = strings.TrimSpace(serverTeamID)
	namespaceTeamID := strings.TrimSpace(s.teamIDForPrincipalNamespace(ctx, namespace))
	if subject.TeamID == "" {
		subject.TeamID = mcpaccess.TeamID(firstNonEmpty(serverTeamID, namespaceTeamID))
	}
	if err := runtimeaccess.ValidateTeamIDValue("subject.teamID", string(subject.TeamID)); err != nil {
		return err
	}
	p, ok := principalFromContext(ctx)
	if ok && p.Role != roleAdmin && namespaceTeamID == "" && subject.TeamID != "" {
		return errors.New("subject.teamID is only allowed in a team namespace")
	}
	return nil
}

func (s *AccessService) teamIDForPrincipalNamespace(ctx context.Context, namespace string) string {
	namespace = strings.TrimSpace(namespace)
	if p, ok := principalFromContext(ctx); ok {
		if team, found := p.TeamForNamespace(namespace); found {
			return strings.TrimSpace(team.ID)
		}
	}
	if s != nil && s.identityConfigured() && namespace != "" {
		if record, found, err := s.identity.GetNamespace(ctx, namespace); err == nil && found {
			return strings.TrimSpace(fmt.Sprint(record["team_id"]))
		}
	}
	return ""
}

func (s *AccessService) scopedNamespaceForPrincipal(ctx context.Context, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	p, ok := principalFromContext(ctx)
	if !ok || p.Role == roleAdmin {
		return requested, nil
	}
	if requested == "" {
		if sharedCatalogWritableForUsers() {
			return defaultCatalogNamespaceForMode(), nil
		}
		if preferred := strings.TrimSpace(p.Namespace); preferred != "" {
			return preferred, nil
		}
		return "", errPrincipalIdentityRequired
	}
	if !principalCanReadNamespace(p, requested) {
		return "", errors.New("forbidden namespace")
	}
	return requested, nil
}

func (s *AccessService) scopedAccessWriteNamespaceForPrincipal(ctx context.Context, requested string) (string, error) {
	namespace, err := s.scopedNamespaceForPrincipal(ctx, requested)
	if err != nil {
		return "", err
	}
	p, ok := principalFromContext(ctx)
	if ok && p.Role != roleAdmin && namespace == sharedCatalogNamespace {
		return "", errors.New("shared catalog namespace is read-only for access resources")
	}
	if ok && p.Role != roleAdmin {
		team, member := p.TeamForNamespace(namespace)
		if !member || strings.TrimSpace(team.Role) != teamRoleOwner {
			return "", errors.New("team owner role required for access changes")
		}
	}
	return namespace, nil
}
