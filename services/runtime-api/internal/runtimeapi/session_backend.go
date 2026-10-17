package runtimeapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	mcpaccess "mcp-runtime/pkg/access"
	"mcp-runtime/pkg/policy"
	"mcp-runtime/pkg/sessionstate"
)

const sessionServerUIDAnnotation = mcpaccess.ServerUIDAnnotation

type platformSessionClient interface {
	PutSession(context.Context, sessionstate.Session) (sessionstate.Session, error)
	GetSession(context.Context, string, string) (sessionstate.Session, bool, error)
	ListSessions(context.Context, string) ([]sessionstate.Session, error)
	SetSessionRevoked(context.Context, string, string, bool, string) error
	DeleteSession(context.Context, string, string) error
}

// platformSessionBackend adapts the existing public session representation to
// platform-owned persistence. It never creates a Kubernetes session resource.
type platformSessionBackend struct {
	client platformSessionClient
	access *mcpaccess.Manager
}

func (b *platformSessionBackend) configured() error {
	if b.client == nil {
		return fmt.Errorf("platform session store is unavailable")
	}
	return nil
}

func (b *platformSessionBackend) ListSessions(ctx context.Context, namespace string) (*mcpaccess.MCPAgentSessionList, error) {
	if err := b.configured(); err != nil {
		return nil, err
	}
	rows, err := b.client.ListSessions(ctx, namespace)
	if err != nil {
		return nil, err
	}
	result := &mcpaccess.MCPAgentSessionList{Items: make([]mcpaccess.MCPAgentSession, 0, len(rows))}
	for _, row := range rows {
		result.Items = append(result.Items, *sessionFromState(row))
	}
	return result, nil
}

func (b *platformSessionBackend) GetSession(ctx context.Context, name, namespace string) (*mcpaccess.MCPAgentSession, error) {
	if err := b.configured(); err != nil {
		return nil, err
	}
	row, found, err := b.client.GetSession(ctx, namespace, name)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, apierrors.NewNotFound(schema.GroupResource{Group: mcpaccess.APIGroup, Resource: mcpaccess.AccessSessionResource}, name)
	}
	return sessionFromState(row), nil
}

func (b *platformSessionBackend) ApplySession(ctx context.Context, session *mcpaccess.MCPAgentSession) (*mcpaccess.MCPAgentSession, error) {
	if err := b.configured(); err != nil {
		return nil, err
	}
	if session == nil {
		return nil, fmt.Errorf("session is required")
	}
	ref := session.Spec.ServerRef
	if strings.TrimSpace(string(ref.Namespace)) == "" {
		ref.Namespace = mcpaccess.Namespace(session.Namespace)
	}
	server, err := b.access.GetActiveMCPServerRef(ctx, ref)
	if err != nil {
		return nil, err
	}
	if server.UID == "" {
		return nil, fmt.Errorf("server UID is required for session persistence")
	}
	if bound := session.Annotations[mcpaccess.ServerUIDAnnotation]; bound != "" && bound != string(server.UID) {
		return nil, sessionstate.ErrConflict
	}
	row := sessionToState(session, string(server.UID), server.Spec.TeamID)
	stored, err := b.client.PutSession(ctx, row)
	if err != nil {
		return nil, err
	}
	return sessionFromState(stored), nil
}

func (b *platformSessionBackend) RevokeSession(ctx context.Context, name, namespace string) error {
	if err := b.configured(); err != nil {
		return err
	}
	return sessionBackendError(b.client.SetSessionRevoked(ctx, namespace, name, true, "access_revoked"), name)
}

func (b *platformSessionBackend) RevokeGrantSessions(ctx context.Context, namespace, name string) (int64, error) {
	if err := b.configured(); err != nil {
		return 0, err
	}
	client, ok := b.client.(interface {
		RevokeSessions(context.Context, sessionstate.Filter, string) (int64, error)
	})
	if !ok {
		return 0, fmt.Errorf("platform session store does not support bulk revocation")
	}
	return client.RevokeSessions(ctx, sessionstate.Filter{GrantNamespace: namespace, GrantName: name}, "grant_revoked")
}

func (b *platformSessionBackend) ListGrantSessions(ctx context.Context, namespace, name string) (*mcpaccess.MCPAgentSessionList, error) {
	if err := b.configured(); err != nil {
		return nil, err
	}
	client, ok := b.client.(interface {
		ListSessionsFiltered(context.Context, sessionstate.Filter) ([]sessionstate.Session, error)
	})
	if !ok {
		return nil, fmt.Errorf("platform session store does not support filtered grant listing")
	}
	rows, err := client.ListSessionsFiltered(ctx, sessionstate.Filter{GrantNamespace: namespace, GrantName: name})
	if err != nil {
		return nil, err
	}
	result := &mcpaccess.MCPAgentSessionList{Items: []mcpaccess.MCPAgentSession{}}
	for _, row := range rows {
		result.Items = append(result.Items, *sessionFromState(row))
	}
	return result, nil
}

func (b *platformSessionBackend) UnrevokeSession(ctx context.Context, name, namespace string) error {
	if err := b.configured(); err != nil {
		return err
	}
	return sessionBackendError(b.client.SetSessionRevoked(ctx, namespace, name, false, "access_unrevoked"), name)
}

func (b *platformSessionBackend) DeleteSession(ctx context.Context, name, namespace string) error {
	if err := b.configured(); err != nil {
		return err
	}
	return sessionBackendError(b.client.DeleteSession(ctx, namespace, name), name)
}

