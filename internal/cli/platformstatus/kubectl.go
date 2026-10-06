package platformstatus

import (
	"fmt"
	"strings"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/kubeerr"
)

func runKubectlCombinedOutput(kubectl core.KubectlRunner, args []string) (string, error) {
	cmd, err := kubectl.CommandArgs(args)
	if err != nil {
		return "", err
	}
	output, execErr := cmd.CombinedOutput()
	return strings.TrimSpace(string(output)), execErr
}

// CheckClusterStatusQuiet probes cluster connectivity without printing status.
func CheckClusterStatusQuiet(kubectl core.KubectlRunner) error {
	output, err := runKubectlCombinedOutput(kubectl, []string{"--request-timeout=8s", "cluster-info"})
	if err == nil {
		return nil
	}
	detail := kubeerr.CommandDetail(output, err)
	if hint, handled := kubeerr.SetupHint(detail); handled {
		return core.WrapWithBase(core.ErrClusterNotAccessible, err, hint)
	}
	return core.WrapWithBase(core.ErrClusterNotAccessible, err, fmt.Sprintf("cluster not accessible: %s", detail))
}
