package cluster

import (
	"context"
	"encoding/json"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"mcp-runtime/pkg/authgateway"
)

func TestAuthGatewayApplyDryRunAndPreservation(t *testing.T) {
	const resource = "https://auth.example.com/demo/mcp"
	raw := []byte(`{"listen":{"host":"0.0.0.0","port":8090},"authorization_server":{"issuer":"https://auth.example.com/mcp-auth"},"services":[{"id":"demo","resource_url":"https://auth.example.com/demo/mcp","upstream_url":"https://upstream.example.com/mcp","validation_url":"https://upstream.example.com/check","required_scopes":["tools:read"]}]}`)
	cfg, err := authgateway.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	group := int64(65532)
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "mcp-auth-server", Namespace: mcpAuthNamespace}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{SecurityContext: &corev1.PodSecurityContext{FSGroup: &group}, Containers: []corev1.Container{{Name: "auth-server", Image: "review-image", Env: []corev1.EnvVar{{Name: "MCP_AUTH_ISSUER", Value: cfg.AuthorizationServer.Issuer}, {Name: "MCP_AUTH_RESOURCES", Value: "https://mcp.example.com/ordinary/mcp"}, {Name: "KEEP", Value: "yes"}}}}, Volumes: []corev1.Volume{{Name: "data"}}}}}}
	c := fake.NewSimpleClientset(d, &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "mcp-auth-server", Namespace: mcpAuthNamespace}, Spec: networkingv1.IngressSpec{TLS: []networkingv1.IngressTLS{{Hosts: []string{"auth.example.com"}, SecretName: "existing-tls"}}}})
	payload := map[string][]byte{"config.json": raw, "client.pem": []byte("fixture-sensitive")}
	if err := applyAuthGateway(context.Background(), c, cfg, payload, "existing-tls", true); err != nil {
		t.Fatal(err)
	}
	for _, a := range c.Actions() {
		if a.GetVerb() != "get" {
			t.Fatal("dry run mutated Kubernetes")
		}
	}
	if err := applyAuthGateway(context.Background(), c, cfg, payload, "existing-tls", false); err != nil {
		t.Fatal(err)
	}
	after, _ := c.AppsV1().Deployments(mcpAuthNamespace).Get(context.Background(), d.Name, metav1.GetOptions{})
	if after.Spec.Template.Spec.Containers[0].Image != "review-image" || after.Spec.Template.Spec.Volumes[0].Name != "data" {
		t.Fatal("unrelated deployment wiring changed")
	}
	var registered map[string][]string
	if json.Unmarshal([]byte(after.Annotations[authgateway.ResourceAnnotation]), &registered) != nil || len(registered[resource]) != 1 {
		t.Fatal("gateway registration missing")
	}
	secret, _ := c.CoreV1().Secrets(mcpAuthNamespace).Get(context.Background(), authgateway.SecretName, metav1.GetOptions{})
	if string(secret.Data["client.pem"]) != "fixture-sensitive" {
		t.Fatal("secret was not stored")
	}
	ingress, _ := c.NetworkingV1().Ingresses(mcpAuthNamespace).Get(context.Background(), authgateway.SecretName, metav1.GetOptions{})
	if ingress.Spec.TLS[0].SecretName != "existing-tls" || len(ingress.Spec.Rules[0].HTTP.Paths) != 2 {
		t.Fatal("existing TLS/discovery wiring incorrect")
	}
	if err := applyAuthGateway(context.Background(), c, cfg, payload, "existing-tls", false); err != nil {
		t.Fatal("repeat apply failed", err)
	}
}
