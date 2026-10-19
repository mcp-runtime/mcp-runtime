package update

import (
	"context"

	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"

	"mcp-runtime/internal/platformrelease"
	"mcp-runtime/pkg/platforminventory"
	"mcp-runtime/pkg/resourcehistory"
)

func desiredNetworkPolicy(ctx context.Context, cs kubernetes.Interface, obj platformrelease.PlatformObject) (*networkingv1.NetworkPolicy, error) {
	want := &networkingv1.NetworkPolicy{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, want); err != nil {
		return nil, err
	}
	if obj.Namespace == platforminventory.PlatformNamespace && obj.Name == resourcehistory.NodeEgressPolicy {
		egress, err := resourcehistory.NodeEgress(ctx, cs, obj.Namespace)
		if err != nil {
			return nil, err
		}
		want.Spec.Egress = egress
	}
	return want, nil
}
