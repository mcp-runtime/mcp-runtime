package sentinel_test

import (
	"testing"

	"go.uber.org/zap"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/sentinel"
)

func contains(slice []string, val string) bool {
	for _, s := range slice {
		if s == val {
			return true
		}
	}
	return false
}

func TestSentinelManager_ViewSentinelLogs(t *testing.T) {
	mock := &core.MockExecutor{}
	kubectl := core.NewTestKubectlClient(mock)
	mgr := sentinel.NewSentinelManager(kubectl, zap.NewNop())

	if err := mgr.ViewSentinelLogs("api", true, false, 50, "5m"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cmd := mock.LastCommand()
	if cmd.Name != "kubectl" {
		t.Fatalf("expected kubectl, got %q", cmd.Name)
	}
	for _, want := range []string{"logs", "-n", core.ComponentNamespace("platform-api"), "-l", "app=mcp-platform-api", "--all-containers=true", "--prefix=true", "--tail", "50", "--since", "5m", "-f"} {
		if !contains(cmd.Args, want) {
			t.Fatalf("expected %q in args, got %v", want, cmd.Args)
		}
	}
}

func TestSentinelManager_PortForwardSentinelTarget(t *testing.T) {
	mock := &core.MockExecutor{}
	kubectl := core.NewTestKubectlClient(mock)
	mgr := sentinel.NewSentinelManager(kubectl, zap.NewNop())

	if err := mgr.PortForwardSentinelTarget("grafana", 0, "0.0.0.0"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cmd := mock.LastCommand()
	for _, want := range []string{"port-forward", "-n", core.ComponentNamespace("grafana"), "service/grafana", "3000:3000", "--address", "0.0.0.0"} {
		if !contains(cmd.Args, want) {
			t.Fatalf("expected %q in args, got %v", want, cmd.Args)
		}
	}
}

func TestSentinelManager_RestartSentinel(t *testing.T) {
	mock := &core.MockExecutor{}
	kubectl := core.NewTestKubectlClient(mock)
	mgr := sentinel.NewSentinelManager(kubectl, zap.NewNop())

	if err := mgr.RestartSentinel("processor", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cmd := mock.LastCommand()
	for _, want := range []string{"rollout", "restart", "deployment/mcp-processor", "-n", core.ComponentNamespace("processor")} {
		if !contains(cmd.Args, want) {
			t.Fatalf("expected %q in args, got %v", want, cmd.Args)
		}
	}
}

func TestSentinelManager_ShowSentinelEventsCoversEveryOwnerNamespace(t *testing.T) {
	mock := &core.MockExecutor{}
	mgr := sentinel.NewSentinelManager(core.NewTestKubectlClient(mock), zap.NewNop())
	if err := mgr.ShowSentinelEvents(); err != nil {
		t.Fatal(err)
	}
	wantNamespaces := []string{core.NamespaceMCPRuntime, core.ComponentNamespace("platform-api"), core.ComponentNamespace("analytics-api"), core.ComponentNamespace("promtail")}
	var eventCommands []core.ExecSpec
	for _, command := range mock.Commands {
		if len(command.Args) >= 2 && command.Args[0] == "get" && command.Args[1] == "events" {
			eventCommands = append(eventCommands, command)
		}
	}
	if len(eventCommands) != len(wantNamespaces) {
		t.Fatalf("event commands = %d, want %d", len(eventCommands), len(wantNamespaces))
	}
	for i, namespace := range wantNamespaces {
		if !contains(eventCommands[i].Args, namespace) {
			t.Errorf("command %d does not query %s: %v", i, namespace, eventCommands[i].Args)
		}
	}
}
