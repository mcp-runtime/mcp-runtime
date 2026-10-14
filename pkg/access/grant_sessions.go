package access

import (
	"context"
	"fmt"
)

func (m *Manager) ListGrantSessions(ctx context.Context, namespace, name string) (*MCPAgentSessionList, error) {
	if m.sessions != nil {
		if backend, ok := m.sessions.(interface {
			ListGrantSessions(context.Context, string, string) (*MCPAgentSessionList, error)
		}); ok {
			return backend.ListGrantSessions(ctx, namespace, name)
		}
	}
	all, err := m.ListSessions(ctx, "")
	if err != nil {
		return nil, err
	}
	result := &MCPAgentSessionList{Items: []MCPAgentSession{}}
	for _, session := range all.Items {
		if session.Annotations["mcpruntime.org/access-grant-name"] == name && session.Annotations["mcpruntime.org/access-grant-namespace"] == namespace {
			result.Items = append(result.Items, session)
		}
	}
	return result, nil
}

// RevokeGrantSessions revokes every explicitly linked session, even when a
// migrated binding lives outside the grant namespace. Platform backends perform
// this as one transaction; direct Kubernetes mode remains a best-effort path.
func (m *Manager) RevokeGrantSessions(ctx context.Context, namespace, name string) (int64, error) {
	if m.sessions != nil {
		backend, ok := m.sessions.(interface {
			RevokeGrantSessions(context.Context, string, string) (int64, error)
		})
		if !ok {
			return 0, fmt.Errorf("authoritative session backend does not support grant revocation")
		}
		return backend.RevokeGrantSessions(ctx, namespace, name)
	}
	sessions, err := m.ListSessions(ctx, "")
	if err != nil {
		return 0, err
	}
	var count int64
	for _, session := range sessions.Items {
		if session.Annotations["mcpruntime.org/access-grant-name"] != name || session.Annotations["mcpruntime.org/access-grant-namespace"] != namespace || session.Spec.Revoked {
			continue
		}
		if err := m.RevokeSession(ctx, session.Name, session.Namespace); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}
