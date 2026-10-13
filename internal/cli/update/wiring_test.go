package update

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"mcp-runtime/internal/platformrelease"
)

var ingestObjectKeys = []string{
	"ServiceAccount/mcp-observability/mcp-ingest",
	"ClusterRole/mcp-ingest-tokenreview",
	"ClusterRoleBinding/mcp-ingest-tokenreview",
}

// wiredManifest is the release manifest for version carrying the real ingest
// platform objects and wiring from the repository.
func wiredManifest(t *testing.T, version string) *platformrelease.Manifest {
	t.Helper()
	m := manifest(t, version)
	bundle, err := platformrelease.BundlePlatformObjects("../../../k8s", ingestObjectKeys)
	if err != nil {
		t.Fatal(err)
	}
	wiring, err := platformrelease.LoadWiring("../../../config/release/wiring.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.ApplyReleaseExtras(bundle, wiring.Components); err != nil {
		t.Fatal(err)
	}
	return m
}

func ingestDeployment(t *testing.T, cs *fake.Clientset) *appsv1.Deployment {
	t.Helper()
	d, err := cs.AppsV1().Deployments("mcp-observability").Get(context.Background(), "mcp-ingest", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func envOf(d *appsv1.Deployment, name string) (corev1.EnvVar, bool) {
	for _, e := range d.Spec.Template.Spec.Containers[0].Env {
		if e.Name == name {
			return e, true
		}
	}
	return corev1.EnvVar{}, false
}

// installedWithIngestEnv is installed(version) with a custom env var on ingest
// that update must preserve.
func installedWithIngestEnv(version string) []runtime.Object {
	objs := installed(version)
	for _, o := range objs {
		if d, ok := o.(*appsv1.Deployment); ok && d.Name == "mcp-ingest" {
			d.Spec.Template.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "KEEP", Value: "me"}}
		}
	}
	return objs
}

func TestPlanDetectsWiringDriftAndMissingObjects(t *testing.T) {
	cs := fake.NewSimpleClientset(installedWithIngestEnv("v0.7.1")...)
	m := wiredManifest(t, "v0.7.2")
	plan, err := BuildPlan(context.Background(), cs, m, Selection{})
	if err != nil {
		t.Fatal(err)
	}
	if err := refineObjectPlan(context.Background(), cs, plan); err != nil {
		t.Fatal(err)
	}
	row := rowFor(t, plan, "ingest")
	if row.Action != ActionUpdate {
		t.Fatalf("ingest action = %s, want update", row.Action)
	}
	joined := strings.Join(row.Wiring, "; ")
	for _, want := range []string{`serviceAccountName "" -> "mcp-ingest"`, "automountServiceAccountToken -> true", "INGEST_SA_TOKEN_AUDIENCE added"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("wiring %q missing %q", joined, want)
		}
	}
	if len(plan.objects) != 3 || !plan.NeedsApply() {
		t.Fatalf("objects = %d, want 3 to create", len(plan.objects))
	}
	for _, o := range plan.PlatformObjects {
		if o.Action != ObjectActionCreate {
			t.Fatalf("%s action = %s, want create", o.Key, o.Action)
		}
	}
}

func TestApplyCreatesObjectsBeforePatchingWiringAndPreservesEnv(t *testing.T) {
	cs := fake.NewSimpleClientset(installedWithIngestEnv("v0.7.1")...)
	m := wiredManifest(t, "v0.7.2")
	plan, err := BuildPlan(context.Background(), cs, m, Selection{})
	if err != nil {
		t.Fatal(err)
	}
	if err := refineObjectPlan(context.Background(), cs, plan); err != nil {
		t.Fatal(err)
	}
	cs.ClearActions()
	res := Apply(context.Background(), cs, plan, ApplyOptions{Timeout: time.Second, RollbackOnFailure: true, Waiter: noopWaiter})
	if res.Failed {
		t.Fatalf("apply failed: %+v", res)
	}

	// Objects exist.
	if _, err := cs.CoreV1().ServiceAccounts("mcp-observability").Get(context.Background(), "mcp-ingest", metav1.GetOptions{}); err != nil {
		t.Fatalf("ServiceAccount not created: %v", err)
	}
	if _, err := cs.RbacV1().ClusterRoles().Get(context.Background(), "mcp-ingest-tokenreview", metav1.GetOptions{}); err != nil {
		t.Fatalf("ClusterRole not created: %v", err)
	}
	crb, err := cs.RbacV1().ClusterRoleBindings().Get(context.Background(), "mcp-ingest-tokenreview", metav1.GetOptions{})
	if err != nil || len(crb.Subjects) != 1 || crb.Subjects[0].Name != "mcp-ingest" {
		t.Fatalf("ClusterRoleBinding wrong: %v %+v", err, crb)
	}

	// Deployment wired, other env kept, image rolled.
	d := ingestDeployment(t, cs)
	if d.Spec.Template.Spec.ServiceAccountName != "mcp-ingest" {
		t.Fatalf("serviceAccountName = %q", d.Spec.Template.Spec.ServiceAccountName)
	}
	if a := d.Spec.Template.Spec.AutomountServiceAccountToken; a == nil || !*a {
		t.Fatalf("automountServiceAccountToken = %v, want true", a)
	}
	if e, ok := envOf(d, "INGEST_SA_TOKEN_AUDIENCE"); !ok || e.Value != "mcp-ingest" {
		t.Fatalf("audience env = %+v", e)
	}
	if e, ok := envOf(d, "KEEP"); !ok || e.Value != "me" {
		t.Fatalf("existing env was not preserved: %+v", e)
	}
	if !strings.Contains(d.Spec.Template.Spec.Containers[0].Image, "v0.7.2") {
		t.Fatalf("image = %s", d.Spec.Template.Spec.Containers[0].Image)
	}

	// Ordering: every RBAC create precedes the first Deployment patch.
	lastObject, firstPatch := -1, -1
	for i, a := range cs.Actions() {
		switch {
		case a.Matches("create", "serviceaccounts"), a.Matches("create", "clusterroles"), a.Matches("create", "clusterrolebindings"):
			lastObject = i
		case a.Matches("patch", "deployments") && firstPatch < 0:
			firstPatch = i
		}
	}
	if lastObject < 0 || firstPatch < 0 || lastObject > firstPatch {
		t.Fatalf("objects must be created before the first Deployment patch (objects@%d patch@%d)", lastObject, firstPatch)
	}
}

