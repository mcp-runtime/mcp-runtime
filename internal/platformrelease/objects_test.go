package platformrelease

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
)

const goodRole = `
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata: {name: mcp-ingest-tokenreview}
rules:
- apiGroups: [authentication.k8s.io]
  resources: [tokenreviews]
  verbs: [create]
`

func TestParsePlatformObjectsAcceptsScopedRBAC(t *testing.T) {
	bundle := goodRole + `
---
apiVersion: v1
kind: ServiceAccount
metadata: {name: mcp-ingest, namespace: mcp-observability}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata: {name: mcp-ingest-tokenreview}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: mcp-ingest-tokenreview}
subjects:
- {kind: ServiceAccount, name: mcp-ingest, namespace: mcp-observability}
`
	objs, err := ParsePlatformObjects(bundle)
	if err != nil || len(objs) != 3 {
		t.Fatalf("objs=%d err=%v", len(objs), err)
	}
}

func TestParsePlatformObjectsRejectsUnsafeRequests(t *testing.T) {
	binding := func(refName, subject string) string {
		return goodRole + `
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata: {name: mcp-x}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: ` + refName + `}
subjects:
- ` + subject + `
`
	}
	tests := []struct {
		name, bundle, want string
	}{
		{"wildcard verb", strings.Replace(goodRole, "[create]", `["*"]`, 1), "wildcard"},
		{"wildcard resource", strings.Replace(goodRole, "[tokenreviews]", `["*"]`, 1), "wildcard"},
		{"escalate verb", strings.Replace(goodRole, "[create]", "[escalate]", 1), `"escalate"`},
		{"impersonate verb", strings.Replace(goodRole, "[create]", "[impersonate]", 1), `"impersonate"`},
		{"disallowed kind", "apiVersion: v1\nkind: Secret\nmetadata: {name: x, namespace: mcp-platform}\n", "may only contain"},
		{"binding to existing cluster-admin", binding("cluster-admin", "{kind: ServiceAccount, name: a, namespace: mcp-platform}"), "must be defined in the same release bundle"},
		{"user subject", binding("mcp-ingest-tokenreview", "{kind: User, name: mallory}"), "only ServiceAccount"},
		{"foreign namespace subject", binding("mcp-ingest-tokenreview", "{kind: ServiceAccount, name: a, namespace: kube-system}"), "not a platform namespace"},
		{"foreign namespace object", "apiVersion: v1\nkind: ServiceAccount\nmetadata: {name: x, namespace: kube-system}\n", "not a platform namespace"},
		{"unprefixed cluster role", strings.Replace(goodRole, "mcp-ingest-tokenreview", "admin-ish", 1), `must start with "mcp-"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParsePlatformObjects(tc.bundle)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestManifestRejectsUnsafePlatformObjects(t *testing.T) {
	m, err := GenerateManifest("v0.7.2", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.ApplyReleaseExtras(strings.Replace(goodRole, "[create]", `["*"]`, 1), nil); err == nil {
		t.Fatal("manifest with a wildcard role must not validate")
	}
	if err := m.ApplyReleaseExtras("", map[string]*ComponentWiring{"not-a-component": {ServiceAccountName: "x"}}); err == nil {
		t.Fatal("wiring for an unknown component must be rejected")
	}
	if err := m.ApplyReleaseExtras("", map[string]*ComponentWiring{"ingest": {Env: map[string]string{"lower": "x"}}}); err == nil {
		t.Fatal("invalid env name must be rejected")
	}
}

func TestBundlePlatformObjectsFailsForUnknownKey(t *testing.T) {
	if _, err := BundlePlatformObjects("../../k8s", []string{"ClusterRole/mcp-does-not-exist"}); err == nil {
		t.Fatal("selecting an object that is not in k8s/ must fail")
	}
}

// TestReleaseWiringMatchesK8sManifests keeps config/release/wiring.yaml honest:
// everything a release asks update to apply must equal what setup renders from
// k8s/, so update and setup converge on the same Deployment.
func TestReleaseWiringMatchesK8sManifests(t *testing.T) {
	wiring, err := LoadWiring("../../config/release/wiring.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BundlePlatformObjects("../../k8s", wiring.PlatformObjects); err != nil {
		t.Fatalf("declared platform objects must exist in k8s/: %v", err)
	}
	for name, want := range wiring.Components {
		c, ok := Lookup(name)
		if !ok {
			t.Fatalf("wiring component %q is not in the catalog", name)
		}
		dep := findK8sDeployment(t, "../../k8s", c.Namespace, c.Deployment)
		spec := nested(dep, "spec", "template", "spec")
		if got, _ := spec["serviceAccountName"].(string); want.ServiceAccountName != "" && got != want.ServiceAccountName {
			t.Errorf("%s: k8s serviceAccountName %q != wiring %q", name, got, want.ServiceAccountName)
		}
		if want.AutomountServiceAccountToken != nil {
			if got, ok := spec["automountServiceAccountToken"].(bool); !ok || got != *want.AutomountServiceAccountToken {
				t.Errorf("%s: k8s automountServiceAccountToken %v != wiring %v", name, spec["automountServiceAccountToken"], *want.AutomountServiceAccountToken)
			}
		}
		containers, _ := spec["containers"].([]any)
		for _, ci := range containers {
			container, _ := ci.(map[string]any)
			if container["name"] != c.Container {
				continue
			}
			env := map[string]string{}
			list, _ := container["env"].([]any)
			for _, ei := range list {
				e, _ := ei.(map[string]any)
				n, _ := e["name"].(string)
				v, _ := e["value"].(string)
				env[n] = v
			}
			for k, v := range want.Env {
				if env[k] != v {
					t.Errorf("%s: k8s env %s=%q != wiring %q", name, k, env[k], v)
				}
			}
		}
	}
}

func nested(m map[string]any, path ...string) map[string]any {
	for _, p := range path {
		next, _ := m[p].(map[string]any)
		m = next
	}
	return m
}

func findK8sDeployment(t *testing.T, dir, namespace, name string) map[string]any {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, "*.yaml"))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		dec := k8syaml.NewYAMLOrJSONDecoder(strings.NewReader(string(data)), 4096)
		for {
			var obj map[string]any
			if err := dec.Decode(&obj); err != nil {
				if err == io.EOF {
					break
				}
				break // templated file; not the one we want
			}
			meta, _ := obj["metadata"].(map[string]any)
			if obj["kind"] == "Deployment" && meta["name"] == name && meta["namespace"] == namespace {
				return obj
			}
		}
	}
	t.Fatalf("Deployment %s/%s not found in %s", namespace, name, dir)
	return nil
}
