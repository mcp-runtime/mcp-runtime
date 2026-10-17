package k8sprovision

import (
	"context"
	"errors"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestEnsureTraefikWatchRBACUsesNamespaceRole(t *testing.T) {
	cfg := TraefikWatchConfig{
		Namespace:      "traefik",
		ServiceAccount: "traefik",
	}
	existingRole := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: TraefikWatchRoleName, Namespace: "mcp-servers-public"},
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{""}, Resources: []string{"services", "endpoints"}, Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{"networking.k8s.io"}, Resources: []string{"ingresses"}, Verbs: []string{"get", "list", "watch"}},
		},
	}
	existingBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: TraefikWatchRoleName, Namespace: "mcp-servers-public"},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     TraefikWatchRoleName,
		},
		Subjects: []rbacv1.Subject{
			{Kind: "ServiceAccount", Name: cfg.ServiceAccount, Namespace: cfg.Namespace},
		},
	}
	client := kubernetesfake.NewSimpleClientset(existingRole, existingBinding)

	if err := ensureTraefikWatchRBAC(context.Background(), client, "mcp-servers-public", cfg); err != nil {
		t.Fatalf("ensureTraefikWatchRBAC() error = %v", err)
	}
	binding, err := client.RbacV1().RoleBindings("mcp-servers-public").Get(context.Background(), TraefikWatchRoleName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("traefik watch rolebinding missing: %v", err)
	}
	if binding.RoleRef.Kind != "Role" || binding.RoleRef.Name != TraefikWatchRoleName {
		t.Fatalf("traefik watch binding role ref = %#v, want Role/%s", binding.RoleRef, TraefikWatchRoleName)
	}
	role, err := client.RbacV1().Roles("mcp-servers-public").Get(context.Background(), TraefikWatchRoleName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("traefik watch role missing: %v", err)
	}
	if len(role.Rules) != 4 {
		t.Fatalf("traefik watch role rules = %#v, want 4 rules", role.Rules)
	}
	hasSecrets := false
	for _, resource := range role.Rules[0].Resources {
		if resource == "secrets" {
			hasSecrets = true
			break
		}
	}
	if !hasSecrets {
		t.Fatalf("traefik watch role rules = %#v, want secrets watch in namespace role", role.Rules)
	}
	for _, resource := range []string{"endpointslices", "configmaps", "pods"} {
		found := false
		for _, rule := range role.Rules {
			for _, name := range rule.Resources {
				if name == resource {
					found = true
				}
			}
			for _, verb := range rule.Verbs {
				if verb != "get" && verb != "list" && verb != "watch" {
					t.Fatalf("watch role grants mutation: %v", rule)
				}
			}
		}
		if !found {
			t.Errorf("Traefik v3 required resource %s missing in namespace role", resource)
		}
	}
}

func TestEnsureTraefikDeploymentWatchesNamespaceRetriesConflict(t *testing.T) {
	client := kubernetesfake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "registry"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mcp-platform"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mcp-observability"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mcp-servers"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mcp-team-beta"}},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "traefik", Namespace: "traefik"},
			Spec: appsv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Name: "traefik",
							Args: []string{
								"--providers.kubernetesingress=true",
								"--providers.kubernetesingress.namespaces=registry,mcp-platform,mcp-observability,mcp-servers",
							},
						}},
					},
				},
			},
		},
	)
	updateAttempts := 0
	client.Fake.PrependReactor("update", "deployments", func(action k8stesting.Action) (bool, runtime.Object, error) {
		updateAttempts++
		if updateAttempts == 1 {
			return true, nil, apierrors.NewConflict(
				schema.GroupResource{Group: "apps", Resource: "deployments"},
				"traefik",
				errors.New("stale deployment resource version"),
			)
		}
		return false, nil, nil
	})

	err := ensureTraefikDeploymentWatchesNamespace(context.Background(), client, "mcp-team-beta", TraefikWatchConfig{
		Mode:       "required",
		Namespace:  "traefik",
		Deployment: "traefik",
	})
	if err != nil {
		t.Fatalf("ensureTraefikDeploymentWatchesNamespace() error = %v", err)
	}
	if updateAttempts < 2 {
		t.Fatalf("expected update retry after conflict, got %d attempt(s)", updateAttempts)
	}
	deployment, err := client.AppsV1().Deployments("traefik").Get(context.Background(), "traefik", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("traefik deployment missing: %v", err)
	}
	args := strings.Join(deployment.Spec.Template.Spec.Containers[0].Args, "\n")
	if !strings.Contains(args, "--providers.kubernetesingress.namespaces=registry,mcp-platform,mcp-observability,mcp-servers,mcp-team-beta") {
		t.Fatalf("traefik namespace args = %q", args)
	}
}

