package operator

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	"mcp-runtime/pkg/policy"
)

func TestRuntimeSessionChangesDoNotRewritePolicy(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	server := &mcpv1alpha1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "server", Namespace: "team", UID: "server-uid"}, Spec: mcpv1alpha1.MCPServerSpec{Session: &mcpv1alpha1.SessionConfig{Required: true}}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(server).Build()
	reconciler := &MCPServerReconciler{Client: kube, Scheme: scheme}
	before, err := reconciler.renderGatewayPolicy(context.Background(), server)
	if err != nil {
		t.Fatal(err)
	}
	if before.SchemaVersion != policy.SchemaVersionSessionSync || before.Session.Store != "platform" {
		t.Fatalf("policy=%+v", before)
	}
	old, err := renderPolicyConfigMapData("", before)
	if err != nil {
		t.Fatal(err)
	}
	session := &mcpv1alpha1.MCPAgentSession{ObjectMeta: metav1.ObjectMeta{Name: "legacy", Namespace: "team"}, Spec: mcpv1alpha1.MCPAgentSessionSpec{ServerRef: mcpv1alpha1.ServerReference{Name: "server"}, Subject: mcpv1alpha1.SubjectRef{HumanID: "human"}, Revoked: true}}
	if err := kube.Create(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	after, err := reconciler.renderGatewayPolicy(context.Background(), server)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := renderPolicyConfigMapData(old, after)
	if err != nil {
		t.Fatal(err)
	}
	if old != updated || before.Revision != after.Revision || len(after.Sessions) != 0 {
		t.Fatal("session change altered the rendered policy")
	}
	var payload map[string]any
	_ = json.Unmarshal([]byte(updated), &payload)
	if _, ok := payload["sessions"]; ok {
		t.Fatal("sessions embedded in policy")
	}
}

func TestPolicySizeGuardRejectsOversizedGrants(t *testing.T) {
	doc := &policy.Document{Server: policy.Server{Name: "server"}, Grants: []policy.Grant{{Name: "oversized", HumanID: policy.HumanID(strings.Repeat("x", maxGatewayPolicyBytes))}}}
	if err := policy.Stamp(doc, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := renderPolicyConfigMapData("", doc); err == nil || !strings.Contains(err.Error(), "size guard") {
		t.Fatalf("oversize error=%v", err)
	}
}