func (b *platformSessionBackend) CompleteSessionEnrollment(ctx context.Context, name, namespace, uid string) error {
	client, ok := b.client.(interface {
		CompleteSessionEnrollment(context.Context, string, string, string) error
	})
	if !ok {
		return fmt.Errorf("conditional enrollment completion is unavailable")
	}
	return sessionBackendError(client.CompleteSessionEnrollment(ctx, namespace, name, uid), name)
}

func (b *platformSessionBackend) DeletePendingSession(ctx context.Context, name, namespace, uid, humanID string) error {
	client, ok := b.client.(interface {
		DeletePendingSession(context.Context, string, string, string, string) error
	})
	if !ok {
		return fmt.Errorf("conditional pending-session cleanup is unavailable")
	}
	return sessionBackendError(client.DeletePendingSession(ctx, namespace, name, uid, humanID), name)
}

func (b *platformSessionBackend) DeleteSessionVersioned(ctx context.Context, name, namespace, uid string) error {
	if err := b.configured(); err != nil {
		return err
	}
	client, ok := b.client.(interface {
		DeleteSessionIfUID(context.Context, string, string, string) error
	})
	if !ok {
		return fmt.Errorf("versioned platform session deletion is unavailable")
	}
	return sessionBackendError(client.DeleteSessionIfUID(ctx, namespace, name, uid), name)
}

func sessionBackendError(err error, name string) error {
	if errors.Is(err, sessionstate.ErrNotFound) {
		return apierrors.NewNotFound(schema.GroupResource{Group: mcpaccess.APIGroup, Resource: mcpaccess.AccessSessionResource}, name)
	}
	return err
}

func sessionToState(session *mcpaccess.MCPAgentSession, serverUID, authorityTeam string) sessionstate.Session {
	serverNamespace := string(session.Spec.ServerRef.Namespace)
	if serverNamespace == "" {
		serverNamespace = session.Namespace
	}
	row := sessionstate.Session{
		ID: session.Namespace + "/" + session.Name, UID: string(session.UID), Namespace: session.Namespace,
		ServerNamespace: serverNamespace, ServerName: string(session.Spec.ServerRef.Name), ServerUID: serverUID,
		AuthorityTeamID: authorityTeam, Labels: session.Labels, Annotations: session.Annotations,
		GrantName: session.Annotations[adapterGrantNameAnnotation], GrantNamespace: session.Annotations[adapterGrantNamespaceAnnotation],
		CreatedAt: session.CreationTimestamp.Time,
		Binding: policy.Binding{
			Name: policy.SessionID(session.Name), Namespace: policy.Namespace(session.Namespace),
			HumanID: policy.HumanID(session.Spec.Subject.HumanID), AgentID: policy.AgentID(session.Spec.Subject.AgentID), TeamID: policy.TeamID(session.Spec.Subject.TeamID),
			ConsentedTrust: string(session.Spec.ConsentedTrust), Revoked: session.Spec.Revoked, PolicyVersion: session.Spec.PolicyVersion,
		},
	}
	if session.Spec.ExpiresAt != nil {
		row.Binding.ExpiresAt = session.Spec.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if session.Spec.UpstreamTokenSecretRef != nil {
		row.Binding.UpstreamTokenRef = session.Spec.UpstreamTokenSecretRef.Name + "/" + session.Spec.UpstreamTokenSecretRef.Key
	}
	return row
}

func sessionFromState(row sessionstate.Session) *mcpaccess.MCPAgentSession {
	annotations := make(map[string]string, len(row.Annotations)+3)
	for key, value := range row.Annotations {
		annotations[key] = value
	}
	annotations[sessionServerUIDAnnotation] = row.ServerUID
	annotations["mcpruntime.org/session-revocation-cause"] = row.RevocationCause
	if row.RevokedAt != nil {
		annotations["mcpruntime.org/session-revoked-at"] = row.RevokedAt.UTC().Format(time.RFC3339)
	}
	if row.GrantName != "" {
		annotations[adapterGrantNameAnnotation] = row.GrantName
		annotations[adapterGrantNamespaceAnnotation] = row.GrantNamespace
	}
	session := &mcpaccess.MCPAgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: string(row.Binding.Name), Namespace: row.Namespace, UID: types.UID(row.UID), CreationTimestamp: metav1.NewTime(row.CreatedAt), Labels: row.Labels, Annotations: annotations},
		Spec: mcpaccess.MCPAgentSessionSpec{
			ServerRef:      mcpaccess.ServerReference{Name: mcpaccess.ServerName(row.ServerName), Namespace: mcpaccess.Namespace(row.ServerNamespace)},
			Subject:        mcpaccess.SubjectRef{HumanID: mcpaccess.HumanID(row.Binding.HumanID), AgentID: mcpaccess.AgentID(row.Binding.AgentID), TeamID: mcpaccess.TeamID(row.Binding.TeamID)},
			ConsentedTrust: mcpaccess.TrustLevel(row.Binding.ConsentedTrust), Revoked: row.Binding.Revoked, PolicyVersion: row.Binding.PolicyVersion,
		},
	}
	if expires, err := time.Parse(time.RFC3339, row.Binding.ExpiresAt); err == nil {
		session.Spec.ExpiresAt = &metav1.Time{Time: expires}
	}
	if name, key, ok := strings.Cut(row.Binding.UpstreamTokenRef, "/"); ok {
		session.Spec.UpstreamTokenSecretRef = &mcpaccess.SecretKeyRef{Name: name, Key: key}
	}
	return session
}