func TestRemoveTraefikDeploymentWatchesNamespace(t *testing.T) {
	client := kubernetesfake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mcp-servers"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mcp-team-beta"}},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "traefik", Namespace: "traefik"},
			Spec: appsv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Name: "traefik",
							Args: []string{
								"--providers.kubernetesingress.namespaces=registry,mcp-servers,mcp-team-beta",
								"--providers.kubernetescrd.namespaces=mcp-servers,mcp-team-beta",
							},
						}},
					},
				},
			},
		},
	)
	cfg := TraefikWatchConfig{Mode: "required", Namespace: "traefik", Deployment: "traefik"}
	if err := RemoveTraefikDeploymentWatchesNamespace(context.Background(), client, "mcp-team-beta", cfg); err != nil {
		t.Fatalf("RemoveTraefikDeploymentWatchesNamespace: %v", err)
	}
	deployment, err := client.AppsV1().Deployments("traefik").Get(context.Background(), "traefik", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get traefik: %v", err)
	}
	args := strings.Join(deployment.Spec.Template.Spec.Containers[0].Args, "\n")
	if strings.Contains(args, "mcp-team-beta") {
		t.Fatalf("expected team namespace removed, got %q", args)
	}
	if !strings.Contains(args, "--providers.kubernetesingress.namespaces=registry,mcp-servers") {
		t.Fatalf("ingress watch = %q", args)
	}
	if !strings.Contains(args, "--providers.kubernetescrd.namespaces=mcp-servers") {
		t.Fatalf("crd watch = %q", args)
	}
}

func TestEnsureTraefikDeploymentWatchesNamespacePrunesMissing(t *testing.T) {
	client := kubernetesfake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mcp-servers"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mcp-team-live"}},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "traefik", Namespace: "traefik"},
			Spec: appsv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Name: "traefik",
							Args: []string{
								"--providers.kubernetesingress.namespaces=mcp-servers,mcp-team-gone,mcp-team-live",
								"--providers.kubernetescrd.namespaces=mcp-servers,mcp-team-gone,mcp-team-live",
							},
						}},
					},
				},
			},
		},
	)
	err := ensureTraefikDeploymentWatchesNamespace(context.Background(), client, "mcp-team-live", TraefikWatchConfig{
		Mode:       "required",
		Namespace:  "traefik",
		Deployment: "traefik",
	})
	if err != nil {
		t.Fatalf("ensureTraefikDeploymentWatchesNamespace: %v", err)
	}
	deployment, err := client.AppsV1().Deployments("traefik").Get(context.Background(), "traefik", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get traefik: %v", err)
	}
	args := strings.Join(deployment.Spec.Template.Spec.Containers[0].Args, "\n")
	if strings.Contains(args, "mcp-team-gone") {
		t.Fatalf("expected missing namespace pruned, got %q", args)
	}
	if !strings.Contains(args, "mcp-team-live") || !strings.Contains(args, "mcp-servers") {
		t.Fatalf("expected live namespaces kept, got %q", args)
	}
}
