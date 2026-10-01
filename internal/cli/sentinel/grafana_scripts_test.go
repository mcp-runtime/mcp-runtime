package sentinel

import (
	"testing"

	"mcp-runtime/internal/cli/core"
)

// The real kubectl client rejects newlines, tabs and carriage returns in any
// argument, and fake executors bypass that validator. Run the production
// validator over every script so a multi-line script cannot ship again.
func TestGrafanaScriptsPassKubectlArgumentValidation(t *testing.T) {
	validate := core.NoControlChars()
	for name, script := range map[string]string{
		"probe":  grafanaProbeScript,
		"backup": grafanaBackupScript,
		"reset":  grafanaResetScript,
	} {
		spec := core.ExecSpec{Name: "kubectl", Args: grafanaExecArgs(script)}
		if err := validate(spec); err != nil {
			t.Errorf("%s script rejected by kubectl argument validation: %v", name, err)
		}
	}
}
