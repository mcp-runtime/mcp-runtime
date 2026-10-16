package operator

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
)

func TestDeploymentFailureDetailsReportsNewCrashLoopPodAndEvent(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := mcpv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{LabelApp: "demo", LabelManagedBy: LabelManagedByValue, "mcpruntime.org/rollout-track": "stable"}
	oldReadyPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-old", Namespace: "team", Labels: labels, CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Minute))},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}
	newCrashLoopPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-new", Namespace: "team", UID: types.UID("pod-new"), Labels: labels, CreationTimestamp: metav1.NewTime(time.Now())},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "server", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff", Message: "back-off restarting failed container"}}}}},
	}
	event := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "demo-new-warning", Namespace: "team"},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: newCrashLoopPod.Name, UID: newCrashLoopPod.UID},
		Type:           corev1.EventTypeWarning, Reason: "BackOff", Message: "Back-off restarting failed container", LastTimestamp: metav1.NewTime(time.Now()),
	}
	// The cached client must never list Events: their RBAC is namespace-scoped.
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(oldReadyPod, newCrashLoopPod).Build()
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(event).Build()
	r := &MCPServerReconciler{Client: client, APIReader: reader}
	server := &mcpv1alpha1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "team"}}
	one := int32(1)
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "team", Generation: 2}, Spec: appsv1.DeploymentSpec{Replicas: &one}, Status: appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: 1, ReadyReplicas: 1, AvailableReplicas: 1}}
	if deploymentRevisionReady(deployment, 1) {
		t.Fatal("previous ready replica must not satisfy the requested deployment revision")
	}

	got := r.deploymentFailureDetails(context.Background(), server)
	for _, want := range []string{"pod=demo-new", "container=server", "reason=CrashLoopBackOff", "recentEvent=\"Back-off restarting failed container\""} {
		if !strings.Contains(got, want) {
			t.Fatalf("deploymentFailureDetails() = %q, missing %q", got, want)
		}
	}
	if strings.Contains(got, "demo-old") {
		t.Fatalf("diagnostics selected the previous Ready pod: %q", got)
	}
}

func TestDeploymentWarningEventRejectsReusedPodName(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "current-pod"}}
	for _, tc := range []struct {
		uid  types.UID
		want bool
	}{
		{"current-pod", true},
		{"previous-pod", false},
		{"", false},
	} {
		event := &corev1.Event{InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "demo", UID: tc.uid}}
		if got := eventMatchesPod(event, pod); got != tc.want {
			t.Errorf("event UID %q: matched=%v, want %v", tc.uid, got, tc.want)
		}
	}
}
