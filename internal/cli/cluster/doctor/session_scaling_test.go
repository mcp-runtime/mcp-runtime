package doctor

import (
	"strings"
	"testing"

	"mcp-runtime/internal/cli/core"
)

func TestCheckSessionLocalDeploymentScaling(t *testing.T) {
	ui := `{"spec":{"template":{"spec":{"containers":[{"env":[{"name":"UI_SESSION_STORE","value":"postgres"}]}]}}}}`
	t.Run("passes when the UI uses postgres sessions", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				switch {
				case contains(spec.Args, "namespace"):
					return &core.MockCommand{OutputData: []byte(componentNamespace("ui"))}
				case contains(spec.Args, "mcp-ui"):
					return &core.MockCommand{OutputData: []byte(ui)}
				default:
					return &core.MockCommand{}
				}
			},
		}
		check := checkSessionLocalDeploymentScaling(core.NewTestKubectlClient(mock))
		if !check.OK {
			t.Fatalf("expected ok check, got detail=%q remedy=%q", check.Detail, check.Remedy)
		}
	})

	t.Run("fails when the UI session store is memory", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				switch {
				case contains(spec.Args, "namespace"):
					return &core.MockCommand{OutputData: []byte(componentNamespace("ui"))}
				case contains(spec.Args, "mcp-ui"):
					return &core.MockCommand{OutputData: []byte(`{"spec":{"template":{"spec":{"containers":[{"env":[{"name":"UI_SESSION_STORE","value":"memory"}]}]}}}}`)}
				default:
					return &core.MockCommand{}
				}
			},
		}
		check := checkSessionLocalDeploymentScaling(core.NewTestKubectlClient(mock))
		if check.OK {
			t.Fatal("expected failure when the session store is memory")
		}
		if !strings.Contains(check.Detail, "memory") {
			t.Fatalf("detail = %q, want memory store called out", check.Detail)
		}
	})
}

func TestParseDoctorReplicaCount(t *testing.T) {
	got, err := parseDoctorReplicaCount(" 3 ")
	if err != nil || got != 3 {
		t.Fatalf("parseDoctorReplicaCount() = (%d, %v), want (3, nil)", got, err)
	}
}
