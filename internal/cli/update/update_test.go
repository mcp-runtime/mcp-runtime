package update

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"mcp-runtime/internal/platformrelease"
)

const reg = "registry.registry.svc.cluster.local:5000"

var digestA = "sha256:" + strings.Repeat("a", 64)
var digestB = "sha256:" + strings.Repeat("b", 64)

func deployment(ns, name, container, image string, annotations map[string]string, env ...corev1.EnvVar) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Annotations: annotations, Generation: 1},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: container, Image: image, Env: env}}},
			},
		},
	}
}

func readyPod(ns, app, container, imageID string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: app + "-pod", Namespace: ns, Labels: map[string]string{"app": app}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name: container, Ready: true, ImageID: imageID,
		}}},
	}
}

// installed returns objects for a v0.4.0 test install of the default components.
func installed(version string) []runtime.Object {
	platform := func(name, container, repo string) *appsv1.Deployment {
		ns := "mcp-platform"
		switch name {
		case "mcp-analytics-api", "mcp-ingest", "mcp-processor":
			ns = "mcp-observability"
		}
		return deployment(ns, name, container, fmt.Sprintf("%s/%s:%s", reg, repo, version), nil)
	}
	return []runtime.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mcp-runtime"}},
		deployment("mcp-runtime", platformrelease.OperatorDeployment, "manager", reg+"/mcp-runtime-operator:"+version, nil,
			corev1.EnvVar{Name: "MCP_GATEWAY_PROXY_IMAGE", Value: reg + "/mcp-gateway:" + version},
			corev1.EnvVar{Name: "OTHER", Value: "keep"}),
		platform("mcp-platform-api", "platform-api", "mcp-platform-api"),
		platform("mcp-runtime-api", "runtime-api", "mcp-runtime-api"),
		platform("mcp-analytics-api", "analytics-api", "mcp-analytics-api"),
		platform("mcp-ingest", "ingest", "mcp-ingest"),
		platform("mcp-processor", "processor", "mcp-processor"),
		platform("mcp-ui", "ui", "mcp-ui"),
		deployment("mcp-platform", "mcp-auth-server", "auth-server", "docker.io/princekrroshan01/mcp-auth-server:v1.0.0", nil),
	}
}

