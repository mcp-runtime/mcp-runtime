package operator

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	v1 "mcp-runtime/api/v1alpha1"
	"mcp-runtime/pkg/policy"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestRenderMonitorGrantRequiresSchemaV5(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	server := &v1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "servers"}}
	grant := &v1.MCPAccessGrant{ObjectMeta: metav1.ObjectMeta{Name: "monitor", Namespace: "servers"}, Spec: v1.MCPAccessGrantSpec{Mode: v1.GrantModeMonitor, ServerRef: v1.ServerReference{Name: "demo"}, Subject: v1.SubjectRef{HumanID: "human"}, MaxTrust: v1.TrustLevelHigh, AllowedSideEffects: []v1.ToolSideEffect{v1.ToolSideEffectRead}}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithIndex(&v1.MCPAccessGrant{}, grantTargetServerIndex, grantTargetServerKeys).WithObjects(server, grant).Build()
	r := MCPServerReconciler{GatewayProxyImage: "example.com/mcp-gateway:test", Client: c, Scheme: scheme}
	doc, err := r.renderGatewayPolicy(context.Background(), server)
	if err != nil {
		t.Fatal(err)
	}
	if doc.SchemaVersion != policy.SchemaVersionGrantMonitor || len(doc.Grants) != 1 || doc.Grants[0].Mode != "monitor" {
		t.Fatalf("rendered policy lost monitor mode: %#v", doc)
	}
	if err := policy.Validate(doc); err != nil {
		t.Fatal(err)
	}
}
