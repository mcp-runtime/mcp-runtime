package runtimeapi

import (
	"context"
	"errors"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	mcpaccess "mcp-runtime/pkg/access"
	"mcp-runtime/pkg/sessionstate"
)

type fakeSessionClient struct {
	row sessionstate.Session
	err error
}

func (c *fakeSessionClient) PutSession(_ context.Context, row sessionstate.Session) (sessionstate.Session, error) {
	if c.err != nil {
		return row, c.err
	}
	row.UID = "session-uid"
	row.CreatedAt = time.Now()
	c.row = row
	return row, nil
}
func (c *fakeSessionClient) GetSession(_ context.Context, namespace, name string) (sessionstate.Session, bool, error) {
	return c.row, c.row.Namespace == namespace && string(c.row.Binding.Name) == name, c.err
}
func (c *fakeSessionClient) ListSessions(context.Context, string) ([]sessionstate.Session, error) {
	return []sessionstate.Session{c.row}, c.err
}
func (c *fakeSessionClient) SetSessionRevoked(_ context.Context, namespace, name string, revoked bool, cause string) error {
	c.row.Binding.Revoked = revoked
	return c.err
}
func (c *fakeSessionClient) DeleteSession(context.Context, string, string) error {
	c.row = sessionstate.Session{}
	return c.err
}

func TestPlatformSessionBackendPersistsWithoutKubernetesState(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mcpv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	server := &mcpv1alpha1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "server", Namespace: "team", UID: types.UID("server-uid")}, Spec: mcpv1alpha1.MCPServerSpec{TeamID: "authority-team"}}
	dynamic := dynamicfake.NewSimpleDynamicClient(scheme, server)
	manager := mcpaccess.NewManager(dynamic, nil)
	client := &fakeSessionClient{}
	backend := &platformSessionBackend{client: client, access: manager}
	manager.WithSessionBackend(backend)
	session := &mcpaccess.MCPAgentSession{ObjectMeta: metav1.ObjectMeta{Name: "session", Namespace: "team", Annotations: map[string]string{adapterGrantNameAnnotation: "grant", adapterGrantNamespaceAnnotation: "foreign"}}, Spec: mcpaccess.MCPAgentSessionSpec{ServerRef: mcpaccess.ServerReference{Name: "server", Namespace: "team"}, Subject: mcpaccess.SubjectRef{HumanID: "human", AgentID: "agent", TeamID: "actor-team"}, ConsentedTrust: "medium", PolicyVersion: "v1", ExpiresAt: &metav1.Time{Time: time.Now().Add(time.Hour)}}}
	applied, err := manager.ApplySession(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	if client.row.ServerUID != "server-uid" || client.row.AuthorityTeamID != "authority-team" || client.row.GrantNamespace != "foreign" || applied.Spec.Subject.TeamID != "actor-team" {
		t.Fatalf("lost session attribution: %+v", client.row)
	}
	for _, action := range dynamic.Actions() {
		if action.GetResource().Resource == mcpaccess.AccessSessionResource {
			t.Fatalf("used Kubernetes session state: %+v", action)
		}
	}
	if err := manager.RevokeSession(context.Background(), "session", "team"); err != nil {
		t.Fatal(err)
	}
	got, err := manager.GetSession(context.Background(), "session", "team")
	if err != nil || !got.Spec.Revoked {
		t.Fatalf("revoked=%+v error=%v", got, err)
	}
	if _, err := manager.GetSession(context.Background(), "missing", "team"); !apierrors.IsNotFound(err) {
		t.Fatalf("missing=%v", err)
	}
	client.err = errors.New("platform unavailable")
	if _, err := manager.GetSession(context.Background(), "session", "team"); err == nil {
		t.Fatal("backend outage accepted")
	}
	// No backend operation may silently query CRDs during an outage.
	for _, action := range dynamic.Actions() {
		if action.GetResource().Resource == mcpaccess.AccessSessionResource {
			t.Fatal("fell back to Kubernetes")
		}
	}
}

func TestSessionStateRoundTripKeepsRevocationAndGrantLink(t *testing.T) {
	expires := metav1.NewTime(time.Now().UTC().Truncate(time.Second).Add(time.Hour))
	session := &mcpaccess.MCPAgentSession{ObjectMeta: metav1.ObjectMeta{Name: "session", Namespace: "team", Annotations: map[string]string{adapterGrantNameAnnotation: "grant", adapterGrantNamespaceAnnotation: "foreign"}}, Spec: mcpaccess.MCPAgentSessionSpec{ServerRef: mcpaccess.ServerReference{Name: "server"}, Subject: mcpaccess.SubjectRef{HumanID: "human"}, ConsentedTrust: "low", ExpiresAt: &expires, Revoked: true, PolicyVersion: "v1"}}
	row := sessionToState(session, "server-uid", "authority")
	now := time.Now().UTC()
	row.RevokedAt = &now
	row.RevocationCause = "server_retired"
	got := sessionFromState(row)
	summary := mcpaccess.ToSessionSummary(*got)
	if summary.ServerUID != "server-uid" || summary.GrantNamespace != "foreign" || summary.RevocationCause != "server_retired" || !got.Spec.ExpiresAt.Equal(&expires) {
		t.Fatalf("round trip=%+v", summary)
	}
}

func TestPlatformSessionBackendRejectsRetiringTarget(t *testing.T) {
	for _, tc := range []struct {
		name     string
		deleting bool
	}{{"retiring", false}, {"deleting", true}} {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			_ = mcpv1alpha1.AddToScheme(scheme)
			server := &mcpv1alpha1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "server", Namespace: "team", UID: types.UID("server-uid")}}
			if tc.deleting {
				now := metav1.Now()
				server.DeletionTimestamp = &now
			} else {
				server.Annotations = map[string]string{mcpaccess.ServerRetiringAnnotation: "true"}
			}
			manager := mcpaccess.NewManager(dynamicfake.NewSimpleDynamicClient(scheme, server), nil)
			client := &fakeSessionClient{}
			manager.WithSessionBackend(&platformSessionBackend{client: client, access: manager})
			session := &mcpaccess.MCPAgentSession{ObjectMeta: metav1.ObjectMeta{Name: "new", Namespace: "team"}, Spec: mcpaccess.MCPAgentSessionSpec{ServerRef: mcpaccess.ServerReference{Name: "server", Namespace: "team"}}}
			if _, err := manager.ApplySession(context.Background(), session); !apierrors.IsConflict(err) {
				t.Fatalf("retiring target issuance: %v", err)
			}
			if client.row.ID != "" {
				t.Fatal("wrote a session for a retiring target")
			}
		})
	}
}
