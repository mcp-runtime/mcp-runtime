package root

import (
	"testing"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

func TestAddCommandsRegistersTopLevelCommands(t *testing.T) {
	root := &cobra.Command{Use: "mcp-runtime"}

	AddCommands(root, zap.NewNop())

	want := []string{
		"admin",
		"catalog",
		"cluster",
		"registry",
		"server",
		"access",
		"agent",
		"adapter",
		"auth",
		"bootstrap",
		"setup",
		"status",
		"sentinel",
		"team",
		"update",
	}
	got := root.Commands()
	if len(got) != len(want) {
		t.Fatalf("registered %d commands, want %d", len(got), len(want))
	}

	seen := make(map[string]bool, len(got))
	for _, cmd := range got {
		seen[cmd.Name()] = true
	}
	for _, name := range want {
		if !seen[name] {
			t.Fatalf("command %q was not registered", name)
		}
	}
}
