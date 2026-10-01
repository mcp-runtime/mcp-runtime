package k8sclient

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestWaitForWorkloadRolloutWaitsForDeploymentCurrentRevision(t *testing.T) {
	replicas := int32(2)
	clients := &Clients{Clientset: kubernetesfake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "mcp-sentinel", Generation: 2},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 2,
			Replicas:           2,
			UpdatedReplicas:    1,
			AvailableReplicas:  1,
		},
	})}

	err := WaitForWorkloadRollout(context.Background(), clients, "mcp-sentinel", "deployment", "api", time.Millisecond)
	if err == nil {
		t.Fatal("expected rollout wait to fail while only an old replica is available")
	}
}

func TestWaitForWorkloadRolloutAcceptsRolledOutDeployment(t *testing.T) {
	replicas := int32(2)
	clients := &Clients{Clientset: kubernetesfake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "mcp-sentinel", Generation: 2},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 2,
			Replicas:           2,
			UpdatedReplicas:    2,
			AvailableReplicas:  2,
		},
	})}

	if err := WaitForWorkloadRollout(context.Background(), clients, "mcp-sentinel", "deployment", "api", time.Second); err != nil {
		t.Fatalf("WaitForWorkloadRollout() error = %v", err)
	}
}

func TestWaitForWorkloadRolloutWaitsForDaemonSetReadyReplicas(t *testing.T) {
	clients := &Clients{Clientset: kubernetesfake.NewSimpleClientset(&appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "promtail", Namespace: "mcp-log-collector", Generation: 2},
		Status: appsv1.DaemonSetStatus{
			ObservedGeneration:     1,
			DesiredNumberScheduled: 1,
			NumberReady:            0,
			UpdatedNumberScheduled: 0,
		},
	})}

	err := WaitForWorkloadRollout(context.Background(), clients, "mcp-log-collector", "daemonset", "promtail", time.Millisecond)
	if err == nil {
		t.Fatal("expected rollout wait to fail while daemonset replicas are not ready")
	}
}

func TestWaitForWorkloadRolloutAcceptsReadyDaemonSet(t *testing.T) {
	clients := &Clients{Clientset: kubernetesfake.NewSimpleClientset(&appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "promtail", Namespace: "mcp-log-collector", Generation: 2},
		Status: appsv1.DaemonSetStatus{
			ObservedGeneration:     2,
			DesiredNumberScheduled: 1,
			NumberReady:            1,
			UpdatedNumberScheduled: 1,
		},
	})}

	if err := WaitForWorkloadRollout(context.Background(), clients, "mcp-log-collector", "daemonset", "promtail", time.Second); err != nil {
		t.Fatalf("WaitForWorkloadRollout() error = %v", err)
	}
}

func TestDeleteJobUsesBackgroundPropagation(t *testing.T) {
	clientset := kubernetesfake.NewSimpleClientset(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "clickhouse-init", Namespace: "mcp-sentinel"},
	})
	clients := &Clients{Clientset: clientset}
	clientset.PrependReactor("delete", "jobs", func(action clienttesting.Action) (bool, runtime.Object, error) {
		deleteAction := action.(clienttesting.DeleteAction)
		options := deleteAction.GetDeleteOptions()
		if options.PropagationPolicy == nil || *options.PropagationPolicy != metav1.DeletePropagationBackground {
			t.Fatalf("PropagationPolicy = %#v, want Background", options)
		}
		return false, nil, nil
	})

	if err := DeleteJob(context.Background(), clients, "mcp-sentinel", "clickhouse-init", time.Second); err != nil {
		t.Fatalf("DeleteJob() error = %v", err)
	}
}

func TestWaitForCertificateReadyPropagatesAPIError(t *testing.T) {
	clients := &Clients{Dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())}
	clients.Dynamic.(*dynamicfake.FakeDynamicClient).PrependReactor("get", "certificates", func(action clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "cert-manager.io", Resource: "certificates"}, "registry-cert", errors.New("denied"))
	})

	err := WaitForCertificateReady(context.Background(), clients, "registry", "registry-cert", time.Second)
	if err == nil {
		t.Fatal("expected certificate wait to propagate API error")
	}
	if !apierrors.IsForbidden(err) {
		t.Fatalf("WaitForCertificateReady() error = %v, want forbidden", err)
	}
}

func TestWaitForCertificateReadyAcceptsReadyCertificate(t *testing.T) {
	clients := &Clients{Dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "cert-manager.io/v1",
			"kind":       "Certificate",
			"metadata": map[string]any{
				"name":      "registry-cert",
				"namespace": "registry",
			},
			"status": map[string]any{
				"conditions": []any{
					map[string]any{"type": "Ready", "status": "True"},
				},
			},
		},
	})}

	if err := WaitForCertificateReady(context.Background(), clients, "registry", "registry-cert", time.Second); err != nil {
		t.Fatalf("WaitForCertificateReady() error = %v", err)
	}
}

func TestPruneTerminatedPodsRemovesOnlyTerminalLeftovers(t *testing.T) {
	pod := func(name string, phase corev1.PodPhase, owners ...metav1.OwnerReference) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "mcp-sentinel", OwnerReferences: owners},
			Status:     corev1.PodStatus{Phase: phase},
		}
	}
	clientset := kubernetesfake.NewSimpleClientset(
		pod("evicted", corev1.PodFailed),
		pod("orphan-done", corev1.PodSucceeded),
		pod("job-done", corev1.PodSucceeded, metav1.OwnerReference{Kind: "Job", Name: "kafka-topic-init"}),
		pod("running", corev1.PodRunning),
		pod("pending", corev1.PodPending),
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "other-ns", Namespace: "other"},
			Status:     corev1.PodStatus{Phase: corev1.PodFailed},
		},
	)
	deleted, err := PruneTerminatedPods(context.Background(), &Clients{Clientset: clientset}, "mcp-sentinel")
	if err != nil {
		t.Fatalf("PruneTerminatedPods: %v", err)
	}
	if got := strings.Join(deleted, ","); got != "evicted,orphan-done" {
		t.Fatalf("deleted = %q, want evicted,orphan-done", got)
	}
	remaining, _ := clientset.CoreV1().Pods("mcp-sentinel").List(context.Background(), metav1.ListOptions{})
	names := map[string]bool{}
	for _, p := range remaining.Items {
		names[p.Name] = true
	}
	for _, want := range []string{"job-done", "running", "pending"} {
		if !names[want] {
			t.Fatalf("pod %s must be preserved; remaining=%v", want, names)
		}
	}
	if _, err := clientset.CoreV1().Pods("other").Get(context.Background(), "other-ns", metav1.GetOptions{}); err != nil {
		t.Fatalf("pod in another namespace must be untouched: %v", err)
	}
}
