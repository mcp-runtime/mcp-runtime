package cluster

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestMCPAuthConnectorApplyPreservesOtherConfiguration(t *testing.T) {
	const old = `{"keycloak":{"client_id":"mcp-auth","mcp_scopes":["tools:read"]}}`
	const next = `{"keycloak":{"client_id":"mcp-auth","client_secret_env":"KEYCLOAK_CLIENT_SECRET"}}`
	if err := validateMCPAuthConnector([]byte(old), "keycloak"); err == nil {
		t.Fatal("removed connector scope setting was accepted")
	}
	if err := validateMCPAuthConnector([]byte(next), "keycloak"); err != nil {
		t.Fatalf("valid connector rejected: %v", err)
	}
	client := fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: mcpAuthConnectorName, Namespace: mcpAuthNamespace},
		Data:       map[string]string{"connectors.json": old, "unrelated": "keep"},
	})
	changed, err := applyMCPAuthConnector(context.Background(), client, next, true)
	if err != nil || !changed {
		t.Fatalf("dry run = %v, %v; want pending change", changed, err)
	}
	before, err := client.CoreV1().ConfigMaps(mcpAuthNamespace).Get(context.Background(), mcpAuthConnectorName, metav1.GetOptions{})
	if err != nil || before.Data["connectors.json"] != old {
		t.Fatalf("dry run changed ConfigMap: %v, %+v", err, before)
	}
	changed, err = applyMCPAuthConnector(context.Background(), client, next, false)
	if err != nil || !changed {
		t.Fatalf("apply = %v, %v; want update", changed, err)
	}
	after, err := client.CoreV1().ConfigMaps(mcpAuthNamespace).Get(context.Background(), mcpAuthConnectorName, metav1.GetOptions{})
	if err != nil || after.Data["connectors.json"] != next || after.Data["unrelated"] != "keep" {
		t.Fatalf("apply changed unrelated data: %v, %+v", err, after)
	}
	changed, err = applyMCPAuthConnector(context.Background(), client, next, false)
	if err != nil || changed {
		t.Fatalf("repeat apply = %v, %v; want unchanged", changed, err)
	}
}
