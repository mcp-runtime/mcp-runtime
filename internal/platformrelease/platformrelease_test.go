package platformrelease

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestVersionCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v0.5.0", "v0.5.0", 0},
		{"v0.5.0", "0.5.0", 0},
		{"v0.5.1", "v0.5.0", 1},
		{"v0.4.9", "v0.5.0", -1},
		{"v1.0.0", "v0.99.99", 1},
		{"v0.5.0-rc.1", "v0.5.0", -1},
		{"v0.5.0-rc.2", "v0.5.0-rc.10", -1},
		{"v0.5.0-alpha", "v0.5.0-alpha.1", -1},
		{"v0.5.0-beta", "v0.5.0-alpha", 1},
		{"v0.5.0-1", "v0.5.0-alpha", -1},
	}
	for _, tc := range cases {
		got, ok := CompareVersionStrings(tc.a, tc.b)
		if !ok || got != tc.want {
			t.Errorf("Compare(%s, %s) = %d, %v; want %d", tc.a, tc.b, got, ok, tc.want)
		}
	}
	if _, ok := CompareVersionStrings("latest", "v0.5.0"); ok {
		t.Fatal("latest must not compare as semver")
	}
	a, _ := ParseVersion("v1.14.2")
	b, _ := ParseVersion("v1.14.5")
	c, _ := ParseVersion("v1.15.0")
	if !a.SameMinor(b) || a.SameMinor(c) {
		t.Fatal("SameMinor mismatch")
	}
}

func TestParseImageRef(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	cases := []struct {
		in                          string
		registry, path, tag, digest string
	}{
		{"mcp-ui:latest", "", "mcp-ui", "latest", ""},
		{"registry.registry.svc.cluster.local:5000/mcp-ui:v1", "registry.registry.svc.cluster.local:5000", "mcp-ui", "v1", ""},
		{"localhost/foo/bar", "localhost", "foo/bar", "", ""},
		{"docker.io/princekrroshan01/mcp-auth-server:latest@" + digest, "docker.io", "princekrroshan01/mcp-auth-server", "latest", digest},
		{"quay.io/jetstack/cert-manager-controller:v1.14.5", "quay.io", "jetstack/cert-manager-controller", "v1.14.5", ""},
		{"org/repo", "", "org/repo", "", ""},
	}
	for _, tc := range cases {
		ref, err := ParseImageRef(tc.in)
		if err != nil {
			t.Fatalf("ParseImageRef(%q) error: %v", tc.in, err)
		}
		if ref.Registry != tc.registry || ref.Path != tc.path || ref.Tag != tc.tag || ref.Digest != tc.digest {
			t.Errorf("ParseImageRef(%q) = %+v", tc.in, ref)
		}
		if ref.String() != tc.in {
			t.Errorf("String() = %q, want %q", ref.String(), tc.in)
		}
	}
	for _, bad := range []string{"", "UPPER/case", "repo:bad tag", "repo@sha256:short", "repo name"} {
		if _, err := ParseImageRef(bad); err == nil {
			t.Errorf("ParseImageRef(%q) expected error", bad)
		}
	}
	if got := DigestFromImageID("docker-pullable://x/y@" + digest); got != digest {
		t.Fatalf("DigestFromImageID = %q", got)
	}
	if got := DigestFromImageID("x/y:tag"); got != "" {
		t.Fatalf("DigestFromImageID without digest = %q", got)
	}
}

