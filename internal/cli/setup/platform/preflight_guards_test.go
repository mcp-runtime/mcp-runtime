package platform

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"mcp-runtime/pkg/k8sclient"
	"mcp-runtime/pkg/platforminventory"
)

func guardClients(objs ...interface{}) *k8sclient.Clients {
	cs := fake.NewSimpleClientset()
	for _, o := range objs {
		switch v := o.(type) {
		case *corev1.Namespace:
			_, _ = cs.CoreV1().Namespaces().Create(context.Background(), v, metav1.CreateOptions{})
		case *appsv1.Deployment:
			_, _ = cs.AppsV1().Deployments(v.Namespace).Create(context.Background(), v, metav1.CreateOptions{})
		}
	}
	return &k8sclient.Clients{Clientset: cs}
}

func kubeSystem(uid string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: types.UID(uid)}}
}

func TestCheckClusterIdentity(t *testing.T) {
	clients := guardClients(kubeSystem("uid-prod"))
	if issues := checkClusterIdentity(context.Background(), clients, ""); len(issues) != 0 {
		t.Fatal("no expectation must mean no check")
	}
	if issues := checkClusterIdentity(context.Background(), clients, "uid-prod"); len(issues) != 0 {
		t.Fatalf("matching id must pass: %+v", issues)
	}
	issues := checkClusterIdentity(context.Background(), clients, "uid-staging")
	if len(issues) != 1 || !issues[0].fatal || !strings.Contains(issues[0].message, "uid-prod") {
		t.Fatalf("a mismatched id must be fatal and name the actual cluster: %+v", issues)
	}
	missing := guardClients()
	if issues := checkClusterIdentity(context.Background(), missing, "uid"); len(issues) != 1 || !issues[0].fatal {
		t.Fatal("an unreadable cluster must fail closed when an id is expected")
	}
}

func authDeployment(image string) *appsv1.Deployment {
	component, _ := platforminventory.Lookup("mcp-auth")
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: component.Resource, Namespace: component.Namespace},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "auth-server", Image: image}},
		}}},
	}
}

func TestCheckExistingMCPAuthRetained(t *testing.T) {
	live := "docker.io/example/mcp-auth-server:1.0.0"
	none := guardClients()
	if issues := checkExistingMCPAuthRetained(context.Background(), none, false, ""); len(issues) != 0 {
		t.Fatal("no installed mcp-auth means nothing to protect")
	}
	clients := guardClients(authDeployment(live))
	if issues := checkExistingMCPAuthRetained(context.Background(), clients, true, live); len(issues) != 0 {
		t.Fatalf("retaining the installed image must pass: %+v", issues)
	}
	dropped := checkExistingMCPAuthRetained(context.Background(), clients, false, "")
	if len(dropped) != 1 || !dropped[0].fatal || !strings.Contains(dropped[0].message, "--with-mcp-auth-server") {
		t.Fatalf("dropping an installed mcp-auth must be fatal: %+v", dropped)
	}
	swapped := checkExistingMCPAuthRetained(context.Background(), clients, true, "docker.io/example/mcp-auth-server:0.4.4")
	if len(swapped) != 1 || !swapped[0].fatal || !strings.Contains(swapped[0].message, live) {
		t.Fatalf("swapping the image must be fatal and name the live one: %+v", swapped)
	}
}