func TestSecondRunConvergesToNoop(t *testing.T) {
	cs := fake.NewSimpleClientset(installedWithIngestEnv("v0.7.1")...)
	m := wiredManifest(t, "v0.7.2")
	for i := 0; i < 2; i++ {
		plan, err := BuildPlan(context.Background(), cs, m, Selection{})
		if err != nil {
			t.Fatal(err)
		}
		if err := refineObjectPlan(context.Background(), cs, plan); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if res := Apply(context.Background(), cs, plan, ApplyOptions{Timeout: time.Second, Waiter: noopWaiter}); res.Failed {
				t.Fatalf("apply failed: %+v", res)
			}
			continue
		}
		if plan.NeedsApply() && len(plan.Changed()) > 1 { // only unrelated components may still differ
			t.Fatalf("second plan still wants changes: %+v", plan.Changed())
		}
		if len(plan.objects) != 0 {
			t.Fatalf("objects should all be skipped, got %d to apply", len(plan.objects))
		}
		if row := rowFor(t, plan, "ingest"); row.Action != ActionUnchanged || len(row.Wiring) != 0 {
			t.Fatalf("ingest should be unchanged after apply, got %s %v", row.Action, row.Wiring)
		}
	}
}

func TestWiringOnlyUpdateWhenImageAlreadyCurrent(t *testing.T) {
	cs := fake.NewSimpleClientset(installedWithIngestEnv("v0.7.2")...)
	m := wiredManifest(t, "v0.7.2")
	plan, err := BuildPlan(context.Background(), cs, m, Selection{})
	if err != nil {
		t.Fatal(err)
	}
	row := rowFor(t, plan, "ingest")
	if row.Action != ActionUpdate || !strings.Contains(row.Reason, "wiring") {
		t.Fatalf("action=%s reason=%q, want a wiring-only update", row.Action, row.Reason)
	}
	if row.TargetImage != row.CurrentImage {
		t.Fatalf("image should not change: %s -> %s", row.CurrentImage, row.TargetImage)
	}
}