func manifest(t *testing.T, version string, mutate ...func(*platformrelease.Manifest)) *platformrelease.Manifest {
	t.Helper()
	m, err := platformrelease.GenerateManifest(version, false, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range mutate {
		f(m)
	}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	return m
}

func setComponent(name, repo, tag, digest string) func(*platformrelease.Manifest) {
	return func(m *platformrelease.Manifest) {
		for i := range m.Components {
			if m.Components[i].Name == name {
				m.Components[i] = platformrelease.ManifestComponent{Name: name, Repository: repo, Tag: tag, Digest: digest}
				return
			}
		}
		m.Components = append(m.Components, platformrelease.ManifestComponent{Name: name, Repository: repo, Tag: tag, Digest: digest})
	}
}

func rowFor(t *testing.T, p *Plan, name string) Row {
	t.Helper()
	for _, r := range p.Rows {
		if r.Component == name {
			return r
		}
	}
	t.Fatalf("no row for %s", name)
	return Row{}
}

func patchedDeployments(cs *fake.Clientset) []string {
	var out []string
	for _, a := range cs.Actions() {
		if a.GetVerb() == "patch" {
			out = append(out, a.(k8stesting.PatchAction).GetName())
		}
	}
	return out
}

// assertOnlyDeploymentWrites is the preserved-resource guard: update may only
// read namespaces/deployments/pods and patch deployments.
func assertOnlyDeploymentWrites(t *testing.T, cs *fake.Clientset) {
	t.Helper()
	for _, a := range cs.Actions() {
		res := a.GetResource().Resource
		switch a.GetVerb() {
		case "get", "list":
			if res != "namespaces" && res != "deployments" && res != "pods" {
				t.Errorf("unexpected read of %s", res)
			}
		case "patch":
			if res != "deployments" {
				t.Errorf("unexpected patch of %s", res)
			}
		default:
			t.Errorf("unexpected %s on %s", a.GetVerb(), res)
		}
	}
}

func noopWaiter(context.Context, string, string, time.Duration) error { return nil }

func TestPlanUpToDateIsNoop(t *testing.T) {
	cs := fake.NewSimpleClientset(installed("v0.4.0")...)
	plan, err := BuildPlan(context.Background(), cs, manifest(t, "v0.4.0"), Selection{})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(plan.Changed()); n != 0 {
		t.Fatalf("expected no changes, got %d: %+v", n, plan.Changed())
	}
	if r := rowFor(t, plan, "ui"); r.Action != ActionUnchanged {
		t.Fatalf("ui = %+v", r)
	}
	if r := rowFor(t, plan, "mcp-auth"); r.Action != ActionSkip || !strings.Contains(r.Reason, "--include-auth") {
		t.Fatalf("mcp-auth = %+v", r)
	}
	if r := rowFor(t, plan, "doctor-smoke"); r.Action != ActionSkip {
		t.Fatalf("doctor-smoke = %+v", r)
	}
	if plan.InstalledVersion != "v0.4.0" {
		t.Fatalf("installed version = %q", plan.InstalledVersion)
	}
	res := Apply(context.Background(), cs, plan, ApplyOptions{Timeout: time.Second, Waiter: noopWaiter})
	if res.Failed || len(res.Workloads) != 0 || len(patchedDeployments(cs)) != 0 {
		t.Fatalf("no-op apply patched: %v", patchedDeployments(cs))
	}
	assertOnlyDeploymentWrites(t, cs)
}

func TestPlanDetectsTagChangeAndRelativeRegistry(t *testing.T) {
	cs := fake.NewSimpleClientset(installed("v0.4.0")...)
	plan, err := BuildPlan(context.Background(), cs, manifest(t, "v0.5.0"), Selection{})
	if err != nil {
		t.Fatal(err)
	}
	ui := rowFor(t, plan, "ui")
	if ui.Action != ActionUpdate || ui.TargetImage != reg+"/mcp-ui:v0.5.0" || ui.CurrentVersion != "v0.4.0" {
		t.Fatalf("ui = %+v", ui)
	}
	gw := rowFor(t, plan, "gateway-proxy")
	if gw.Action != ActionUpdate || gw.CurrentImage != reg+"/mcp-gateway:v0.4.0" {
		t.Fatalf("gateway-proxy = %+v", gw)
	}
	if len(plan.Warnings) == 0 || !strings.Contains(strings.Join(plan.Warnings, ";"), "tenant MCP server pods") {
		t.Fatalf("expected gateway-proxy warning, got %v", plan.Warnings)
	}
}

func TestPlanDigestComparison(t *testing.T) {
	objs := installed("v0.4.0")
	objs = append(objs, readyPod("mcp-platform", "mcp-ui", "ui", reg+"/mcp-ui@"+digestA))
	cs := fake.NewSimpleClientset(objs...)

	// Running digest matches: spec gets pinned (update) so later runs are exact.
	plan, err := BuildPlan(context.Background(), cs, manifest(t, "v0.4.0", setComponent("ui", "mcp-ui", "v0.4.0", digestA)), Selection{})
	if err != nil {
		t.Fatal(err)
	}
	ui := rowFor(t, plan, "ui")
	if ui.Action != ActionUpdate || ui.CurrentDigest != digestA || ui.TargetImage != reg+"/mcp-ui:v0.4.0@"+digestA {
		t.Fatalf("pin row = %+v", ui)
	}
	if n := len(plan.Changed()); n != 1 {
		t.Fatalf("only ui should change, got %d", n)
	}

	// Different digest for the same tag: update.
	plan, _ = BuildPlan(context.Background(), cs, manifest(t, "v0.4.0", setComponent("ui", "mcp-ui", "v0.4.0", digestB)), Selection{})
	if r := rowFor(t, plan, "ui"); r.Action != ActionUpdate {
		t.Fatalf("digest change = %+v", r)
	}

	// Spec already pinned to target digest: unchanged.
	pinned := fake.NewSimpleClientset(append(installed("v0.4.0")[:7],
		deployment("mcp-platform", "mcp-ui", "ui", reg+"/mcp-ui:v0.4.0@"+digestA, nil))...)
	plan, _ = BuildPlan(context.Background(), pinned, manifest(t, "v0.4.0", setComponent("ui", "mcp-ui", "v0.4.0", digestA)), Selection{})
	if r := rowFor(t, plan, "ui"); r.Action != ActionUnchanged {
		t.Fatalf("pinned = %+v", r)
	}
	// Pinned spec with a tag-only manifest stays pinned and unchanged.
	plan, _ = BuildPlan(context.Background(), pinned, manifest(t, "v0.4.0"), Selection{})
	if r := rowFor(t, plan, "ui"); r.Action != ActionUnchanged || !strings.Contains(r.TargetImage, digestA) {
		t.Fatalf("pinned tag-only = %+v", r)
	}
}

func TestPlanSelection(t *testing.T) {
	objs := append(installed("v0.4.0"), deployment("cert-manager", "cert-manager", "cert-manager-controller", "quay.io/jetstack/cert-manager-controller:v1.14.2", nil))
	cs := fake.NewSimpleClientset(objs...)
	m := manifest(t, "v0.5.0",
		setComponent("mcp-auth", "docker.io/princekrroshan01/mcp-auth-server", "v1.1.0", ""),
		setComponent("cert-manager-controller", "quay.io/jetstack/cert-manager-controller", "v1.14.5", ""))

	plan, err := BuildPlan(context.Background(), cs, m, Selection{Only: []string{"ui"}})
	if err != nil {
		t.Fatal(err)
	}
	if changed := plan.Changed(); len(changed) != 1 || changed[0].Component != "ui" {
		t.Fatalf("--only ui changed = %+v", changed)
	}
	if r := rowFor(t, plan, "operator"); r.Action != ActionSkip || r.Reason != "not selected (--only)" {
		t.Fatalf("operator = %+v", r)
	}
	if r := rowFor(t, plan, "cert-manager-controller"); r.Action != ActionSkip {
		t.Fatalf("cert-manager excluded by default = %+v", r)
	}

	plan, _ = BuildPlan(context.Background(), cs, m, Selection{IncludeAuth: true, IncludeCertManager: true})
	if r := rowFor(t, plan, "mcp-auth"); r.Action != ActionUpdate {
		t.Fatalf("mcp-auth opt-in = %+v", r)
	}
	if r := rowFor(t, plan, "cert-manager-controller"); r.Action != ActionUpdate {
		t.Fatalf("cert-manager patch = %+v", r)
	}
	if r := rowFor(t, plan, "cert-manager-webhook"); r.Action != ActionSkip || r.Reason != "not in release manifest" {
		t.Fatalf("cert-manager-webhook = %+v", r)
	}

	// cert-manager minor bump is blocked.
	m2 := manifest(t, "v0.5.0", setComponent("cert-manager-controller", "quay.io/jetstack/cert-manager-controller", "v1.15.0", ""))
	plan, _ = BuildPlan(context.Background(), cs, m2, Selection{IncludeCertManager: true})
	if r := rowFor(t, plan, "cert-manager-controller"); r.Action != ActionBlocked {
		t.Fatalf("cert-manager minor = %+v", r)
	}

	if _, err := BuildPlan(context.Background(), cs, m, Selection{Only: []string{"traefik"}}); err == nil {
		t.Fatal("unknown --only component must fail")
	}
}

func TestPlanNotInstalledAndMissingWorkloads(t *testing.T) {
	cs := fake.NewSimpleClientset()
	if _, err := BuildPlan(context.Background(), cs, manifest(t, "v0.5.0"), Selection{}); err == nil || !strings.Contains(err.Error(), "does not look like an MCP Runtime install") {
		t.Fatalf("expected not-installed refusal, got %v", err)
	}
	// Operator only (no platform): platform rows are skipped, never created.
	cs = fake.NewSimpleClientset(installed("v0.4.0")[:2]...)
	plan, err := BuildPlan(context.Background(), cs, manifest(t, "v0.5.0"), Selection{})
	if err != nil {
		t.Fatal(err)
	}
	if r := rowFor(t, plan, "ui"); r.Action != ActionSkip || r.Reason != "not installed" {
		t.Fatalf("ui = %+v", r)
	}
}

func TestPlanDowngradeGuard(t *testing.T) {
	cs := fake.NewSimpleClientset(installed("v0.5.0")...)
	plan, err := BuildPlan(context.Background(), cs, manifest(t, "v0.4.0"), Selection{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Blocked()) == 0 || rowFor(t, plan, "ui").Action != ActionBlocked {
		t.Fatalf("downgrade not blocked: %+v", rowFor(t, plan, "ui"))
	}
	plan, _ = BuildPlan(context.Background(), cs, manifest(t, "v0.4.0"), Selection{AllowDowngrade: true})
	if len(plan.Blocked()) != 0 || rowFor(t, plan, "ui").Action != ActionUpdate {
		t.Fatalf("--allow-downgrade ignored")
	}
	// Non-semver installs (test-mode latest) are not blocked, only noted.
	cs = fake.NewSimpleClientset(installed("latest")...)
	plan, _ = BuildPlan(context.Background(), cs, manifest(t, "v0.4.0"), Selection{})
	ui := rowFor(t, plan, "ui")
	if ui.Action != ActionUpdate || len(ui.Notes) == 0 {
		t.Fatalf("latest install = %+v", ui)
	}
	// Version annotation wins over the tag.
	objs := installed("latest")
	objs[7].(*appsv1.Deployment).Annotations = map[string]string{platformrelease.AnnotationVersion: "v0.6.0"}
	cs = fake.NewSimpleClientset(objs...)
	plan, _ = BuildPlan(context.Background(), cs, manifest(t, "v0.5.0"), Selection{})
	if r := rowFor(t, plan, "ui"); r.Action != ActionBlocked || r.CurrentVersion != "v0.6.0" {
		t.Fatalf("annotation version = %+v", r)
	}
}

func TestPlanCommitTaggedImageUsesReleaseVersion(t *testing.T) {
	tag := "v0.6.3-g" + strings.Repeat("a", 40)
	cs := fake.NewSimpleClientset(installed("v0.6.3")...)
	m := manifest(t, "v0.6.3", setComponent("runtime-api", "mcp-runtime-api", tag, ""))
	plan, err := BuildPlan(context.Background(), cs, m, Selection{})
	if err != nil {
		t.Fatal(err)
	}
	if row := rowFor(t, plan, "runtime-api"); row.Action != ActionUpdate || row.CurrentVersion != "v0.6.3" || row.TargetVersion != "v0.6.3" {
		t.Fatalf("same-release image change = %+v", row)
	}

	cs = fake.NewSimpleClientset(installed("v0.6.3-g" + strings.Repeat("b", 40))...)
	plan, err = BuildPlan(context.Background(), cs, m, Selection{})
	if err != nil {
		t.Fatal(err)
	}
	if row := rowFor(t, plan, "runtime-api"); row.Action != ActionUpdate || row.CurrentVersion != "v0.6.3" {
		t.Fatalf("same-release commit tag = %+v", row)
	}

	cs = fake.NewSimpleClientset(installed("v0.6.4")...)
	plan, err = BuildPlan(context.Background(), cs, m, Selection{})
	if err != nil {
		t.Fatal(err)
	}
	if row := rowFor(t, plan, "runtime-api"); row.Action != ActionBlocked {
		t.Fatalf("actual release downgrade must stay blocked: %+v", row)
	}
}

func TestPlanAppliesEmbeddedCRDs(t *testing.T) {
	cs := fake.NewSimpleClientset(installed("v0.4.0")...)
	m := manifest(t, "v0.4.0", func(m *platformrelease.Manifest) {
		m.CRDChange = true
		m.CRDs = "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: mcpservers.mcpruntime.org\n"
	})
	plan, err := BuildPlan(context.Background(), cs, m, Selection{})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.ApplyCRDs || len(plan.CRDNames) != 1 || plan.CRDNames[0] != "mcpservers.mcpruntime.org" {
		t.Fatalf("plan CRDs = apply=%v names=%v", plan.ApplyCRDs, plan.CRDNames)
	}
	if len(plan.Changed()) != 0 {
		t.Fatalf("expected image no-op with matching tags, got %+v", plan.Changed())
	}
	if !plan.NeedsApply() {
		t.Fatal("expected NeedsApply with CRD change even if images match")
	}
}

func TestPlanSkipsCRDsWhenOnlySet(t *testing.T) {
	cs := fake.NewSimpleClientset(installed("v0.4.0")...)
	m := manifest(t, "v0.5.0", func(m *platformrelease.Manifest) {
		m.CRDChange = true
		m.CRDs = "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: mcpservers.mcpruntime.org\n"
	})
	plan, err := BuildPlan(context.Background(), cs, m, Selection{Only: []string{"ui"}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ApplyCRDs {
		t.Fatal("expected CRD apply skipped with --only")
	}
	if len(plan.Warnings) == 0 || !strings.Contains(plan.Warnings[0], "--only") {
		t.Fatalf("expected --only CRD skip warning, got %v", plan.Warnings)
	}
}

func TestPlanRejectsNonCRDBundle(t *testing.T) {
	cs := fake.NewSimpleClientset(installed("v0.4.0")...)
	m := manifest(t, "v0.5.0", func(m *platformrelease.Manifest) {
		m.CRDChange = true
		m.CRDs = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: nope\n"
	})
	if _, err := BuildPlan(context.Background(), cs, m, Selection{}); err == nil || !strings.Contains(err.Error(), "ConfigMap") {
		t.Fatalf("expected non-CRD rejection, got %v", err)
	}
}

func TestPlanRefusesCRDChangeWithoutBundle(t *testing.T) {
	cs := fake.NewSimpleClientset(installed("v0.4.0")...)
	m := manifest(t, "v0.5.0", func(m *platformrelease.Manifest) { m.CRDChange = true })
	if _, err := BuildPlan(context.Background(), cs, m, Selection{}); err == nil || !strings.Contains(err.Error(), "no embedded crds") {
		t.Fatalf("expected missing CRD bundle error, got %v", err)
	}
}

func TestCRDReadyConditions(t *testing.T) {
	obj := map[string]any{
		"status": map[string]any{
			"conditions": []any{
				map[string]any{"type": "NamesAccepted", "status": "True"},
				map[string]any{"type": "Established", "status": "True"},
			},
		},
	}
	est, names, err := crdReadyConditions(obj)
	if err != nil || !est || !names {
		t.Fatalf("ready = %v %v %v", est, names, err)
	}
	obj["status"].(map[string]any)["conditions"] = []any{
		map[string]any{"type": "Established", "status": "False", "message": "pending"},
	}
	est, names, err = crdReadyConditions(obj)
	if err != nil || est || names {
		t.Fatalf("Established=False should keep polling, got est=%v names=%v err=%v", est, names, err)
	}
}

func TestApplyPatchesOnlyChangedImages(t *testing.T) {
	cs := fake.NewSimpleClientset(installed("v0.4.0")...)
	m := manifest(t, "v0.4.0",
		setComponent("ui", "mcp-ui", "v0.4.1", ""),
		setComponent("gateway-proxy", "mcp-gateway", "v0.4.1", ""),
		setComponent("operator", "mcp-runtime-operator", "v0.4.1", ""))
	plan, err := BuildPlan(context.Background(), cs, m, Selection{})
	if err != nil {
		t.Fatal(err)
	}
	var waited []string
	res := Apply(context.Background(), cs, plan, ApplyOptions{Timeout: time.Second, Waiter: func(_ context.Context, ns, name string, _ time.Duration) error {
		waited = append(waited, ns+"/"+name)
		return nil
	}})
	if res.Failed {
		t.Fatalf("apply failed: %+v", res)
	}
	if got := patchedDeployments(cs); strings.Join(got, ",") != platformrelease.OperatorDeployment+",mcp-ui" {
		t.Fatalf("patched = %v", got)
	}
	if len(waited) != 2 {
		t.Fatalf("waited = %v", waited)
	}
	op, _ := cs.AppsV1().Deployments("mcp-runtime").Get(context.Background(), platformrelease.OperatorDeployment, metav1.GetOptions{})
	c := op.Spec.Template.Spec.Containers[0]
	if c.Image != reg+"/mcp-runtime-operator:v0.4.1" {
		t.Fatalf("operator image = %s", c.Image)
	}
	if envValue(c.Env, "MCP_GATEWAY_PROXY_IMAGE") != reg+"/mcp-gateway:v0.4.1" || envValue(c.Env, "OTHER") != "keep" {
		t.Fatalf("operator env = %+v", c.Env)
	}
	if op.Annotations[platformrelease.AnnotationVersion] != "v0.4.0" ||
		op.Annotations[platformrelease.PreviousImageAnnotation("operator")] != reg+"/mcp-runtime-operator:v0.4.0" ||
		op.Labels[platformrelease.LabelComponent] != "operator" {
		t.Fatalf("operator metadata = %v %v", op.Labels, op.Annotations)
	}
	assertOnlyDeploymentWrites(t, cs)

	// Idempotent rerun: nothing changes.
	cs.ClearActions()
	plan, _ = BuildPlan(context.Background(), cs, m, Selection{})
	if n := len(plan.Changed()); n != 0 {
		t.Fatalf("rerun changes = %+v", plan.Changed())
	}
}

func TestApplyPartialFailureRollsBack(t *testing.T) {
	cs := fake.NewSimpleClientset(installed("v0.4.0")...)
	plan, err := BuildPlan(context.Background(), cs, manifest(t, "v0.5.0"), Selection{})
	if err != nil {
		t.Fatal(err)
	}
	failed := false
	waiter := func(_ context.Context, _, name string, _ time.Duration) error {
		if name == "mcp-runtime-api" && !failed {
			failed = true
			return errors.New("ImagePullBackOff")
		}
		return nil
	}
	res := Apply(context.Background(), cs, plan, ApplyOptions{Timeout: time.Second, RollbackOnFailure: true, Waiter: waiter})
	if !res.Failed {
		t.Fatal("expected failure")
	}
	status := map[string]string{}
	for _, w := range res.Workloads {
		status[w.Deployment] = w.Status
	}
	if status[platformrelease.OperatorDeployment] != StatusRolledBack || status["mcp-platform-api"] != StatusRolledBack || status["mcp-runtime-api"] != StatusRolledBack {
		t.Fatalf("status = %v", status)
	}
	if status["mcp-ui"] != StatusNotAttempted {
		t.Fatalf("ui should not be attempted: %v", status)
	}
	api, _ := cs.AppsV1().Deployments("mcp-platform").Get(context.Background(), "mcp-platform-api", metav1.GetOptions{})
	if api.Spec.Template.Spec.Containers[0].Image != reg+"/mcp-platform-api:v0.4.0" {
		t.Fatalf("platform-api not restored: %s", api.Spec.Template.Spec.Containers[0].Image)
	}
	op, _ := cs.AppsV1().Deployments("mcp-runtime").Get(context.Background(), platformrelease.OperatorDeployment, metav1.GetOptions{})
	if envValue(op.Spec.Template.Spec.Containers[0].Env, "MCP_GATEWAY_PROXY_IMAGE") != reg+"/mcp-gateway:v0.4.0" {
		t.Fatal("gateway proxy env not restored")
	}
	// Rollback order is reverse: runtime-api, platform-api, operator.
	got := patchedDeployments(cs)
	want := []string{platformrelease.OperatorDeployment, "mcp-platform-api", "mcp-runtime-api", "mcp-runtime-api", "mcp-platform-api", platformrelease.OperatorDeployment}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("patch order = %v", got)
	}
	assertOnlyDeploymentWrites(t, cs)
}

func TestApplyFailureWithoutRollbackKeepsStateAndPrintsRecovery(t *testing.T) {
	cs := fake.NewSimpleClientset(installed("v0.4.0")...)
	plan, _ := BuildPlan(context.Background(), cs, manifest(t, "v0.5.0"), Selection{Only: []string{"ui"}})
	res := Apply(context.Background(), cs, plan, ApplyOptions{Timeout: time.Second, Waiter: func(context.Context, string, string, time.Duration) error {
		return errors.New("timed out")
	}})
	if !res.Failed || res.Workloads[0].Status != StatusFailed {
		t.Fatalf("res = %+v", res)
	}
	var buf bytes.Buffer
	writeResultText(&buf, res)
	out := buf.String()
	if !strings.Contains(out, "kubectl -n mcp-platform rollout undo deployment/mcp-ui") ||
		!strings.Contains(out, "kubectl -n mcp-platform set image deployment/mcp-ui ui="+reg+"/mcp-ui:v0.4.0") {
		t.Fatalf("recovery commands missing:\n%s", out)
	}
	ui, _ := cs.AppsV1().Deployments("mcp-platform").Get(context.Background(), "mcp-ui", metav1.GetOptions{})
	if ui.Spec.Template.Spec.Containers[0].Image != reg+"/mcp-ui:v0.5.0" {
		t.Fatal("without rollback the new image should remain")
	}
}

func testDeps(cs kubernetes.Interface, m *platformrelease.Manifest, confirm func(io.Reader, io.Writer, ClusterInfo) (bool, error)) deps {
	data, _ := json.Marshal(m)
	return deps{
		loadManifest: func(context.Context, string) ([]byte, error) { return data, nil },
		kube: func(string, string) (kubeHandle, error) {
			return kubeHandle{
				Clientset: cs,
				Cluster:   ClusterInfo{Context: "kind-mcp-runtime", Server: "https://127.0.0.1:6443", ClusterID: "uid-1"},
			}, nil
		},
		waiter:  func(kubernetes.Interface) RolloutWaiter { return noopWaiter },
		confirm: confirm,
	}
}

func execute(t *testing.T, d deps, args ...string) (string, error) {
	t.Helper()
	cmd := newCommand(d)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	err := cmd.Execute()
	return out.String(), err
}

func TestCommandRequiresExplicitTarget(t *testing.T) {
	cs := fake.NewSimpleClientset(installed("v0.4.0")...)
	d := testDeps(cs, manifest(t, "v0.5.0"), nil)
	if _, err := execute(t, d); err == nil || !strings.Contains(err.Error(), "--to") {
		t.Fatalf("expected target error, got %v", err)
	}
	if _, err := execute(t, d, "--to", "v0.6.0", "--release-manifest", "m.json"); err == nil || !strings.Contains(err.Error(), "refusing to change the target silently") {
		t.Fatalf("expected mismatch error, got %v", err)
	}
	if _, err := execute(t, d, "--to", "latest"); err == nil {
		t.Fatal("non-semver --to must fail")
	}
	if _, err := execute(t, d, "--to", "v0.5.0", "--output", "yaml"); err == nil {
		t.Fatal("bad --output must fail")
	}
}

func TestCommandDryRunMakesNoChanges(t *testing.T) {
	cs := fake.NewSimpleClientset(installed("v0.4.0")...)
	d := testDeps(cs, manifest(t, "v0.5.0"), func(io.Reader, io.Writer, ClusterInfo) (bool, error) {
		t.Fatal("dry-run must not prompt")
		return false, nil
	})
	out, err := execute(t, d, "--to", "v0.5.0", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Kube context:   kind-mcp-runtime", "Cluster ID:     uid-1", "Version:        v0.4.0 -> v0.5.0", "Dry run (plan only;", "Preserved (never modified by update):", "mcp-platform/mcp-ui"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output missing %q:\n%s", want, out)
		}
	}
	if len(patchedDeployments(cs)) != 0 {
		t.Fatal("dry-run patched deployments")
	}
}

func TestCommandConfirmation(t *testing.T) {
	cs := fake.NewSimpleClientset(installed("v0.4.0")...)
	d := testDeps(cs, manifest(t, "v0.5.0"), func(io.Reader, io.Writer, ClusterInfo) (bool, error) { return false, nil })
	if _, err := execute(t, d, "--to", "v0.5.0"); err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("expected abort, got %v", err)
	}
	if len(patchedDeployments(cs)) != 0 {
		t.Fatal("declined confirmation patched deployments")
	}
	// Non-TTY default confirm refuses.
	d.confirm = promptConfirm
	d.stdin = strings.NewReader("yes\n")
	if _, err := execute(t, d, "--to", "v0.5.0"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("expected non-tty refusal, got %v", err)
	}
	// --yes applies.
	out, err := execute(t, d, "--to", "v0.5.0", "--yes", "--only", "ui")
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if got := patchedDeployments(cs); len(got) != 1 || got[0] != "mcp-ui" {
		t.Fatalf("patched = %v", got)
	}
	if !strings.Contains(out, "updated") {
		t.Fatalf("missing result:\n%s", out)
	}
}

func TestCommandJSONOutputAndBlocked(t *testing.T) {
	cs := fake.NewSimpleClientset(installed("v0.5.0")...)
	d := testDeps(cs, manifest(t, "v0.4.0"), nil)
	out, err := execute(t, d, "--to", "v0.4.0", "--dry-run", "--output", "json")
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("expected blocked error, got %v", err)
	}
	var doc struct {
		DryRun bool `json:"dryRun"`
		Plan   Plan `json:"plan"`
	}
	if jerr := json.Unmarshal([]byte(out), &doc); jerr != nil {
		t.Fatalf("json output: %v\n%s", jerr, out)
	}
	if !doc.DryRun || doc.Plan.Cluster.ClusterID != "uid-1" || doc.Plan.TargetVersion != "v0.4.0" {
		t.Fatalf("doc = %+v", doc)
	}
}

