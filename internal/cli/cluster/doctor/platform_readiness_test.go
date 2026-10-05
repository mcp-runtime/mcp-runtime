package doctor

import (
	"strings"
	"testing"

	"mcp-runtime/internal/cli/core"
)

func TestPlatformAPIReadinessUsesOwnerNamespace(t *testing.T) {
	tests := []struct {
		name      string
		namespace string
		service   string
		check     func(core.KubectlRunner) DoctorCheck
	}{
		{"platform API", "mcp-platform", doctorPlatformAPIService, checkPlatformPlatformAPIReadiness},
		{"analytics API", "mcp-observability", doctorAnalyticsAPIService, checkPlatformAnalyticsAPIReadiness},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seenDeployment := false
			mock := &core.MockExecutor{CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				args := strings.Join(spec.Args, " ")
				switch {
				case strings.Contains(args, "get namespace "+tt.namespace+" "):
					return &core.MockCommand{OutputData: []byte(tt.namespace)}
				case strings.Contains(args, "get deploy -n "+tt.namespace+" "+tt.service+" "):
					seenDeployment = true
					return &core.MockCommand{OutputData: []byte("0/1")}
				default:
					t.Fatalf("unexpected kubectl command: %s", args)
					return &core.MockCommand{}
				}
			}}
			check := tt.check(core.NewTestKubectlClient(mock))
			if !seenDeployment || check.OK || !strings.Contains(check.Remedy, "kubectl -n "+tt.namespace) {
				t.Fatalf("check = %+v; seenDeployment = %t", check, seenDeployment)
			}
		})
	}
}
