package update

import (
	"context"
	"errors"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"mcp-runtime/internal/platformrelease"
)

func kafkaPolicy(t *testing.T) platformrelease.PlatformObject {
	t.Helper()
	bundle, err := platformrelease.BundlePlatformObjects("../../../k8s", []string{"NetworkPolicy/mcp-observability/kafka-client-ingress"})
	if err != nil {
		t.Fatal(err)
	}
	objects, err := platformrelease.ParsePlatformObjects(bundle)
	if err != nil || len(objects) != 1 {
		t.Fatalf("policy bundle: %v (%d objects)", err, len(objects))
	}
	return objects[0]
}

func TestUpdateReconcilesKafkaIsolationWithoutChangingOtherPolicies(t *testing.T) {
	ctx := context.Background()
	obj := kafkaPolicy(t)
	other := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "tenant-policy", Namespace: "team-a", Labels: map[string]string{"owner": "tenant"}}}
	cs := fake.NewSimpleClientset(other)
	plan := &Plan{allObjects: []platformrelease.PlatformObject{obj}}
	if err := refineObjectPlan(ctx, cs, plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.objects) != 1 || plan.PlatformObjects[0].Action != ObjectActionCreate {
		t.Fatalf("missing policy not planned: %+v", plan.PlatformObjects)
	}
	if _, err := applyPlatformObjects(ctx, cs, plan, func(string) {}); err != nil {
		t.Fatal(err)
	}
	live, err := cs.NetworkingV1().NetworkPolicies(obj.Namespace).Get(ctx, obj.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if live.Spec.PodSelector.MatchLabels["app"] != "kafka" || len(live.Spec.Ingress) != 2 {
		t.Fatalf("missing Kafka restrictions: %+v", live.Spec)
	}
	if action, err := objectAction(ctx, cs, obj); err != nil || action != ObjectActionSkipped {
		t.Fatalf("unchanged policy: action=%q err=%v", action, err)
	}
	// Simulate an older, broad ingress rule. Updating must correct the spec
	// while preserving site-owned metadata and unrelated policies.
	live.Labels = map[string]string{"site": "keep"}
	live.Annotations = map[string]string{"note": "keep"}
	live.Spec.Ingress = []networkingv1.NetworkPolicyIngressRule{{}}
	if _, err := cs.NetworkingV1().NetworkPolicies(obj.Namespace).Update(ctx, live, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := refineObjectPlan(ctx, cs, plan); err != nil {
		t.Fatal(err)
	}
	if plan.PlatformObjects[0].Action != ObjectActionUpdate {
		t.Fatalf("broad ingress not corrected: %+v", plan.PlatformObjects)
	}
	if _, err := applyPlatformObjects(ctx, cs, plan, func(string) {}); err != nil {
		t.Fatal(err)
	}
	live, _ = cs.NetworkingV1().NetworkPolicies(obj.Namespace).Get(ctx, obj.Name, metav1.GetOptions{})
	if live.Labels["site"] != "keep" || live.Annotations["note"] != "keep" || len(live.Spec.Ingress) != 2 {
		t.Fatalf("incorrect policy update: %+v", live)
	}
	unrelated, err := cs.NetworkingV1().NetworkPolicies("team-a").Get(ctx, "tenant-policy", metav1.GetOptions{})
	if err != nil || unrelated.Labels["owner"] != "tenant" || len(unrelated.Spec.Ingress) != 0 {
		t.Fatalf("unrelated policy changed: %+v err=%v", unrelated, err)
	}
}

func TestNetworkPolicyReadFailureStopsReleasePlanning(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("get", "networkpolicies", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("policy read forbidden")
	})
	plan := &Plan{allObjects: []platformrelease.PlatformObject{kafkaPolicy(t)}}
	if err := refineObjectPlan(context.Background(), cs, plan); err == nil {
		t.Fatal("cannot plan a release without reading its network restrictions")
	}
	for _, action := range cs.Actions() {
		if action.GetVerb() != "get" {
			t.Fatalf("mutation after read failure: %+v", action)
		}
	}
}
