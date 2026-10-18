package operator

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	mcpv1 "mcp-runtime/api/v1alpha1"
	"mcp-runtime/pkg/policy"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCredentialEnabledGatewayRequiresHTTPSAndProjectsOnlyDedicatedToken(t *testing.T) {
	server := &mcpv1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "server", Namespace: "team", UID: "uid"}, Spec: mcpv1.MCPServerSpec{Image: "fixture/server:v1", CredentialBindings: []string{"upstream"}, Gateway: &mcpv1.GatewayConfig{Image: "fixture/gateway:v1"}, Policy: &mcpv1.PolicyConfig{Mode: mcpv1.PolicyModeAllowList, DefaultDecision: mcpv1.PolicyDecisionDeny}}}
	r := &MCPServerReconciler{}
	if _, err := r.buildGatewayContainer(server); err == nil {
		t.Fatal("credential-enabled gateway used an unset broker")
	}
	r.GatewayCredentialBrokerURL = "http://platform.internal"
	if _, err := r.buildGatewayContainer(server); err == nil {
		t.Fatal("plaintext broker configured")
	}
	r.GatewayCredentialBrokerURL = "https://platform.example.test"
	container, err := r.buildGatewayContainer(server)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, env := range container.Env {
		found[env.Name] = true
		if env.Name == "CREDENTIAL_BROKER_URL" && env.Value != r.GatewayCredentialBrokerURL {
			t.Fatal("broker origin changed")
		}
	}
	if !found["CREDENTIAL_BROKER_TOKEN_FILE"] || !found["CREDENTIAL_BROKER_URL"] {
		t.Fatal("broker authentication missing")
	}
	for _, env := range container.Env {
		if env.Name == "INTERNAL_AUTH_TOKEN" {
			t.Fatal("broad service credential leaked to gateway")
		}
	}
	for _, mount := range container.VolumeMounts {
		if mount.Name == "gateway-credential-token" && !mount.ReadOnly {
			t.Fatal("projected token is writable")
		}
	}
	containers, volumes, err := r.buildDeploymentContainers(server, server.Spec.Image)
	if err != nil {
		t.Fatal(err)
	}
	for _, mount := range containers[0].VolumeMounts {
		if mount.Name == "gateway-credential-token" {
			t.Fatal("workload received gateway broker credential")
		}
	}
	projected := false
	for _, volume := range volumes {
		if volume.Name != "gateway-credential-token" {
			continue
		}
		projected = true
		if volume.Projected == nil || len(volume.Projected.Sources) != 1 || volume.Projected.Sources[0].ServiceAccountToken == nil || volume.Projected.Sources[0].ServiceAccountToken.Audience != "mcp-runtime-credential-broker" || *volume.Projected.Sources[0].ServiceAccountToken.ExpirationSeconds != 600 {
			t.Fatal("broker token scope/lifetime changed")
		}
	}
	if !projected {
		t.Fatal("broker token not projected")
	}
	scheme := runtime.NewScheme()
	_ = mcpv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	r.Client = fake.NewClientBuilder().WithScheme(scheme).WithIndex(&mcpv1.MCPAccessGrant{}, grantTargetServerIndex, func(obj client.Object) []string { return nil }).Build()
	doc, err := r.renderGatewayPolicy(context.Background(), server)
	if err != nil {
		t.Fatal(err)
	}
	if doc.SchemaVersion != policy.SchemaVersionCredentialBroker || doc.Session == nil || !doc.Session.Required || len(doc.CredentialBindings) != 1 {
		t.Fatal("credential policy allows sessionless/old gateway use")
	}
	if err = policy.Validate(doc); err != nil {
		t.Fatal(err)
	}
}
