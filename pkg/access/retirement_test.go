package access

import (
	"context"
	"errors"
	"fmt"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
)

func TestRetireServerAccessCrossNamespaceAndRetry(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := mcpv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	ref := ServerReference{Name: "demo", Namespace: "team-a"}
	grant := &MCPAccessGrant{ObjectMeta: metav1.ObjectMeta{Name: "grant", Namespace: "team-b"}, Spec: MCPAccessGrantSpec{ServerRef: ref}}
	session := &MCPAgentSession{ObjectMeta: metav1.ObjectMeta{Name: "session", Namespace: "team-c"}, Spec: MCPAgentSessionSpec{ServerRef: ref}}
	otherGrant := *grant
	otherGrant.Name = "other"
	otherGrant.Spec.ServerRef.Namespace = "team-other"
	otherSession := *session
	otherSession.Name = "other"
	otherSession.Spec.ServerRef.Name = "other"
	g1, _ := toUnstructured(grant, "MCPAccessGrant")
	g2, _ := toUnstructured(&otherGrant, "MCPAccessGrant")
	s1, _ := toUnstructured(session, "MCPAgentSession")
	s2, _ := toUnstructured(&otherSession, "MCPAgentSession")
	client := dynamicfake.NewSimpleDynamicClient(scheme, g1, s1, g2, s2)
	manager := NewManager(client, nil)
	failed := false
	client.PrependReactor("delete", AccessSessionResource, func(a clienttesting.Action) (bool, runtime.Object, error) {
		if !failed {
			failed = true
			return true, nil, errors.New("injected delete failure")
		}
		return false, nil, nil
	})
	if err := manager.RetireServerAccess(ctx, "team-a", "demo"); err == nil {
		t.Fatal("expected cleanup failure")
	}
	g, err := manager.GetGrant(ctx, grant.Name, grant.Namespace)
	if err != nil || !g.Spec.Disabled {
		t.Fatalf("grant not invalidated: %+v %v", g, err)
	}
	s, err := manager.GetSession(ctx, session.Name, session.Namespace)
	if err != nil || !s.Spec.Revoked {
		t.Fatalf("session not revoked: %+v %v", s, err)
	}
	if err := manager.RetireServerAccess(ctx, "team-a", "demo"); err != nil {
		t.Fatal(err)
	}
	if err := manager.RetireServerAccess(ctx, "team-a", "demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.GetGrant(ctx, grant.Name, grant.Namespace); !apierrors.IsNotFound(err) {
		t.Fatalf("grant still exists: %v", err)
	}
	if _, err := manager.GetSession(ctx, session.Name, session.Namespace); !apierrors.IsNotFound(err) {
		t.Fatalf("session still exists: %v", err)
	}
	if _, err := manager.GetGrant(ctx, otherGrant.Name, otherGrant.Namespace); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.GetSession(ctx, otherSession.Name, otherSession.Namespace); err != nil {
		t.Fatal(err)
	}
}

func TestRetireServerAccessDoesNotDeleteAfterInvalidationFailure(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mcpv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	grant := &MCPAccessGrant{ObjectMeta: metav1.ObjectMeta{Name: "grant", Namespace: "team-a"}, Spec: MCPAccessGrantSpec{ServerRef: ServerReference{Name: "demo", Namespace: "team-a"}}}
	obj, _ := toUnstructured(grant, "MCPAccessGrant")
	client := dynamicfake.NewSimpleDynamicClient(scheme, obj)
	client.PrependReactor("patch", AccessGrantResource, func(a clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: AccessGrantResource}, "grant", errors.New("blocked"))
	})
	manager := NewManager(client, nil)
	if err := manager.RetireServerAccess(context.Background(), "team-a", "demo"); err == nil {
		t.Fatal("expected error")
	}
	for _, action := range client.Actions() {
		if action.GetVerb() == "delete" {
			t.Fatal("delete attempted despite invalidation failure")
		}
	}
}

func TestAuthorizationBoundToServerDeployment(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	server := &mcpv1alpha1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "team-a", UID: "new"}}
	manager := NewManager(dynamicfake.NewSimpleDynamicClient(scheme, server), nil)
	grant := &MCPAccessGrant{ObjectMeta: metav1.ObjectMeta{Name: "grant", Namespace: "team-a", Annotations: map[string]string{ServerUIDAnnotation: "old"}}, Spec: MCPAccessGrantSpec{ServerRef: ServerReference{Name: "demo", Namespace: "team-a"}}}
	if _, err := manager.ApplyGrant(context.Background(), grant); err == nil {
		t.Fatal("stale request rebound to replacement server")
	}
	grant.Annotations = nil
	bound, err := manager.ApplyGrant(context.Background(), grant)
	if err != nil {
		t.Fatal(err)
	}
	if bound.Annotations[ServerUIDAnnotation] != "new" {
		t.Fatal("new grant not bound to deployment")
	}
}

func TestRetiringServerLookupAllowsManagementButBlocksIssuance(t *testing.T) {
	for _, deleting := range []bool{false, true} {
		t.Run(fmt.Sprint("deleting=", deleting), func(t *testing.T) {
			scheme := runtime.NewScheme()
			_ = mcpv1alpha1.AddToScheme(scheme)
			server := &mcpv1alpha1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "team-a", UID: "uid", Labels: map[string]string{"owner": "owner"}}}
			if deleting {
				now := metav1.Now()
				server.DeletionTimestamp = &now
			} else {
				server.Annotations = map[string]string{ServerRetiringAnnotation: "true"}
			}
			manager := NewManager(dynamicfake.NewSimpleDynamicClient(scheme, server), nil)
			ref := ServerReference{Name: "demo", Namespace: "team-a"}
			got, err := manager.GetMCPServerRef(context.Background(), ref)
			if err != nil || got.Labels["owner"] != "owner" {
				t.Fatalf("management lookup lost authority: %+v %v", got, err)
			}
			if err := manager.AssertMCPServerRef(context.Background(), ref); !apierrors.IsConflict(err) {
				t.Fatalf("issuance accepted retiring target: %v", err)
			}
			grant := &MCPAccessGrant{ObjectMeta: metav1.ObjectMeta{Name: "new", Namespace: "team-a"}, Spec: MCPAccessGrantSpec{ServerRef: ref}}
			if _, err := manager.ApplyGrant(context.Background(), grant); !apierrors.IsConflict(err) {
				t.Fatalf("grant issuance: %v", err)
			}
			session := &MCPAgentSession{ObjectMeta: metav1.ObjectMeta{Name: "new", Namespace: "team-a"}, Spec: MCPAgentSessionSpec{ServerRef: ref}}
			if _, err := manager.ApplySession(context.Background(), session); !apierrors.IsConflict(err) {
				t.Fatalf("session issuance: %v", err)
			}
		})
	}
}