func TestCommandUpToDate(t *testing.T) {
	cs := fake.NewSimpleClientset(installed("v0.5.0")...)
	d := testDeps(cs, manifest(t, "v0.5.0"), nil)
	out, err := execute(t, d, "--to", "v0.5.0", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "up to date") || len(patchedDeployments(cs)) != 0 {
		t.Fatalf("expected no-op:\n%s", out)
	}
}

func TestCommandRolloutFailureReturnsError(t *testing.T) {
	cs := fake.NewSimpleClientset(installed("v0.4.0")...)
	d := testDeps(cs, manifest(t, "v0.5.0"), nil)
	d.waiter = func(kubernetes.Interface) RolloutWaiter {
		return func(context.Context, string, string, time.Duration) error { return errors.New("boom") }
	}
	out, err := execute(t, d, "--to", "v0.5.0", "--yes", "--only", "ui", "--rollback-on-failure=false")
	if err == nil || !strings.Contains(err.Error(), "update failed") {
		t.Fatalf("expected failure, got %v", err)
	}
	if !strings.Contains(out, "Manual recovery commands") {
		t.Fatalf("missing recovery:\n%s", out)
	}
}

func TestPlanImageBuildsChangedOnlyReuseAndBuild(t *testing.T) {
	cs := fake.NewSimpleClientset(installed("v0.4.0")...)
	plan, err := BuildPlan(context.Background(), cs, manifest(t, "v0.5.0"), Selection{})
	if err != nil {
		t.Fatal(err)
	}
	present := map[string]bool{
		reg + "/mcp-ui:v0.5.0": true,
	}
	actions, err := planImageBuilds(context.Background(), plan, BuildOptions{
		RegistryHasImage: func(_ context.Context, image string) (bool, error) {
			return present[image], nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) < 7 {
		t.Fatalf("expected every changed Built image, got %d: %+v", len(actions), actions)
	}
	byComp := map[string]ImageBuildAction{}
	for _, a := range actions {
		byComp[a.Component] = a
	}
	if a := byComp["ui"]; a.Action != ImageActionReuse {
		t.Fatalf("ui should reuse existing tag: %+v", a)
	}
	if a := byComp["platform-api"]; a.Action != ImageActionBuild {
		t.Fatalf("platform-api should build: %+v", a)
	}
	if _, ok := byComp["operator"]; !ok {
		t.Fatal("operator must be in build plan when release changes it")
	}
}

func TestPlanCommitTaggedImageRebuildsExistingTag(t *testing.T) {
	tag := "v0.6.4-g" + strings.Repeat("a", 40)
	cs := fake.NewSimpleClientset(installed("v0.6.3")...)
	m := manifest(t, "v0.6.4", setComponent("runtime-api", "mcp-runtime-api", tag, ""))
	plan, err := BuildPlan(context.Background(), cs, m, Selection{Only: []string{"runtime-api"}})
	if err != nil {
		t.Fatal(err)
	}
	probes := 0
	actions, err := planImageBuilds(context.Background(), plan, BuildOptions{
		RegistryHasImage: func(context.Context, string) (bool, error) {
			probes++
			return true, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 1 || actions[0].Action != ImageActionBuild || probes != 0 {
		t.Fatalf("commit-tagged image must build without registry reuse: actions=%+v probes=%d", actions, probes)
	}
}

func TestVerifyReleaseSource(t *testing.T) {
	source := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", source}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-q", "-m", "init")
	commit := git("rev-parse", "HEAD")
	if err := verifyReleaseSource(context.Background(), source, commit); err != nil {
		t.Fatal(err)
	}
	if err := verifyReleaseSource(context.Background(), source, strings.Repeat("a", 40)); err == nil || !strings.Contains(err.Error(), "requires source commit") {
		t.Fatalf("mismatched commit error = %v", err)
	}
	if err := os.WriteFile(source+"/untracked.txt", []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyReleaseSource(context.Background(), source, commit); err == nil || !strings.Contains(err.Error(), "local changes") {
		t.Fatalf("dirty checkout error = %v", err)
	}
}

func TestBuildAndPushChangedSkipsReuse(t *testing.T) {
	var built, pushed []string
	actions := []ImageBuildAction{
		{Component: "ui", Image: reg + "/mcp-ui:v0.5.0", Action: ImageActionReuse},
		{Component: "platform-api", Image: reg + "/mcp-platform-api:v0.5.0", Action: ImageActionBuild},
	}
	err := buildAndPushChanged(context.Background(), actions, BuildOptions{
		Enabled: true,
		Source:  ".",
		BuildImage: func(_ context.Context, _, _ string, spec imageBuildSpec, image string) error {
			built = append(built, spec.Component+":"+image)
			return nil
		},
		PushImage: func(_ context.Context, image string) error {
			pushed = append(pushed, image)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(built) != 1 || !strings.Contains(built[0], "platform-api") {
		t.Fatalf("built = %v", built)
	}
	if len(pushed) != 1 || pushed[0] != reg+"/mcp-platform-api:v0.5.0" {
		t.Fatalf("pushed = %v", pushed)
	}
}

func TestApplySkipsAlreadyCurrentDeployment(t *testing.T) {
	cs := fake.NewSimpleClientset(installed("v0.5.0")...)
	// Force a plan row that thinks ui needs update while the live image already matches.
	plan, err := BuildPlan(context.Background(), cs, manifest(t, "v0.5.0", setComponent("ui", "mcp-ui", "v0.5.1", "")), Selection{Only: []string{"ui"}})
	if err != nil {
		t.Fatal(err)
	}
	// Pretend inventory said update, but put the target image on the Deployment before Apply.
	ui, _ := cs.AppsV1().Deployments("mcp-platform").Get(context.Background(), "mcp-ui", metav1.GetOptions{})
	ui.Spec.Template.Spec.Containers[0].Image = reg + "/mcp-ui:v0.5.1"
	_, _ = cs.AppsV1().Deployments("mcp-platform").Update(context.Background(), ui, metav1.UpdateOptions{})
	cs.ClearActions()

	res := Apply(context.Background(), cs, plan, ApplyOptions{Timeout: time.Second, Waiter: noopWaiter})
	if res.Failed || len(res.Workloads) != 1 || res.Workloads[0].Status != StatusSkipped {
		t.Fatalf("res = %+v", res)
	}
	if len(patchedDeployments(cs)) != 0 {
		t.Fatalf("expected no patch, got %v", patchedDeployments(cs))
	}
}

func TestCRDSpecEqualAndDecode(t *testing.T) {
	yamlDoc := "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: mcpservers.mcpruntime.org\nspec:\n  group: mcpruntime.org\n"
	docs, err := decodeCRDDocuments(yamlDoc)
	if err != nil || len(docs) != 1 || docs[0].Name != "mcpservers.mcpruntime.org" {
		t.Fatalf("docs = %+v err=%v", docs, err)
	}
	live := map[string]any{"spec": docs[0].Object["spec"], "status": map[string]any{"conditions": []any{}}}
	if !crdSpecEqual(docs[0].Object, live) {
		t.Fatal("expected matching specs")
	}
	live["spec"] = map[string]any{"group": "other.org"}
	if crdSpecEqual(docs[0].Object, live) {
		t.Fatal("expected differing specs")
	}
}

func TestCommandBuildDryRunPlansImagesWithoutDocker(t *testing.T) {
	cs := fake.NewSimpleClientset(installed("v0.4.0")...)
	d := testDeps(cs, manifest(t, "v0.5.0"), func(io.Reader, io.Writer, ClusterInfo) (bool, error) {
		t.Fatal("dry-run must not prompt")
		return false, nil
	})
	d.build = BuildOptions{
		BuildImage: func(context.Context, string, string, imageBuildSpec, string) error {
			t.Fatal("dry-run must not build")
			return nil
		},
		PushImage: func(context.Context, string) error {
			t.Fatal("dry-run must not push")
			return nil
		},
	}
	out, err := execute(t, d, "--to", "v0.5.0", "--dry-run", "--build", "--source", ".")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Images (--build):") || !strings.Contains(out, "build") {
		t.Fatalf("expected build plan:\n%s", out)
	}
	if !strings.Contains(out, "registry not checked") {
		t.Fatalf("dry-run should skip registry probe:\n%s", out)
	}
	if !strings.Contains(out, "image build(s)") {
		t.Fatalf("expected dry-run build summary:\n%s", out)
	}
	if len(patchedDeployments(cs)) != 0 {
		t.Fatal("dry-run patched deployments")
	}
}

func TestBuildAndPushRetriesThenSucceeds(t *testing.T) {
	attempts := 0
	actions := []ImageBuildAction{
		{Component: "ui", Image: reg + "/mcp-ui:v0.5.0", Action: ImageActionBuild},
	}
	err := buildAndPushChanged(context.Background(), actions, BuildOptions{
		Enabled: true,
		Source:  ".",
		Retries: 1,
		BuildImage: func(context.Context, string, string, imageBuildSpec, string) error {
			attempts++
			if attempts == 1 {
				return errors.New("fatal error: fault")
			}
			return nil
		},
		PushImage: func(context.Context, string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestIsRegistryNotFound(t *testing.T) {
	if !isRegistryNotFound("manifest unknown: tag v0.3.1") {
		t.Fatal("expected not-found")
	}
	if isRegistryNotFound("unauthorized: authentication required") {
		t.Fatal("auth errors must not look like missing")
	}
}

func TestPlanOnlyTrimsSpaces(t *testing.T) {
	cs := fake.NewSimpleClientset(installed("v0.4.0")...)
	plan, err := BuildPlan(context.Background(), cs, manifest(t, "v0.5.0"), Selection{Only: []string{" ui ", "platform-api"}})
	if err != nil {
		t.Fatal(err)
	}
	changed := plan.Changed()
	if len(changed) != 2 {
		t.Fatalf("changed = %+v", changed)
	}
}
