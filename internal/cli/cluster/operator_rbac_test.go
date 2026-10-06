package cluster

import (
	"context"
	"reflect"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestReconcileOperatorRBACRepairsOnlyManagedRoles(t *testing.T) {
	clusterRole, authRole, authBinding, err := loadOperatorRBAC()
	if err != nil {
		t.Fatal(err)
	}
	old := clusterRole.DeepCopy()
	old.Rules = old.Rules[:len(old.Rules)-1] // v0.6.2 production lacked EndpointSlice reads.
	client := fake.NewClientset(old, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "mcp-auth-server", Namespace: mcpAuthNamespace}})
	ctx := context.Background()
	changes, err := reconcileOperatorRBAC(ctx, client, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 3 || !strings.HasPrefix(changes[0], "would update ") ||
		!strings.HasPrefix(changes[1], "would create ") || !strings.HasPrefix(changes[2], "would create ") {
		t.Fatalf("dry-run plan = %v", changes)
	}
	got, err := client.RbacV1().ClusterRoles().Get(ctx, operatorRoleName, metav1.GetOptions{})
	if err != nil || !reflect.DeepEqual(got.Rules, old.Rules) {
		t.Fatalf("dry-run changed ClusterRole: %+v %v", got, err)
	}
	if _, err := client.RbacV1().Roles(mcpAuthNamespace).Get(ctx, authRole.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("dry-run created MCP Auth Role: %v", err)
	}
	if _, err := reconcileOperatorRBAC(ctx, client, false); err != nil {
		t.Fatal(err)
	}
	got, err = client.RbacV1().ClusterRoles().Get(ctx, operatorRoleName, metav1.GetOptions{})
	if err != nil || !reflect.DeepEqual(got.Rules, clusterRole.Rules) {
		t.Fatalf("ClusterRole not repaired: %+v %v", got, err)
	}
	actualRole, err := client.RbacV1().Roles(mcpAuthNamespace).Get(ctx, authRole.Name, metav1.GetOptions{})
	if err != nil || !reflect.DeepEqual(actualRole.Rules, authRole.Rules) {
		t.Fatalf("MCP Auth Role not installed: %+v %v", actualRole, err)
	}
	actualBinding, err := client.RbacV1().RoleBindings(mcpAuthNamespace).Get(ctx, authBinding.Name, metav1.GetOptions{})
	if err != nil || !reflect.DeepEqual(actualBinding.Subjects, authBinding.Subjects) || actualBinding.RoleRef != authBinding.RoleRef {
		t.Fatalf("MCP Auth binding not installed: %+v %v", actualBinding, err)
	}
}

func TestReconcileOperatorRBACSkipsOptionalAuthWhenAbsent(t *testing.T) {
	clusterRole, authRole, _, err := loadOperatorRBAC()
	if err != nil {
		t.Fatal(err)
	}
	client := fake.NewClientset(clusterRole)
	if _, err := reconcileOperatorRBAC(context.Background(), client, false); err != nil {
		t.Fatal(err)
	}
	if _, err := client.RbacV1().Roles(mcpAuthNamespace).Get(context.Background(), authRole.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("optional MCP Auth Role created without its Deployment: %v", err)
	}
}
