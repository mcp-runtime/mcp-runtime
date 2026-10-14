package update

import (
	"context"
	"mcp-runtime/internal/cli/core"
	"reflect"
	"strings"
	"testing"

	"k8s.io/client-go/kubernetes/fake"
)

func TestUpdateRegistryAuthoritySelection(t *testing.T) {
	for _, tc := range []struct {
		image   string
		bundled bool
	}{
		{"registry.registry.svc.cluster.local:5000/mcp-ui:qa", true},
		{"registry.registry.svc.cluster.local/mcp-ui:qa", true},
		{"ghcr.io/mcp-runtime/ui:qa", false},
		{"registry.example.com/mcp-ui:qa", false},
		{"other.registry.svc.cluster.local:5000/ui:qa", false},
		{"registry.registry.svc.cluster.local.example.com/ui:qa", false},
	} {
		if got := bundledRegistryTarget(tc.image); got != tc.bundled {
			t.Errorf("%s: bundled=%v", tc.image, got)
		}
	}
}

func TestCommitBuildDoesNotProbeClusterDNSWithWorkstationDocker(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // No docker binary: bundled targets use the cluster push transport.
	tag := "v0.9.0-g" + strings.Repeat("a", 40)
	cs := fake.NewSimpleClientset(installed("v0.8.1")...)
	plan, err := BuildPlan(context.Background(), cs, manifest(t, "v0.9.0", setComponent("runtime-api", "mcp-runtime-api", tag, "")), Selection{Only: []string{"runtime-api"}})
	if err != nil {
		t.Fatal(err)
	}
	actions, err := planImageBuilds(context.Background(), plan, BuildOptions{})
	if err != nil || len(actions) != 1 || actions[0].Action != ImageActionBuild {
		t.Fatalf("bundled registry commit build must not depend on host registry DNS: %+v %v", actions, err)
	}
}

func TestUpdateHelperCommandsKeepExplicitTarget(t *testing.T) {
	mock := &core.MockExecutor{}
	target := updateTargetExecutor{delegate: mock, kubeconfig: "/tmp/kind-test.yaml", kubeContext: "kind-selected"}
	if _, err := target.Command("kubectl", []string{"get", "namespace", "registry"}, core.NoControlChars()); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Command("docker", []string{"save", "image:qa"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"--kubeconfig=/tmp/kind-test.yaml", "--context=kind-selected", "get", "namespace", "registry"}
	if !reflect.DeepEqual(mock.Commands[0].Args, want) {
		t.Fatalf("helper target: %v", mock.Commands[0].Args)
	}
	if !reflect.DeepEqual(mock.Commands[1].Args, []string{"save", "image:qa"}) {
		t.Fatal("cluster flags leaked into Docker")
	}
}
