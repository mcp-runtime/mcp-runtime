package ops

import (
	"go.uber.org/zap"
	"mcp-runtime/internal/cli/core"
	"testing"
)

func TestPortForwardUsesComponentNamespace(t *testing.T) {
	// Placement can change independently of identity. Use a local catalog copy
	// so the canonical inventory and other commands keep legacy placement.
	previous := platformComponents
	platformComponents = append([]platformComponent(nil), previous...)
	t.Cleanup(func() { platformComponents = previous })
	for i := range platformComponents {
		if platformComponents[i].Key == "grafana" {
			platformComponents[i].Namespace = "mcp-observability"
		}
	}
	mock := &core.MockExecutor{}
	mgr := NewManager(core.NewTestKubectlClient(mock), zap.NewNop())
	if err := mgr.PortForwardTarget("grafana", 0, "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	cmd := mock.LastCommand()
	for i, arg := range cmd.Args {
		if arg == "-n" {
			if cmd.Args[i+1] != "mcp-observability" {
				t.Fatalf("wrong namespace: %v", cmd.Args)
			}
			return
		}
	}
	t.Fatalf("missing namespace: %v", cmd.Args)
}
