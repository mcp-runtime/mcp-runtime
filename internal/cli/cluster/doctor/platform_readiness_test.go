package doctor

import (
	"fmt"
	"strings"
	"testing"

	"mcp-runtime/internal/cli/core"
)

func TestDoctorCurlServiceEndpointFinalStatus(t *testing.T) {
	for _, tc := range []struct {
		name, logs, status string
		failedPod          bool
	}{
		{name: "clean success", logs: "200", status: "200"},
		{name: "recovered connection refusal", logs: "curl: (7) Failed to connect: Couldn't connect to server\n200\n", status: "200"},
		{name: "final failure", logs: "diagnostic: earlier HTTP 200\n503\n", status: "503"},
		{name: "missing status", logs: "curl: (7) connection refused"},
		{name: "embedded status", logs: "diagnostic: HTTP 200 retries"},
		{name: "invalid status", logs: "000"},
		{name: "out of range", logs: "600"},
		{name: "failed curl pod", logs: "200", failedPod: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deleted := false
			mock := &core.MockExecutor{CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				args := strings.Join(spec.Args, " ")
				switch {
				case strings.Contains(args, "run doctor-platform-probe-"):
					return &core.MockCommand{}
				case strings.Contains(args, "get pod doctor-platform-probe-"):
					if tc.failedPod {
						return &core.MockCommand{OutputData: []byte("Failed")}
					}
					return &core.MockCommand{OutputData: []byte("Succeeded")}
				case strings.Contains(args, "logs doctor-platform-probe-"):
					return &core.MockCommand{OutputData: []byte(tc.logs)}
				case strings.Contains(args, "delete pod doctor-platform-probe-"):
					deleted = true
					return &core.MockCommand{}
				default:
					return &core.MockCommand{OutputErr: fmt.Errorf("unexpected kubectl command: %s", args)}
				}
			}}
			status, err := doctorCurlServiceEndpoint(core.NewTestKubectlClient(mock), "http://platform.example/health")
			if tc.status == "" || tc.failedPod {
				if err == nil {
					t.Fatalf("accepted invalid/failed probe: status=%q", status)
				}
			} else if err != nil || status != tc.status {
				t.Fatalf("status=%q err=%v; want %q", status, err, tc.status)
			}
			if !deleted {
				t.Fatal("probe pod was not cleaned up")
			}
		})
	}
}

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
