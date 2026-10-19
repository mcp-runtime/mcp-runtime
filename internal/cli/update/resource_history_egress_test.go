package update

import (
	"context"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"mcp-runtime/internal/platformrelease"
	"mcp-runtime/pkg/resourcehistory"
)

func resourceHistoryPolicy(t *testing.T) platformrelease.PlatformObject {
	t.Helper()
	bundle, err := platformrelease.BundlePlatformObjects("../../../k8s", []string{"NetworkPolicy/mcp-platform/mcp-runtime-api-resource-history-egress"})
	if err != nil {
		t.Fatal(err)
	}
	objects, err := platformrelease.ParsePlatformObjects(bundle)
	if err != nil || len(objects) != 1 {
		t.Fatalf("policy bundle: %v (%d objects)", err, len(objects))
	}
	return objects[0]
}

func kubeletRule(cidr string) networkingv1.NetworkPolicyEgressRule {
	protocol, port := corev1.ProtocolTCP, intstr.FromInt(10250)
	return networkingv1.NetworkPolicyEgressRule{
		To:    []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: cidr}}},
		Ports: []networkingv1.NetworkPolicyPort{{Protocol: &protocol, Port: &port}},
	}
}

func resourceHistoryFixture(t *testing.T) (*fake.Clientset, platformrelease.PlatformObject, *networkingv1.NetworkPolicy) {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(server.Close)
	certificate := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	var targets []resourcehistory.Target
	var objects []runtime.Object
	for _, item := range []struct{ name, trustedUID, liveUID, trustedIP, liveIP string }{
		{"current-v4", "uid-v4", "uid-v4", "172.18.0.2", "172.18.0.2"},
		{"current-v6", "uid-v6", "uid-v6", "fd00::2", "fd00::2"},
		{"replaced-uid", "old", "new", "172.18.0.3", "172.18.0.3"},
		{"replaced-ip", "uid-ip", "uid-ip", "172.18.0.4", "172.18.0.5"},
	} {
		targets = append(targets, resourcehistory.Target{Name: item.name, UID: item.trustedUID, Address: item.trustedIP, ServerName: item.name, CertificatePEM: certificate})
		objects = append(objects, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: item.name, UID: types.UID(item.liveUID)}, Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: item.liveIP}}}})
	}
	data, err := resourcehistory.EncodeTargets(targets)
	if err != nil {
		t.Fatal(err)
	}
	objects = append(objects, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: resourcehistory.TrustConfigMap, Namespace: "mcp-platform"}, Data: map[string]string{"targets.json": data}})
	objects = append(objects, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "untrusted", UID: "untrusted"}, Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "192.0.2.9"}}}})
	obj := resourceHistoryPolicy(t)
	live := &networkingv1.NetworkPolicy{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, live); err != nil {
		t.Fatal(err)
	}
	// The precise policy left by supported setup, rather than the static bundle.
	live.Spec.Egress = []networkingv1.NetworkPolicyEgressRule{kubeletRule("172.18.0.2/32"), kubeletRule("fd00::2/128")}
	objects = append(objects, live)
	return fake.NewSimpleClientset(objects...), obj, live
}

func TestResourceHistoryUpdateTrustFailuresAreReadOnly(t *testing.T) {
	for _, failure := range []string{"invalid-trust", "trust-forbidden", "nodes-forbidden", "too-many-nodes", "truncated-nodes"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			cs, obj, _ := resourceHistoryFixture(t)
			switch failure {
			case "invalid-trust":
				cm, _ := cs.CoreV1().ConfigMaps(obj.Namespace).Get(ctx, resourcehistory.TrustConfigMap, metav1.GetOptions{})
				cm.Data["targets.json"] = "invalid"
				if _, err := cs.CoreV1().ConfigMaps(obj.Namespace).Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			case "trust-forbidden":
				cs.PrependReactor("get", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("trust forbidden") })
			case "nodes-forbidden":
				cs.PrependReactor("list", "nodes", func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("nodes forbidden") })
			default:
				nodes := &corev1.NodeList{}
				if failure == "too-many-nodes" {
					nodes.Items = make([]corev1.Node, 65)
				} else {
					nodes.Continue = "more-nodes"
				}
				cs.PrependReactor("list", "nodes", func(k8stesting.Action) (bool, runtime.Object, error) { return true, nodes, nil })
			}
			cs.ClearActions()
			if err := refineObjectPlan(ctx, cs, &Plan{allObjects: []platformrelease.PlatformObject{obj}}); err == nil {
				t.Fatal("unverifiable trust accepted")
			}
			for _, action := range cs.Actions() {
				if action.GetVerb() != "get" && action.GetVerb() != "list" {
					t.Fatalf("failed planning mutated cluster: %v", action)
				}
			}
		})
	}
}

