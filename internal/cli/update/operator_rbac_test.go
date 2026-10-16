package update

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"mcp-runtime/internal/platformrelease"
)

func operatorManifest(t *testing.T) *platformrelease.Manifest {
	t.Helper()
	wiring, err := platformrelease.LoadWiring("../../../config/release/wiring.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, key := range wiring.PlatformObjects {
		if strings.Contains(key, "mcp-runtime-operator-") {
			keys = append(keys, key)
		}
	}
	bundle, err := platformrelease.BundlePlatformObjects("../../../k8s", keys, "../../../config/rbac")
	if err != nil {
		t.Fatal(err)
	}
	m := manifest(t, "v0.8.0")
	if err := m.ApplyReleaseExtras(platformrelease.ReleaseExtras{PlatformObjects: bundle}); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestOperatorRBACReleaseCorrectsDriftBeforeRollout(t *testing.T) {
	cs := fake.NewClientset(append(installed("v0.7.2"), &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "mcp-runtime-operator-role"}, Rules: []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"list"}}}})...)
	m := operatorManifest(t)
	plan := planAndRefine(t, cs, m)
	sawRole := false
	for _, obj := range plan.PlatformObjects {
		if obj.Key == "ClusterRole/mcp-runtime-operator-role" && obj.Action == ObjectActionUpdate {
			sawRole = true
		}
	}
	if !sawRole {
		t.Fatal("missing RBAC drift preview")
	}
	if !plan.NeedsApply() {
		t.Fatal("RBAC-only drift must make the update actionable")
	}
	preview, err := json.Marshal(plan)
	if err != nil || !strings.Contains(string(preview), "ClusterRole/mcp-runtime-operator-role") {
		t.Fatalf("JSON preview: %s %v", preview, err)
	}
	for _, a := range cs.Actions() {
		if a.Matches("patch", "clusterroles") {
			t.Fatal("planning mutated RBAC")
		}
	}
	cs.ClearActions()
	result := Apply(context.Background(), cs, plan, ApplyOptions{Timeout: time.Second, Waiter: noopWaiter})
	if result.Failed {
		t.Fatalf("apply failed: %+v", result)
	}
	sawRBAC := false
	for _, action := range cs.Actions() {
		if action.Matches("patch", "clusterroles") {
			patch := action.(k8stesting.PatchAction)
			if patch.GetName() == "mcp-runtime-operator-role" {
				if patch.GetPatchType() != types.ApplyPatchType {
					t.Fatal("operator RBAC must use SSA")
				}
				opts := action.(interface{ GetPatchOptions() metav1.PatchOptions }).GetPatchOptions()
				if opts.FieldManager != "mcp-runtime" || opts.Force == nil || !*opts.Force {
					t.Fatalf("SSA options: %+v", opts)
				}
				sawRBAC = true
			}
		}
		if action.Matches("patch", "deployments") && !sawRBAC {
			t.Fatal("rolled image before RBAC apply")
		}
	}
	second := planAndRefine(t, cs, m)
	if len(second.objects) != 0 {
		t.Fatalf("repeat update must converge, objects: %+v", second.PlatformObjects)
	}
	role, err := cs.RbacV1().ClusterRoles().Get(context.Background(), "mcp-runtime-operator-role", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, rule := range role.Rules {
		for _, resource := range rule.Resources {
			if resource == "endpointslices" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("target-release EndpointSlice permission was not restored")
	}
}

func TestOperatorRBACFailureStopsRollouts(t *testing.T) {
	cs := fake.NewClientset(installed("v0.7.2")...)
	cs.PrependReactor("patch", "clusterroles", func(a k8stesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("RBAC denied") })
	plan := planAndRefine(t, cs, operatorManifest(t))
	cs.ClearActions()
	result := Apply(context.Background(), cs, plan, ApplyOptions{Timeout: time.Second, Waiter: noopWaiter})
	if !result.Failed {
		t.Fatal("expected failed RBAC apply")
	}
	for _, a := range cs.Actions() {
		if a.Matches("patch", "deployments") {
			t.Fatal("RBAC failure rolled an image")
		}
	}
}

func TestOperatorRBACScopeAndOptionalAuth(t *testing.T) {
	for _, auth := range []bool{false, true} {
		cs := fake.NewClientset(installed("v0.7.2")...)
		if !auth {
			c, _ := platformrelease.Lookup("mcp-auth")
			if err := cs.AppsV1().Deployments(c.Namespace).Delete(context.Background(), c.Deployment, metav1.DeleteOptions{}); err != nil {
				t.Fatal(err)
			}
		}

		m := operatorManifest(t)
		plan := planAndRefine(t, cs, m)
		count := 0
		for _, obj := range plan.objects {
			if obj.Name == "mcp-runtime-operator-bundled-auth" {
				count++
			}
		}
		if (auth && count != 2) || (!auth && count != 0) {
			t.Fatalf("auth=%v planned optional objects=%d", auth, count)
		}
		scoped, err := BuildPlan(context.Background(), cs, m, Selection{Only: []string{"operator"}})
		if err != nil {
			t.Fatal(err)
		}
		if len(scoped.allObjects) != 0 || !strings.Contains(strings.Join(scoped.Warnings, " "), "--only") {
			t.Fatal("scoped update must explicitly skip RBAC")
		}
	}
}
