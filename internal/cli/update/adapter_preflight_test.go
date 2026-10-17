package update

import (
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"mcp-runtime/internal/platformrelease"
)

func TestAdapterPreflightReleaseAppliesReadOnlyPermissionsBeforeRollout(t *testing.T) {
	wiring, err := platformrelease.LoadWiring("../../../config/release/wiring.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, key := range wiring.PlatformObjects {
		if strings.Contains(key, "mcp-runtime-api-adapter-") {
			keys = append(keys, key)
		}
	}
	if len(keys) != 4 {
		t.Fatalf("enrollment preflight release objects=%v", keys)
	}
	bundle, err := platformrelease.BundlePlatformObjects("../../../k8s", keys)
	if err != nil {
		t.Fatal(err)
	}
	objects, err := platformrelease.ParsePlatformObjects(bundle)
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range objects {
		rules, _ := object.Object["rules"].([]any)
		for _, raw := range rules {
			rule := raw.(map[string]any)
			for _, verb := range rule["verbs"].([]any) {
				if verb != "get" {
					t.Fatalf("preflight grants write permission: %s", object.Name)
				}
			}
			for _, resource := range rule["resources"].([]any) {
				if resource == "secrets" {
					t.Fatal("preflight must not read certificate private keys")
				}
			}
		}
	}
	m := manifest(t, "v0.8.0")
	if err := m.ApplyReleaseExtras(platformrelease.ReleaseExtras{PlatformObjects: bundle}); err != nil {
		t.Fatal(err)
	}
	cs := fake.NewClientset(installed("v0.7.2")...)
	plan := planAndRefine(t, cs, m)
	result := Apply(context.Background(), cs, plan, ApplyOptions{Timeout: time.Second, Waiter: noopWaiter})
	if result.Failed {
		t.Fatalf("apply=%+v", result)
	}
	role, err := cs.RbacV1().ClusterRoles().Get(t.Context(), "mcp-runtime-api-adapter-preflight", metav1.GetOptions{})
	if err != nil || len(role.Rules) != 2 {
		t.Fatalf("preflight role=%+v err=%v", role, err)
	}
	operator, err := cs.RbacV1().Roles("mcp-runtime").Get(t.Context(), "mcp-runtime-api-adapter-operator", metav1.GetOptions{})
	if err != nil || len(operator.Rules[0].ResourceNames) != 1 || operator.Rules[0].ResourceNames[0] != "mcp-runtime-operator-controller-manager" {
		t.Fatalf("operator role=%+v err=%v", operator, err)
	}
	for _, action := range cs.Actions() {
		if action.Matches("patch", "deployments") {
			break
		}
		if action.Matches("patch", "clusterroles") || action.Matches("create", "clusterroles") {
			return
		}
	}
	t.Fatal("preflight RBAC did not precede image rollout")
}
