package operator

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	mcpaccess "mcp-runtime/pkg/access"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestRepublishedServerRejectsDelayedOldAuthorization(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	server := &mcpv1alpha1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "team-a", UID: "new-deployment"}}
	grant := &mcpv1alpha1.MCPAccessGrant{ObjectMeta: metav1.ObjectMeta{Name: "delayed-grant", Namespace: "team-a", Annotations: map[string]string{mcpaccess.ServerUIDAnnotation: "old-deployment"}}, Spec: mcpv1alpha1.MCPAccessGrantSpec{ServerRef: mcpv1alpha1.ServerReference{Name: "demo", Namespace: "team-a"}}}
	session := &mcpv1alpha1.MCPAgentSession{ObjectMeta: metav1.ObjectMeta{Name: "delayed-session", Namespace: "team-a", Annotations: map[string]string{mcpaccess.ServerUIDAnnotation: "old-deployment"}}, Spec: mcpv1alpha1.MCPAgentSessionSpec{ServerRef: mcpv1alpha1.ServerReference{Name: "demo", Namespace: "team-a"}}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithIndex(&mcpv1alpha1.MCPAccessGrant{}, grantTargetServerIndex, grantTargetServerKeys).WithObjects(server, grant, session).Build()
	r := MCPServerReconciler{Client: kube, Scheme: scheme}
	doc, err := r.renderGatewayPolicy(context.Background(), server)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Grants) != 0 || len(doc.Sessions) != 0 {
		t.Fatalf("old deployment authorization rendered: %+v", doc)
	}
	grant.Annotations[mcpaccess.ServerUIDAnnotation] = string(server.UID)
	if err := kube.Update(context.Background(), grant); err != nil {
		t.Fatal(err)
	}
	doc, err = r.renderGatewayPolicy(context.Background(), server)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Grants) != 1 {
		t.Fatal("new deployment grant missing")
	}
}
