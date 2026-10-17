package update

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"mcp-runtime/internal/platformrelease"
)

func TestScopedGrafanaUpdateRequiresSupportedSetupMigration(t *testing.T) {
	objects := []platformrelease.PlatformObject{{Kind: "NetworkPolicy", Namespace: "mcp-observability", Name: "mcp-scoped-observability-ingress"}}
	config := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "mcp-shared-config", Namespace: "mcp-platform"}, Data: map[string]string{"GRAFANA_SCOPED_USER_ACCESS": "true", "UI_GRAFANA_PATH": "/monitoring"}}
	grafana := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "grafana", Namespace: "mcp-observability"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "grafana", Env: []corev1.EnvVar{{Name: "GF_AUTH_PROXY_ENABLED", Value: "true"}}}}}}}}
	ingress := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "mcp-platform-observability", Namespace: "mcp-platform"}, Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{{Path: "/monitoring", Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "mcp-ui", Port: networkingv1.ServiceBackendPort{Number: 8082}}}}}}}}}}}
	for _, tc := range []struct {
		name string
		live []runtime.Object
		want bool
	}{
		{"unmigrated", nil, false},
		{"flag-only", []runtime.Object{config}, false},
		{"missing-proxy-route", []runtime.Object{config, grafana}, false},
		{"migrated", []runtime.Object{config, grafana, ingress}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := fake.NewSimpleClientset(tc.live...)
			err := requireScopedGrafanaMigration(context.Background(), cs, objects)
			if (err == nil) != tc.want {
				t.Fatalf("migration result %v, want allowed=%v", err, tc.want)
			}
			if err != nil && !strings.Contains(err.Error(), "mcp-runtime setup") {
				t.Fatalf("missing supported recovery path: %v", err)
			}
			for _, action := range cs.Actions() {
				if action.GetVerb() != "get" && action.GetVerb() != "list" {
					t.Fatalf("preflight mutated cluster before setup migration: %v", action)
				}
			}
		})
	}
	legacy := ingress.DeepCopy()
	legacy.Namespace = "mcp-observability"
	legacy.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Name = "grafana"
	cs := fake.NewSimpleClientset(config, grafana, ingress, legacy)
	if err := requireScopedGrafanaMigration(context.Background(), cs, objects); err == nil {
		t.Fatal("legacy direct route accepted")
	}
	// Unrelated policy updates and component-only updates do not silently
	// enable or require the tenant Grafana feature.
	if err := requireScopedGrafanaMigration(context.Background(), fake.NewSimpleClientset(), nil); err != nil {
		t.Fatalf("component-only update should preserve existing admin-only access: %v", err)
	}
}

func TestAdminOnlyUpdatePreservesGrafanaWithoutPartialPolicyMigration(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "mcp-shared-config", Namespace: "mcp-platform"}, Data: map[string]string{"GRAFANA_SCOPED_USER_ACCESS": "false"}})
	plan := &Plan{allObjects: []platformrelease.PlatformObject{
		{Kind: "NetworkPolicy", Namespace: "mcp-observability", Name: "mcp-scoped-observability-ingress"},
		{Kind: "NetworkPolicy", Namespace: "mcp-platform", Name: "mcp-runtime-api-grafana-egress"},
		{Kind: "NetworkPolicy", Namespace: "mcp-observability", Name: "kafka-client-ingress"},
	}, Rows: []Row{{Component: "runtime-api"}, {Component: "grafana"}}}
	if err := gateScopedGrafanaPolicies(context.Background(), cs, plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.allObjects) != 1 || plan.allObjects[0].Name != "kafka-client-ingress" || len(plan.Rows[0].Notes) != 1 || len(plan.Rows[1].Notes) != 1 {
		t.Fatalf("admin-only update applied partial Grafana migration or suppressed unrelated policies: %+v %+v", plan.allObjects, plan.Rows)
	}
	for _, action := range cs.Actions() {
		if action.GetVerb() != "get" {
			t.Fatalf("admin-only plan changed feature state: %v", action)
		}
	}
}
