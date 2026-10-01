package sentinel

import (
	"go.uber.org/zap"
	"mcp-runtime/internal/cli/core"
	"testing"
)

func TestPortForwardUsesComponentNamespace(t *testing.T) {
	// Placement can change independently of identity. Use a local catalog copy
	// so the canonical inventory and other commands keep legacy placement.
	previous := sentinelComponents
	sentinelComponents = append([]sentinelComponent(nil), previous...)
	t.Cleanup(func() { sentinelComponents = previous })
	for i := range sentinelComponents {
		if sentinelComponents[i].Key == "grafana" {
			sentinelComponents[i].Namespace = "mcp-observability"
		}
	}
	mock := &core.MockExecutor{}
	mgr := NewSentinelManager(core.NewTestKubectlClient(mock), zap.NewNop())
	if err := mgr.PortForwardSentinelTarget("grafana", 0, "127.0.0.1"); err != nil {
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