func TestResourceHistoryUpdateCreatesPolicyFromCurrentTrust(t *testing.T) {
	ctx := context.Background()
	cs, obj, expected := resourceHistoryFixture(t)
	if err := cs.NetworkingV1().NetworkPolicies(obj.Namespace).Delete(ctx, obj.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	plan := &Plan{allObjects: []platformrelease.PlatformObject{obj}}
	if err := refineObjectPlan(ctx, cs, plan); err != nil || plan.PlatformObjects[0].Action != ObjectActionCreate {
		t.Fatalf("missing policy: %+v, %v", plan, err)
	}
	if _, err := applyPlatformObjects(ctx, cs, plan, func(string) {}); err != nil {
		t.Fatal(err)
	}
	live, err := cs.NetworkingV1().NetworkPolicies(obj.Namespace).Get(ctx, obj.Name, metav1.GetOptions{})
	if err != nil || !reflect.DeepEqual(live.Spec, expected.Spec) {
		t.Fatalf("created policy differs from trusted destinations: %+v, %v", live, err)
	}
}

func TestResourceHistoryUpdateRetainsOnlyCurrentTrustedNodeEgress(t *testing.T) {
	ctx := context.Background()
	cs, obj, setupPolicy := resourceHistoryFixture(t)
	original := runtime.DeepCopyJSON(obj.Object)
	plan := &Plan{allObjects: []platformrelease.PlatformObject{obj}}
	for range 2 {
		cs.ClearActions()
		if err := refineObjectPlan(ctx, cs, plan); err != nil {
			t.Fatal(err)
		}
		if plan.PlatformObjects[0].Action != ObjectActionSkipped {
			t.Fatalf("setup's exact trusted-node egress would be overwritten: %+v", plan.PlatformObjects)
		}
		for _, action := range cs.Actions() {
			if action.GetVerb() != "get" && action.GetVerb() != "list" {
				t.Fatalf("planning mutated cluster: %v", action)
			}
		}
		if _, err := applyPlatformObjects(ctx, cs, plan, func(string) {}); err != nil {
			t.Fatal(err)
		}
	}
	// Arbitrary live destinations must be removed, never preserved as trust.
	broad := setupPolicy.DeepCopy()
	broad.Spec.Egress = []networkingv1.NetworkPolicyEgressRule{kubeletRule("0.0.0.0/0"), kubeletRule("192.0.2.9/32")}
	if _, err := cs.NetworkingV1().NetworkPolicies(obj.Namespace).Update(ctx, broad, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := refineObjectPlan(ctx, cs, plan); err != nil {
		t.Fatal(err)
	}
	if _, err := applyPlatformObjects(ctx, cs, plan, func(string) {}); err != nil {
		t.Fatal(err)
	}
	live, err := cs.NetworkingV1().NetworkPolicies(obj.Namespace).Get(ctx, obj.Name, metav1.GetOptions{})
	if err != nil || !reflect.DeepEqual(live.Spec, setupPolicy.Spec) {
		t.Fatalf("update did not restore only current trusted destinations: %+v, %v", live, err)
	}
	if !reflect.DeepEqual(obj.Object, original) {
		t.Fatal("discovery changed the shared release template")
	}
	// Trust disappearance after planning must revoke all kubelet destinations.
	if _, err := cs.NetworkingV1().NetworkPolicies(obj.Namespace).Update(ctx, broad, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := refineObjectPlan(ctx, cs, plan); err != nil {
		t.Fatal(err)
	}
	if err := cs.CoreV1().ConfigMaps(obj.Namespace).Delete(ctx, resourcehistory.TrustConfigMap, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := applyPlatformObjects(ctx, cs, plan, func(string) {}); err != nil {
		t.Fatal(err)
	}
	live, err = cs.NetworkingV1().NetworkPolicies(obj.Namespace).Get(ctx, obj.Name, metav1.GetOptions{})
	if err != nil || len(live.Spec.Egress) != 0 {
		t.Fatalf("missing trust retained permissions: %+v, %v", live, err)
	}
}