func TestOnlyScopeBlocksWiringThatNeedsMissingServiceAccount(t *testing.T) {
	m := wiredManifest(t, "v0.7.2")

	cs := fake.NewSimpleClientset(installedWithIngestEnv("v0.7.1")...)
	plan, err := BuildPlan(context.Background(), cs, m, Selection{Only: []string{"ingest"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.allObjects) != 0 {
		t.Fatal("--only must skip platform objects")
	}
	if err := refineObjectPlan(context.Background(), cs, plan); err != nil {
		t.Fatal(err)
	}
	if row := rowFor(t, plan, "ingest"); row.Action != ActionBlocked || !strings.Contains(row.Reason, "re-run without --only") {
		t.Fatalf("ingest = %s %q, want blocked", row.Action, row.Reason)
	}

	// With the ServiceAccount already present the scoped update proceeds.
	objs := append(installedWithIngestEnv("v0.7.1"), &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "mcp-ingest", Namespace: "mcp-observability"}})
	cs = fake.NewSimpleClientset(objs...)
	plan, err = BuildPlan(context.Background(), cs, m, Selection{Only: []string{"ingest"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := refineObjectPlan(context.Background(), cs, plan); err != nil {
		t.Fatal(err)
	}
	if row := rowFor(t, plan, "ingest"); row.Action != ActionUpdate {
		t.Fatalf("ingest = %s, want update", row.Action)
	}
}

func TestRollbackRestoresPreviousWiring(t *testing.T) {
	cs := fake.NewSimpleClientset(installedWithIngestEnv("v0.7.1")...)
	m := wiredManifest(t, "v0.7.2")
	plan, err := BuildPlan(context.Background(), cs, m, Selection{})
	if err != nil {
		t.Fatal(err)
	}
	if err := refineObjectPlan(context.Background(), cs, plan); err != nil {
		t.Fatal(err)
	}
	calls := 0
	waiter := func(_ context.Context, ns, name string, _ time.Duration) error {
		if name == "mcp-ingest" {
			calls++
			if calls == 1 {
				return errors.New("rollout stuck")
			}
		}
		return nil
	}
	res := Apply(context.Background(), cs, plan, ApplyOptions{Timeout: time.Second, RollbackOnFailure: true, Waiter: waiter})
	if !res.Failed {
		t.Fatal("expected the stuck rollout to fail the update")
	}
	d := ingestDeployment(t, cs)
	if sa := d.Spec.Template.Spec.ServiceAccountName; sa != "" {
		t.Fatalf("serviceAccountName = %q after rollback, want restored empty", sa)
	}
	if d.Spec.Template.Spec.AutomountServiceAccountToken != nil {
		t.Fatalf("automountServiceAccountToken = %v after rollback, want unset", *d.Spec.Template.Spec.AutomountServiceAccountToken)
	}
	if _, ok := envOf(d, "INGEST_SA_TOKEN_AUDIENCE"); ok {
		t.Fatal("added env var should be removed on rollback")
	}
	if e, ok := envOf(d, "KEEP"); !ok || e.Value != "me" {
		t.Fatalf("existing env lost during rollback: %+v", e)
	}
	if !strings.Contains(d.Spec.Template.Spec.Containers[0].Image, "v0.7.1") {
		t.Fatalf("image = %s, want restored v0.7.1", d.Spec.Template.Spec.Containers[0].Image)
	}
}

func TestObjectCorrectionUpdatesDriftedRole(t *testing.T) {
	m := wiredManifest(t, "v0.7.2")
	cs := fake.NewSimpleClientset(installedWithIngestEnv("v0.7.1")...)
	plan, err := BuildPlan(context.Background(), cs, m, Selection{})
	if err != nil {
		t.Fatal(err)
	}
	if err := refineObjectPlan(context.Background(), cs, plan); err != nil {
		t.Fatal(err)
	}
	if res := Apply(context.Background(), cs, plan, ApplyOptions{Timeout: time.Second, Waiter: noopWaiter}); res.Failed {
		t.Fatalf("apply failed: %+v", res)
	}

	// Someone widens the role; the next update must restore the release's rules.
	role, _ := cs.RbacV1().ClusterRoles().Get(context.Background(), "mcp-ingest-tokenreview", metav1.GetOptions{})
	role.Rules[0].Verbs = []string{"create", "delete"}
	if _, err := cs.RbacV1().ClusterRoles().Update(context.Background(), role, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	plan, err = BuildPlan(context.Background(), cs, m, Selection{})
	if err != nil {
		t.Fatal(err)
	}
	if err := refineObjectPlan(context.Background(), cs, plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.objects) != 1 || plan.objects[0].Kind != "ClusterRole" {
		t.Fatalf("expected only the drifted ClusterRole, got %+v", plan.PlatformObjects)
	}
	if res := Apply(context.Background(), cs, plan, ApplyOptions{Timeout: time.Second, Waiter: noopWaiter}); res.Failed {
		t.Fatalf("apply failed: %+v", res)
	}
	role, _ = cs.RbacV1().ClusterRoles().Get(context.Background(), "mcp-ingest-tokenreview", metav1.GetOptions{})
	if len(role.Rules[0].Verbs) != 1 || role.Rules[0].Verbs[0] != "create" {
		t.Fatalf("rules not restored: %+v", role.Rules)
	}
}

func TestApplyStopsBeforeRolloutsWhenObjectApplyFails(t *testing.T) {
	cs := fake.NewSimpleClientset(installedWithIngestEnv("v0.7.1")...)
	cs.PrependReactor("create", "clusterroles", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden")
	})
	m := wiredManifest(t, "v0.7.2")
	plan, err := BuildPlan(context.Background(), cs, m, Selection{})
	if err != nil {
		t.Fatal(err)
	}
	if err := refineObjectPlan(context.Background(), cs, plan); err != nil {
		t.Fatal(err)
	}
	cs.ClearActions()
	res := Apply(context.Background(), cs, plan, ApplyOptions{Timeout: time.Second, Waiter: noopWaiter})
	if !res.Failed {
		t.Fatal("expected failure")
	}
	for _, a := range cs.Actions() {
		if a.Matches("patch", "deployments") {
			t.Fatal("no Deployment may be patched when the platform objects could not be applied")
		}
	}
}