func TestParseManifestValidation(t *testing.T) {
	good := `{"apiVersion":"mcpruntime.org/v1alpha1","kind":"PlatformRelease","version":"v0.5.0","components":[{"name":"ui","repository":"mcp-ui","tag":"v0.5.0"}]}`
	if _, err := ParseManifest([]byte(good)); err != nil {
		t.Fatalf("good manifest: %v", err)
	}
	yamlGood := "apiVersion: mcpruntime.org/v1alpha1\nkind: PlatformRelease\nversion: v0.5.0\ncomponents:\n- name: operator\n  repository: mcp-runtime-operator\n  tag: v0.5.0\n"
	if _, err := ParseManifest([]byte(yamlGood)); err != nil {
		t.Fatalf("yaml manifest: %v", err)
	}
	cases := map[string]string{
		"bad version":     `{"apiVersion":"mcpruntime.org/v1alpha1","kind":"PlatformRelease","version":"latest","components":[{"name":"ui","repository":"r","tag":"t"}]}`,
		"unknown comp":    `{"apiVersion":"mcpruntime.org/v1alpha1","kind":"PlatformRelease","version":"v1.0.0","components":[{"name":"traefik","repository":"r","tag":"t"}]}`,
		"duplicate":       `{"apiVersion":"mcpruntime.org/v1alpha1","kind":"PlatformRelease","version":"v1.0.0","components":[{"name":"ui","repository":"r","tag":"t"},{"name":"ui","repository":"r","tag":"t"}]}`,
		"bad digest":      `{"apiVersion":"mcpruntime.org/v1alpha1","kind":"PlatformRelease","version":"v1.0.0","components":[{"name":"ui","repository":"r","tag":"t","digest":"md5:x"}]}`,
		"tag in repo":     `{"apiVersion":"mcpruntime.org/v1alpha1","kind":"PlatformRelease","version":"v1.0.0","components":[{"name":"ui","repository":"r:v1","tag":"t"}]}`,
		"wrong kind":      `{"apiVersion":"mcpruntime.org/v1alpha1","kind":"Other","version":"v1.0.0","components":[{"name":"ui","repository":"r","tag":"t"}]}`,
		"empty":           `{"apiVersion":"mcpruntime.org/v1alpha1","kind":"PlatformRelease","version":"v1.0.0","components":[]}`,
		"unknown field":   `{"apiVersion":"mcpruntime.org/v1alpha1","kind":"PlatformRelease","version":"v1.0.0","deployment":"x","components":[{"name":"ui","repository":"r","tag":"t"}]}`,
		"bad registry":    `{"apiVersion":"mcpruntime.org/v1alpha1","kind":"PlatformRelease","version":"v1.0.0","registry":"http://x","components":[{"name":"ui","repository":"r","tag":"t"}]}`,
		"missing tag":     `{"apiVersion":"mcpruntime.org/v1alpha1","kind":"PlatformRelease","version":"v1.0.0","components":[{"name":"ui","repository":"r"}]}`,
		"not json either": `:::`,
	}
	for name, body := range cases {
		if _, err := ParseManifest([]byte(body)); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func TestTargetRefResolution(t *testing.T) {
	m := &Manifest{}
	ref, err := m.TargetRef(ManifestComponent{Repository: "mcp-ui", Tag: "v1"}, "registry.local:5000")
	if err != nil || ref.String() != "registry.local:5000/mcp-ui:v1" {
		t.Fatalf("fallback registry: %v %s", err, ref.String())
	}
	m.Registry = "ghcr.io"
	ref, _ = m.TargetRef(ManifestComponent{Repository: "mcp-ui", Tag: "v1"}, "registry.local:5000")
	if ref.String() != "ghcr.io/mcp-ui:v1" {
		t.Fatalf("manifest registry: %s", ref.String())
	}
	ref, _ = m.TargetRef(ManifestComponent{Repository: "quay.io/x/y", Tag: "v1"}, "registry.local:5000")
	if ref.String() != "quay.io/x/y:v1" {
		t.Fatalf("absolute repo: %s", ref.String())
	}
}

func TestGenerateManifestCoversBuiltComponents(t *testing.T) {
	crdYAML := "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: mcpservers.mcpruntime.org\nspec:\n  group: mcpruntime.org\n"
	m, err := GenerateManifest("v0.5.0", true, crdYAML)
	if err != nil {
		t.Fatal(err)
	}
	if !m.CRDChange || m.CRDs == "" {
		t.Fatal("crdChange/crds not propagated")
	}
	names := map[string]bool{}
	for _, c := range m.Components {
		names[c.Name] = true
		if c.Tag != "v0.5.0" || c.Digest != "" {
			t.Fatalf("unexpected component %+v", c)
		}
	}
	for _, want := range []string{"operator", "gateway-proxy", "platform-api", "runtime-api", "analytics-api", "ingest", "processor", "ui", "doctor-smoke"} {
		if !names[want] {
			t.Errorf("generated manifest missing %s", want)
		}
	}
	for _, not := range []string{"mcp-auth", "cert-manager-controller"} {
		if names[not] {
			t.Errorf("generated manifest must not include third-party %s", not)
		}
	}
	if _, err := GenerateManifest("v0.5.0", true, ""); err == nil {
		t.Fatal("crdChange without CRD YAML must be rejected")
	}
	if _, err := GenerateManifest("dev", false, ""); err == nil {
		t.Fatal("non-semver version must be rejected")
	}
}

func TestGenerateManifestForCommit(t *testing.T) {
	commit := strings.Repeat("a", 40)
	m, err := GenerateManifestForCommit("v0.6.4", commit, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != "v0.6.4" {
		t.Fatalf("release version = %q", m.Version)
	}
	for _, c := range m.Components {
		if c.Tag != "v0.6.4-g"+commit {
			t.Fatalf("%s image tag = %q", c.Name, c.Tag)
		}
	}
	if _, err := GenerateManifestForCommit("v0.6.4", "abc123", false, ""); err == nil {
		t.Fatal("abbreviated source revision must be rejected")
	}
}

func TestReleaseVersionFromImageTag(t *testing.T) {
	commit := strings.Repeat("a", 40)
	for tag, want := range map[string]string{
		"v0.6.4-g" + commit: "v0.6.4",
		"v0.6.3-22e38651":   "v0.6.3-22e38651",
		"v0.6.4-deadbeef":   "v0.6.4-deadbeef",
		"v0.6.4-rc.1":       "v0.6.4-rc.1",
		"v0.6.4":            "v0.6.4",
		"latest":            "latest",
	} {
		if got := ReleaseVersionFromImageTag(tag); got != want {
			t.Errorf("ReleaseVersionFromImageTag(%q) = %q, want %q", tag, got, want)
		}
	}
	if got, ok := ReleaseCommitFromImageTag("v0.6.4-g" + commit); !ok || got != commit {
		t.Fatalf("release commit = %q, %v", got, ok)
	}
	if _, ok := ReleaseCommitFromImageTag("v0.6.3-22e38651"); ok {
		t.Fatal("short prerelease suffix is not a release commit")
	}
}

func TestBundleAndCRDObjectNames(t *testing.T) {
	dir := t.TempDir()
	a := "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: mcpservers.mcpruntime.org\n"
	b := "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: mcpaccessgrants.mcpruntime.org\n"
	if err := os.WriteFile(filepath.Join(dir, "z.yaml"), []byte(a), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.yaml"), []byte(b), 0o644); err != nil {
		t.Fatal(err)
	}
	bundled, err := BundleCRDs(dir)
	if err != nil {
		t.Fatal(err)
	}
	filtered, names, err := FilterCRDBundle(bundled)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "mcpaccessgrants.mcpruntime.org" || names[1] != "mcpservers.mcpruntime.org" {
		t.Fatalf("names = %v", names)
	}
	if !strings.Contains(filtered, "kind: CustomResourceDefinition") {
		t.Fatalf("filtered missing CRD kind: %s", filtered)
	}
	mixed := bundled + "\n---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: nope\n"
	if _, _, err := FilterCRDBundle(mixed); err == nil || !strings.Contains(err.Error(), "ConfigMap") {
		t.Fatalf("expected non-CRD rejection, got %v", err)
	}
}

func TestReleaseManifestURL(t *testing.T) {
	if got := ReleaseManifestURL("v0.5.0"); got != "https://github.com/mcp-runtime/mcp-runtime/releases/download/v0.5.0/platform-manifest.json" {
		t.Fatalf("url = %s", got)
	}
	if got := ReleaseCRDsURL("v0.5.0"); got != "https://github.com/mcp-runtime/mcp-runtime/releases/download/v0.5.0/platform-crds.yaml" {
		t.Fatalf("crds url = %s", got)
	}
}

func TestLoadManifestRejectsPlainHTTP(t *testing.T) {
	if _, err := LoadManifest(context.Background(), "http://example.com/m.json", nil); err == nil || !strings.Contains(err.Error(), "plain http") {
		t.Fatalf("expected plain http refusal, got %v", err)
	}
}

func TestStampInstalledVersionMetadataOnly(t *testing.T) {
	ui := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "mcp-ui", Namespace: "mcp-platform"}}
	op := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: OperatorDeployment, Namespace: OperatorNamespace}}
	cs := fake.NewSimpleClientset(ui, op)
	if err := StampInstalledVersion(context.Background(), cs, "v0.5.0"); err != nil {
		t.Fatal(err)
	}
	got, _ := cs.AppsV1().Deployments("mcp-platform").Get(context.Background(), "mcp-ui", metav1.GetOptions{})
	if got.Annotations[AnnotationVersion] != "v0.5.0" || got.Labels[LabelComponent] != "ui" || got.Labels[LabelPartOf] != LabelPartOfValue {
		t.Fatalf("ui metadata = %v %v", got.Labels, got.Annotations)
	}
	gotOp, _ := cs.AppsV1().Deployments(OperatorNamespace).Get(context.Background(), OperatorDeployment, metav1.GetOptions{})
	if gotOp.Labels[LabelComponent] != "operator" {
		t.Fatalf("operator label = %v", gotOp.Labels)
	}
	for _, a := range cs.Actions() {
		if a.GetVerb() == "patch" {
			patch := a.(interface{ GetPatch() []byte }).GetPatch()
			var body map[string]any
			_ = json.Unmarshal(patch, &body)
			if _, ok := body["spec"]; ok {
				t.Fatalf("stamp patch must not touch spec: %s", patch)
			}
		}
		if a.GetResource().Resource != "deployments" {
			t.Fatalf("stamp touched %s", a.GetResource().Resource)
		}
	}
}
