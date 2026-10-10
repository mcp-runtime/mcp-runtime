package update

import (
	"mcp-runtime/internal/cli/core"
	"reflect"
	"testing"
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
