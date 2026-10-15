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

func TestPlatformAPIReadinessRetriesServiceEndpointConvergence(t *testing.T) {
	var probed []string
	mock := &core.MockExecutor{CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
		args := strings.Join(spec.Args, " ")
		switch {
		case strings.Contains(args, "get namespace mcp-platform "):
			return &core.MockCommand{OutputData: []byte("mcp-platform")}
		case strings.Contains(args, "get deploy -n mcp-platform mcp-platform-api "):
			return &core.MockCommand{OutputData: []byte("1/1")}
		case strings.Contains(args, "run doctor-platform-probe-"):
			if !strings.Contains(args, "--retry-connrefused") || !strings.Contains(args, "--retry-max-time") {
				t.Fatalf("service probe lacks bounded connection-refusal retry: %s", args)
			}
			for _, path := range []string{"/health", "/ready"} {
				if strings.Contains(args, path) {
					probed = append(probed, path)
				}
			}
			return &core.MockCommand{}
		case strings.Contains(args, "get pod doctor-platform-probe-"):
			return &core.MockCommand{OutputData: []byte("Succeeded")}
		case strings.Contains(args, "logs doctor-platform-probe-"):
			return &core.MockCommand{OutputData: []byte("200")}
		case strings.Contains(args, "delete pod doctor-platform-probe-"):
			return &core.MockCommand{}
		default:
			t.Fatalf("unexpected kubectl command: %s", args)
			return &core.MockCommand{}
		}
	}}
	check := checkPlatformPlatformAPIReadiness(core.NewTestKubectlClient(mock))
	if !check.OK || strings.Join(probed, ",") != "/health,/ready" {
		t.Fatalf("check = %+v; probed = %v", check, probed)
	}
}
