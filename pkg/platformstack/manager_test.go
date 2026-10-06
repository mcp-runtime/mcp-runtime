package platformstack

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestGetDeploymentStatusDefaultsNilReplicasToOne(t *testing.T) {
	t.Parallel()

	clientset := fake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "mcp-platform"},
		Spec:       appsv1.DeploymentSpec{},
		Status: appsv1.DeploymentStatus{
			ReadyReplicas: 1,
		},
	})

	manager := NewManager(clientset)
	component := Component{
		Key:       "api",
		Display:   "API",
		Namespace: "mcp-platform",
		Kind:      "deployment",
		Resource:  "api",
	}

	status, err := manager.getDeploymentStatus(context.Background(), component)
	if err != nil {
		t.Fatalf("getDeploymentStatus() error = %v", err)
	}
	if status.Ready != "1/1" {
		t.Fatalf("Ready = %q, want %q", status.Ready, "1/1")
	}
	if status.Status != "Ready" {
		t.Fatalf("Status = %q, want %q", status.Status, "Ready")
	}
}

func TestGetStatefulSetStatusDefaultsNilReplicasToOne(t *testing.T) {
	t.Parallel()

	clientset := fake.NewSimpleClientset(&appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "tempo", Namespace: "mcp-observability"},
		Spec:       appsv1.StatefulSetSpec{},
		Status: appsv1.StatefulSetStatus{
			ReadyReplicas: 0,
		},
	})

	manager := NewManager(clientset)
	component := Component{
		Key:       "tempo",
		Display:   "Tempo",
		Namespace: "mcp-observability",
		Kind:      "statefulset",
		Resource:  "tempo",
	}

	status, err := manager.getStatefulSetStatus(context.Background(), component)
	if err != nil {
		t.Fatalf("getStatefulSetStatus() error = %v", err)
	}
	if status.Ready != "0/1" {
		t.Fatalf("Ready = %q, want %q", status.Ready, "0/1")
	}
	if status.Status != "NotReady" {
		t.Fatalf("Status = %q, want %q", status.Status, "NotReady")
	}
}
