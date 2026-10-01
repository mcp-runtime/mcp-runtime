package platform

import (
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"mcp-runtime/internal/cli/core"
)

func TestMCPAuthRolloutRequiresCurrentRevision(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    appsv1.DeploymentStatus
		wantError bool
	}{
		{
			name:      "old pod ready while replacement crashes",
			status:    appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: 2, UpdatedReplicas: 1, AvailableReplicas: 1},
			wantError: true,
		},
		{
			name:      "controller has not observed new resources",
			status:    appsv1.DeploymentStatus{ObservedGeneration: 1, Replicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1},
			wantError: true,
		},
		{
			name:   "current auth revision ready",
			status: appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1},
		},
		{
			name:      "failed rollout with old pod available",
			status:    appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: 2, UpdatedReplicas: 1, AvailableReplicas: 1, Conditions: []appsv1.DeploymentCondition{{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded"}}},
			wantError: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetPlatformKubeconfig(t)
			platformSetupKubeconfig = ""
			replicas := int32(1)
			clients := newPlatformKubernetesTestClients([]runtime.Object{&appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: "mcp-auth-server", Namespace: core.ComponentNamespace("mcp-auth"), Generation: 2},
				Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
				Status:     tc.status,
			}}, nil)
			swapKubernetesClientsForTest(t, clients)
			// Diagnostics must never fall back to the contributor's real cluster.
			swapDefaultKubectlClientForTest(t, core.NewTestKubectlClient(&core.MockExecutor{}))
			err := waitForDeploymentRolledOut(zap.NewNop(), "mcp-auth-server", core.ComponentNamespace("mcp-auth"), "app=mcp-auth-server", time.Millisecond)
			if (err != nil) != tc.wantError {
				t.Fatalf("waitForDeploymentRolledOut() = %v, wantError %v", err, tc.wantError)
			}
			if tc.wantError && !errors.Is(err, core.ErrDeploymentTimeout) {
				t.Fatalf("expected deployment readiness error, got %v", err)
			}
		})
	}
}
